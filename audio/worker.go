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
	"regexp"
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

// DefaultFastLanguages are the languages whose fast-translation models are
// downloaded when -voice-lang doesn't name any (about 75 MB each).
var DefaultFastLanguages = []string{"ru", "uk", "pl", "tr", "de", "es", "fr"}

// Config describes how to run the worker.
type Config struct {
	UV          string // path to the uv executable
	DataDir     string // where the worker script and models are kept
	Model       string // Whisper model name ("auto" picks one for this PC)
	Device      string // "cpu" (default, keeps the GPU free for the game) or "cuda"
	Threads     int    // CPU threads for Whisper; 0 = auto
	Language    string // spoken language (ISO code), comma-separated candidates, or "" for auto-detect
	AudioDevice string // capture device name/substring; "" = what you hear (loopback)
	Live        bool   // continuously transcribe speech; otherwise only on Capture()
	BufferSecs  int    // seconds of audio kept in memory for Capture()
	// MaxSpeechSecs (live mode) transcribes an utterance once it is this
	// long, even if the speaker hasn't paused yet; 0 = the worker's default.
	MaxSpeechSecs int
	// AppAudio (Windows 10 20H1+/11) records only this program's audio, e.g.
	// "cs2.exe", while it runs, so Discord, music or videos are never
	// transcribed. Before the program starts, or if that isn't possible, the
	// worker records everything the user hears. Ignored with AudioDevice.
	AppAudio string
	// Denoise cleans speech with RNNoise before it is transcribed (only the
	// audio that is transcribed, so there is no load while nobody talks). It
	// is skipped with a warning where RNNoise isn't available (macOS < 26).
	Denoise bool
	// FastTranslate enables the worker's offline Opus-MT translation to
	// English (Worker.Translate). The models of FastLanguages (default:
	// DefaultFastLanguages) are downloaded when the worker starts or is
	// prepared, never while playing; other languages use the LLM.
	FastTranslate bool
	FastLanguages []string

	// Progress, if set, is called with what the worker is doing while it
	// starts: installing packages, downloading and loading the model.
	Progress func(Progress)
}

// Progress describes a step of the worker's startup. Total > 0 means the
// step can show a progress bar (Unit "B" = bytes).
type Progress struct {
	Message     string
	Done, Total int64
	Unit        string
}

// Kind tells what a Transcript event means.
type Kind int

const (
	KindText  Kind = iota // recognised speech in Text
	KindStart             // a transcription job started (Pieces utterances)
	KindDone              // a transcription job finished (Found: any speech)
)

// Transcript is a piece of recognised speech, or the start/end of a job.
type Transcript struct {
	Kind                Kind    `json:"-"`
	Source              string  `json:"source"` // "live" or "capture"
	Text                string  `json:"text"`
	Language            string  `json:"language"`
	LanguageProbability float64 `json:"language_probability"`
	AudioSeconds        float64 `json:"audio_seconds"`
	ProcessingSeconds   float64 `json:"processing_seconds"`
	Pieces              int     `json:"pieces"` // KindStart: utterances found in the audio
	Found               bool    `json:"found"`  // KindDone: the job produced text
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
	Done    int64    `json:"done"`
	Total   int64    `json:"total"`
	Unit    string   `json:"unit"`
	ID      int      `json:"id"`
	OK      bool     `json:"ok"`
}

// Worker is a running speech-to-text worker process.
type Worker struct {
	Info        Info
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	mu          sync.Mutex
	transcripts chan Transcript
	exited      chan struct{}

	nextID  int
	pending map[int]chan string // fast translations in flight; "" = use the LLM
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
	if c.FastTranslate && len(c.FastLanguages) == 0 {
		c.FastLanguages = DefaultFastLanguages
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
		if c.MaxSpeechSecs > 0 {
			args = append(args, "--max-speech-seconds", strconv.Itoa(c.MaxSpeechSecs))
		}
	}
	if c.AppAudio != "" && !strings.EqualFold(c.AppAudio, "off") && c.AudioDevice == "" {
		args = append(args, "--app-audio", c.AppAudio)
	}
	if c.Denoise {
		args = append(args, "--denoise")
	}
	if c.FastTranslate {
		args = append(args, "--nmt-langs", strings.Join(c.Resolve().FastLanguages, ","))
	}
	args = append(args, extra...)

	cmd := exec.CommandContext(ctx, c.UV, args...)
	cmd.Env = append(os.Environ(),
		"PYTHONUNBUFFERED=1",
		"PYTHONIOENCODING=utf-8",
		"HF_HUB_DISABLE_TELEMETRY=1",
		"HF_HUB_DISABLE_SYMLINKS_WARNING=1",
		"HF_HUB_VERBOSITY=error",
	)
	return cmd, nil
}

// process is a started worker with its output being parsed.
type process struct {
	cmd    *exec.Cmd
	events chan event    // protocol events from stdout; closed at EOF
	logs   chan struct{} // closed when stderr is drained
	cfg    Config

	mu      sync.Mutex
	ready   bool     // after "ready", stderr lines are logged instead of shown as progress
	started bool     // the Python worker itself is running (uv finished installing)
	tail    []string // last stderr lines, shown if startup fails
}

func (c Config) spawn(ctx context.Context, withStdin bool, extra ...string) (*process, io.WriteCloser, error) {
	cmd, err := c.command(ctx, extra...)
	if err != nil {
		return nil, nil, err
	}
	var stdin io.WriteCloser
	if withStdin {
		if stdin, err = cmd.StdinPipe(); err != nil {
			return nil, nil, err
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("starting speech worker: %w", err)
	}
	p := &process{cmd: cmd, events: make(chan event, 16), logs: make(chan struct{}), cfg: c}
	p.progress(Progress{Message: "Starting speech engine"})
	go p.readStdout(stdout)
	go p.readStderr(stderr)
	return p, stdin, nil
}

func (p *process) progress(pr Progress) {
	if p.cfg.Progress != nil {
		p.cfg.Progress(pr)
	}
}

func (p *process) readStdout(r io.Reader) {
	defer close(p.events)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var ev event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || ev.Event == "" {
			continue // not a protocol line
		}
		p.mu.Lock()
		p.started = true
		ready := p.ready
		p.mu.Unlock()
		switch ev.Event {
		case "status":
			if !ready {
				p.progress(Progress{Message: ev.Message})
			}
			continue
		case "progress":
			if !ready {
				p.progress(Progress{Message: ev.Message, Done: ev.Done, Total: ev.Total, Unit: ev.Unit})
			}
			continue
		case "ready":
			p.mu.Lock()
			p.ready = true
			p.mu.Unlock()
		}
		p.events <- ev
	}
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// readStderr shows uv's install messages and the worker's logs as progress
// during startup, and logs them afterwards.
func (p *process) readStderr(r io.Reader) {
	defer close(p.logs)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sc.Split(scanLinesCR)
	for sc.Scan() {
		line := strings.TrimSpace(ansiRe.ReplaceAllString(sc.Text(), ""))
		if line == "" {
			continue
		}
		p.mu.Lock()
		ready, started := p.ready, p.started
		p.tail = append(p.tail, line)
		if len(p.tail) > 15 {
			p.tail = p.tail[1:]
		}
		p.mu.Unlock()
		switch {
		case ready:
			log.Printf("%s", line)
		case !started:
			// before Python runs, everything on stderr comes from uv
			p.progress(Progress{Message: "Installing speech engine: " + line})
		default:
			p.progress(Progress{Message: strings.TrimPrefix(line, "[speech] ")})
		}
	}
}

// scanLinesCR splits on \n and \r, so progress output redrawn with \r becomes separate lines.
func scanLinesCR(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// startupError explains why the worker failed before becoming ready,
// including its last messages.
func (p *process) startupError(err error) error {
	<-p.logs
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.tail) == 0 {
		return err
	}
	return fmt.Errorf("%w\nLast messages from the speech engine:\n  %s", err, strings.Join(p.tail, "\n  "))
}

// Start launches the worker and waits until the model is loaded and warmed
// up, so the first transcription is fast. The first start installs Python
// packages and downloads the model, which can take a few minutes (reported
// through cfg.Progress); later starts take a few seconds.
func Start(ctx context.Context, cfg Config) (*Worker, error) {
	cfg = cfg.Resolve()
	p, stdin, err := cfg.spawn(ctx, true)
	if err != nil {
		return nil, err
	}
	w := &Worker{
		cmd:         p.cmd,
		stdin:       stdin,
		transcripts: make(chan Transcript, 16),
		exited:      make(chan struct{}),
		pending:     map[int]chan string{},
	}
	go func() {
		<-p.logs // drain stderr before Wait closes the pipe
		p.cmd.Wait()
		close(w.exited)
	}()

	ready := make(chan error, 1)
	go w.read(p.events, ready)

	select {
	case err := <-ready:
		if err != nil {
			w.Close()
			return nil, p.startupError(err)
		}
		return w, nil
	case <-ctx.Done():
		w.Close()
		return nil, ctx.Err()
	}
}

func (w *Worker) read(events <-chan event, ready chan<- error) {
	defer close(w.transcripts)
	isReady := false
	signal := func(err error) {
		if !isReady {
			isReady = true
			ready <- err
		}
	}
	defer signal(errors.New("speech worker exited during startup"))

	for ev := range events {
		switch ev.Event {
		case "ready":
			w.Info = ev.Info
			signal(nil)
		case "job_start":
			t := ev.Transcript
			t.Kind = KindStart
			w.transcripts <- t
		case "transcript":
			t := ev.Transcript
			t.Kind = KindText
			w.transcripts <- t
		case "job_done":
			t := ev.Transcript
			t.Kind = KindDone
			w.transcripts <- t
		case "translation":
			w.mu.Lock()
			ch := w.pending[ev.ID]
			delete(w.pending, ev.ID)
			w.mu.Unlock()
			if ch != nil {
				if ev.OK {
					ch <- ev.Text
				} else {
					ch <- ""
				}
			}
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

// Transcripts delivers recognised speech and job start/end events. It is
// closed when the worker exits.
func (w *Worker) Transcripts() <-chan Transcript { return w.transcripts }

// Capture transcribes the last `seconds` of audio. The worker answers on
// Transcripts with a KindStart event, a KindText event per utterance and a
// KindDone event.
func (w *Worker) Capture(seconds int) error {
	return w.send(fmt.Sprintf("capture %d", seconds))
}

// fastTimeout bounds how long a fast translation may take before the caller
// falls back to the LLM. A loaded model answers in tens of milliseconds.
const fastTimeout = time.Second

// Translate translates text spoken in lang into English with the worker's
// small offline model. ok is false when that isn't possible right now (model
// still downloading, language unsupported, worker busy): use the LLM instead.
func (w *Worker) Translate(ctx context.Context, lang, text string) (out string, ok bool) {
	text = strings.Join(strings.Fields(text), " ")
	if w == nil || lang == "" || strings.ContainsAny(lang, " \t") || text == "" {
		return "", false
	}
	ch := make(chan string, 1)
	w.mu.Lock()
	w.nextID++
	id := w.nextID
	w.pending[id] = ch
	w.mu.Unlock()
	if err := w.send(fmt.Sprintf("translate %d %s %s", id, lang, text)); err != nil {
		w.forget(id)
		return "", false
	}
	select {
	case out = <-ch:
		return out, out != ""
	case <-time.After(fastTimeout):
	case <-ctx.Done():
	case <-w.exited:
	}
	w.forget(id)
	return "", false
}

func (w *Worker) forget(id int) {
	w.mu.Lock()
	delete(w.pending, id)
	w.mu.Unlock()
}

// SetPrompt gives Whisper a vocabulary hint (e.g. the current map's callouts).
func (w *Worker) SetPrompt(text string) error {
	text = strings.Join(strings.Fields(text), " ") // one line, one command
	return w.send("prompt " + text)
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
	p, _, err := cfg.Resolve().spawn(ctx, false, extra...)
	if err != nil {
		return nil, err
	}
	var found, last *event
	for ev := range p.events {
		switch {
		case ev.Event == want && found == nil:
			e := ev
			found = &e
		case ev.Event == "error":
			e := ev
			last = &e
		case ev.Event == "warning":
			log.Printf("Speech: %s", ev.Message)
		}
	}
	<-p.logs
	runErr := p.cmd.Wait()
	switch {
	case found != nil:
		return found, nil
	case last != nil:
		return nil, p.startupError(fmt.Errorf("speech worker: %s", last.Message))
	case runErr != nil:
		return nil, p.startupError(fmt.Errorf("speech worker failed: %w", runErr))
	}
	return nil, p.startupError(errors.New("speech worker returned no result"))
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
