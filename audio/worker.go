// Package audio runs the speech-to-text worker (audio/transcriber.py).
//
// The worker is a small Python program executed through uv, which installs
// Python and the dependencies (faster-whisper, soundcard) on first use on
// Windows, Linux and macOS alike. It captures system audio itself (WASAPI
// loopback / PulseAudio+PipeWire monitor / BlackHole), detects speech with
// Silero VAD and transcribes it with Whisper on CTranslate2. Audio never
// touches the disk and no ffmpeg, Docker or PyTorch is required.
package audio

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed transcriber.py
var workerScript []byte

// PythonVersion is the interpreter uv provides for the worker. Pinned so the
// prebuilt wheels (ctranslate2, onnxruntime) are always available.
const PythonVersion = "3.12"

// Config describes how to run the worker.
type Config struct {
	UV          string // path to the uv executable
	DataDir     string // where the worker script and models are kept
	Model       string // Whisper model name ("auto" picks one for this PC)
	Device      string // "cpu" (default, keeps the GPU free for the game) or "cuda"
	Threads     int    // CPU threads for Whisper; 0 = auto
	Language    string // spoken language hint (ISO code) or "" for auto-detect
	AudioDevice string // capture device name/substring; "" = what you hear (loopback)
	Live        bool   // continuously transcribe speech; otherwise only on Capture()
	BufferSecs  int    // seconds of audio kept in memory for Capture()
}

// Transcript is a piece of recognised speech.
type Transcript struct {
	Source              string  `json:"source"` // "live" or "capture"
	Text                string  `json:"text"`
	Language            string  `json:"language"`
	LanguageProbability float64 `json:"language_probability"`
	AudioSeconds        float64 `json:"audio_seconds"`
	ProcessingSeconds   float64 `json:"processing_seconds"`
	Empty               bool    `json:"-"` // a Capture() found no speech
}

// Device is an audio input the worker can record from.
type Device struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	Loopback bool   `json:"loopback"`
}

// Info describes the running worker.
type Info struct {
	Model       string `json:"model"`
	Device      string `json:"device"`
	ComputeType string `json:"compute_type"`
	Threads     int    `json:"threads"`
	AudioDevice string `json:"audio_device"`
}

type event struct {
	Transcript
	Info
	Event   string   `json:"event"`
	Message string   `json:"message"`
	Fatal   bool     `json:"fatal"`
	Devices []Device `json:"devices"`
	Default string   `json:"default"`
}

// Worker is a running speech-to-text worker process.
type Worker struct {
	Info        Info
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	mu          sync.Mutex
	transcripts chan Transcript
	exited      chan struct{}
}

// Resolve fills in automatic settings tuned so Whisper never competes
// noticeably with the game for CPU time.
func (c Config) Resolve() Config {
	switch c.Device {
	case "":
		c.Device = "cpu"
	case "gpu":
		c.Device = "cuda"
	}
	if c.Model == "" || c.Model == "auto" {
		c.Model = AutoModel(c.Device, runtime.NumCPU())
	}
	if c.Threads <= 0 {
		c.Threads = AutoThreads(runtime.NumCPU())
	}
	if c.BufferSecs <= 0 {
		c.BufferSecs = 30
	}
	return c
}

// AutoModel picks the most accurate Whisper model that stays light on this machine.
func AutoModel(device string, cpus int) string {
	if device == "cuda" {
		return "large-v3-turbo"
	}
	if cpus <= 4 {
		return "base"
	}
	return "small"
}

// AutoThreads leaves the large majority of CPU cores to the game.
func AutoThreads(cpus int) int {
	switch {
	case cpus <= 4:
		return 1
	case cpus <= 8:
		return 2
	case cpus <= 12:
		return 3
	default:
		return 4
	}
}

func (c Config) scriptPath() (string, error) {
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(c.DataDir, "transcriber.py")
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, workerScript) {
		return path, nil
	}
	// write atomically: a half-written script would break uv's cache key
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, workerScript, 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

func (c Config) command(ctx context.Context, extra ...string) (*exec.Cmd, error) {
	if c.UV == "" {
		return nil, errors.New("uv is not available")
	}
	script, err := c.scriptPath()
	if err != nil {
		return nil, fmt.Errorf("writing worker script: %w", err)
	}
	args := []string{"run", "--python", PythonVersion}
	if c.Device == "cuda" && runtime.GOOS != "darwin" {
		// CUDA runtime libraries as wheels: no system CUDA/cuDNN install needed.
		args = append(args, "--with", "nvidia-cublas-cu12", "--with", "nvidia-cudnn-cu12>=9,<10")
	}
	args = append(args, "--script", script,
		"--model", c.Model,
		"--device", c.Device,
		"--threads", strconv.Itoa(c.Threads),
		"--buffer-seconds", strconv.Itoa(c.BufferSecs),
		"--models-dir", filepath.Join(c.DataDir, "models"),
	)
	if c.Language != "" {
		args = append(args, "--language", c.Language)
	}
	if c.AudioDevice != "" {
		args = append(args, "--audio-device", c.AudioDevice)
	}
	if c.Live {
		args = append(args, "--live")
	}
	args = append(args, extra...)

	cmd := exec.CommandContext(ctx, c.UV, args...)
	cmd.Env = append(os.Environ(),
		"PYTHONUNBUFFERED=1",
		"PYTHONIOENCODING=utf-8",
		"HF_HUB_DISABLE_TELEMETRY=1",
		"HF_HUB_DISABLE_SYMLINKS_WARNING=1",
	)
	cmd.Stderr = os.Stderr // install progress, model download and worker logs
	return cmd, nil
}

// Start launches the worker and waits until the model is loaded. The first
// start installs Python packages and downloads the model, which can take a
// few minutes; later starts take a second or two.
func Start(ctx context.Context, cfg Config) (*Worker, error) {
	cfg = cfg.Resolve()
	cmd, err := cfg.command(ctx)
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting speech worker: %w", err)
	}

	w := &Worker{
		cmd:         cmd,
		stdin:       stdin,
		transcripts: make(chan Transcript, 16),
		exited:      make(chan struct{}),
	}

	ready := make(chan error, 1)
	go w.read(stdout, ready)
	go func() {
		cmd.Wait()
		close(w.exited)
	}()

	select {
	case err := <-ready:
		if err != nil {
			w.Close()
			return nil, err
		}
		return w, nil
	case <-ctx.Done():
		w.Close()
		return nil, ctx.Err()
	}
}

func (w *Worker) read(stdout io.Reader, ready chan<- error) {
	defer close(w.transcripts)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	isReady := false
	signal := func(err error) {
		if !isReady {
			isReady = true
			ready <- err
		}
	}
	defer signal(errors.New("speech worker exited during startup (see messages above)"))

	for sc.Scan() {
		var ev event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue // not a protocol line
		}
		switch ev.Event {
		case "ready":
			w.Info = ev.Info
			signal(nil)
		case "transcript":
			w.transcripts <- ev.Transcript
		case "empty":
			w.transcripts <- Transcript{Source: ev.Source, Empty: true}
		case "warning":
			log.Printf("Speech: %s", ev.Message)
		case "error":
			if !isReady && ev.Fatal {
				signal(fmt.Errorf("speech worker: %s", ev.Message))
			} else {
				log.Printf("Speech error: %s", ev.Message)
			}
		}
	}
}

// Transcripts delivers recognised speech. It is closed when the worker exits.
func (w *Worker) Transcripts() <-chan Transcript { return w.transcripts }

// Capture transcribes the last `seconds` of audio (the result arrives on Transcripts).
func (w *Worker) Capture(seconds int) error {
	return w.send(fmt.Sprintf("capture %d", seconds))
}

func (w *Worker) send(line string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := io.WriteString(w.stdin, line+"\n")
	return err
}

// Close stops the worker, giving it a moment to finish cleanly.
func (w *Worker) Close() {
	if w == nil || w.cmd == nil {
		return
	}
	w.send("quit")
	w.stdin.Close() // EOF also makes the worker exit, even if uv lost the message
	select {
	case <-w.exited:
	case <-time.After(3 * time.Second):
		if w.cmd.Process != nil {
			w.cmd.Process.Kill()
		}
		<-w.exited
	}
}

// runOnce runs the worker in a one-shot mode and returns the first event of the given kind.
func runOnce(ctx context.Context, cfg Config, want string, extra ...string) (*event, error) {
	cmd, err := cfg.Resolve().command(ctx, extra...)
	if err != nil {
		return nil, err
	}
	cmd.Stdin = nil
	out, runErr := cmd.Output()
	var last *event
	for _, line := range strings.Split(string(out), "\n") {
		var ev event
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Event == want {
			return &ev, nil
		}
		if ev.Event == "error" {
			last = &ev
		}
	}
	if last != nil {
		return nil, fmt.Errorf("speech worker: %s", last.Message)
	}
	if runErr != nil {
		return nil, fmt.Errorf("speech worker failed: %w", runErr)
	}
	return nil, errors.New("speech worker returned no result")
}

// ListDevices returns the audio devices the worker can record from and the
// name of the default output (whose loopback is used by default).
func ListDevices(ctx context.Context, cfg Config) ([]Device, string, error) {
	ev, err := runOnce(ctx, cfg, "devices", "--list-devices")
	if err != nil {
		return nil, "", err
	}
	return ev.Devices, ev.Default, nil
}

// Prepare installs the worker's dependencies and downloads the model, then exits.
func Prepare(ctx context.Context, cfg Config) (Info, error) {
	ev, err := runOnce(ctx, cfg, "ready", "--prepare")
	if err != nil {
		return Info{}, err
	}
	return ev.Info, nil
}
