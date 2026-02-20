package audio

import (
	"bufio"
	"context"
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

	"github.com/fsnotify/fsnotify"
	"github.com/micha/cs-ingame-translate/translator"
)

type Listener struct {
	outputDir      string
	ffmpegCmd      *exec.Cmd
	pythonCmd      *exec.Cmd
	pythonStdin    io.WriteCloser
	pythonStdout   *bufio.Scanner
	stop           chan struct{}
	transcriptions chan string
	mu             sync.Mutex
	fileQueue      chan string
	useDocker      bool
}

func useDockerWhisper() bool {
	return os.Getenv("USE_DOCKER_WHISPER") == "1"
}

func NewListener(scriptPath string) (*Listener, error) {
	if useDockerWhisper() {
		return newDockerListener()
	}
	return newLocalListener(scriptPath)
}

func newLocalListener(scriptPath string) (*Listener, error) {
	tmpDir, err := os.MkdirTemp("", "cs-translate-audio")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("transcriber script not found at %s", scriptPath)
	}

	cwd, _ := os.Getwd()
	var pythonPath string
	if runtime.GOOS == "windows" {
		pythonPath = filepath.Join(cwd, "venv", "Scripts", "python.exe")
	} else {
		pythonPath = filepath.Join(cwd, "venv", "bin", "python3")
	}

	if _, err := os.Stat(pythonPath); os.IsNotExist(err) {
		if runtime.GOOS == "windows" {
			pythonPath = "python"
		} else {
			pythonPath = "python3"
		}
	}

	cmd := exec.Command(pythonPath, "-u", scriptPath)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get python stdin: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get python stdout: %w", err)
	}

	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), fmt.Sprintf("WHISPER_MODEL=%s", getWhisperModel()))

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start transcriber.py: %w", err)
	}

	scanner := bufio.NewScanner(stdout)

	if scanner.Scan() {
		text := scanner.Text()
		if !strings.Contains(text, "READY") {
			log.Printf("Transcriber initialization: %s", text)
			for scanner.Scan() {
				text = scanner.Text()
				if strings.Contains(text, "READY") {
					break
				}
				log.Printf("Transcriber init: %s", text)
			}
		}
	}

	l := &Listener{
		outputDir:      tmpDir,
		pythonCmd:      cmd,
		pythonStdin:    stdin,
		pythonStdout:   scanner,
		stop:           make(chan struct{}),
		transcriptions: make(chan string),
		fileQueue:      make(chan string, 100),
		useDocker:      false,
	}

	go l.worker()

	return l, nil
}

func newDockerListener() (*Listener, error) {
	log.Println("Using Docker-based Whisper transcription")

	containerName := "cs-translate"

	checkCmd := exec.Command("docker", "ps", "--filter", "name="+containerName, "--format", "{{.Names}}")
	output, err := checkCmd.Output()
	if err != nil || strings.TrimSpace(string(output)) != containerName {
		return nil, fmt.Errorf("Docker container '%s' is not running. Please run cs-translate first to start the container", containerName)
	}

	tmpDir, err := os.MkdirTemp("", "cs-translate-audio")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	// Use persistent docker exec command
	cmd := exec.Command("docker", "exec", "-i", "cs-translate", "python3", "-u", "/app/transcriber.py")
	cmd.Env = append(os.Environ(), fmt.Sprintf("WHISPER_MODEL=%s", getWhisperModel()))

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get docker stdin: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get docker stdout: %w", err)
	}

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start docker process: %w", err)
	}

	// Wait for READY signal from transcriber
	scanner := bufio.NewScanner(stdout)
	if scanner.Scan() {
		text := scanner.Text()
		if !strings.Contains(text, "READY") {
			log.Printf("Docker Transcriber initialization: %s", text)
			for scanner.Scan() {
				text = scanner.Text()
				if strings.Contains(text, "READY") {
					break
				}
				log.Printf("Docker Transcriber init: %s", text)
			}
		}
	}

	l := &Listener{
		outputDir:      tmpDir,
		pythonCmd:      cmd,
		pythonStdin:    stdin,
		pythonStdout:   scanner,
		stop:           make(chan struct{}),
		transcriptions: make(chan string),
		fileQueue:      make(chan string, 100),
		useDocker:      true,
	}

	go l.dockerPersistentWorker()

	return l, nil
}

func (l *Listener) dockerPersistentWorker() {
	for path := range l.fileQueue {
		// Start timing for transcription
		transcribeStart := time.Now()

		// 1. Copy file to container
		fileName := filepath.Base(path)
		containerPath := "/tmp/" + fileName
		// We use `docker cp` to copy the file into the container
		cpCmd := exec.Command("docker", "cp", path, "cs-translate:"+containerPath)
		if err := cpCmd.Run(); err != nil {
			log.Printf("Failed to copy file to container: %v", err)
			os.Remove(path)
			continue
		}

		// 2. Send container path to python
		l.mu.Lock()
		_, err := fmt.Fprintln(l.pythonStdin, containerPath)
		l.mu.Unlock()

		if err != nil {
			log.Printf("Failed to send path to docker transcriber: %v", err)
			continue
		}

		// 3. Read result
		if l.pythonStdout.Scan() {
			text := strings.TrimSpace(l.pythonStdout.Text())
			transcribeDuration := time.Since(transcribeStart)
			if text != "" {
				l.transcriptions <- fmt.Sprintf("%s|%.2f", text, transcribeDuration.Seconds())
			}
		} else {
			if err := l.pythonStdout.Err(); err != nil {
				log.Printf("Error reading from docker transcriber: %v", err)
			}
			return
		}

		// 4. Cleanup host file
		os.Remove(path)

		// 5. Cleanup container file (async)
		go exec.Command("docker", "exec", "cs-translate", "rm", containerPath).Run()
	}
}

func (l *Listener) dockerWorker() {
	// Deprecated in favor of dockerPersistentWorker, keeping for reference if needed but not used
}

func (l *Listener) Start(ctx context.Context, device string) error {
	var cmd *exec.Cmd
	pattern := filepath.Join(l.outputDir, "audio_%03d.wav")
	//segment_time
	segmentTime := "2"

	if runtime.GOOS == "windows" {
		// Windows: Use virtual-audio-capturer from screen-capture-recorder
		// https://github.com/rdp/screen-capture-recorder-to-video-windows-free
		inputDevice := device
		if inputDevice == "" || inputDevice == "default" {
			inputDevice = "virtual-audio-capturer"
		}

		log.Printf("Starting audio listener on Windows device: %s", inputDevice)

		cmd = exec.CommandContext(ctx, "ffmpeg",
			"-f", "dshow", "-i", fmt.Sprintf("audio=%s", inputDevice),
			"-f", "segment", "-segment_time", segmentTime,
			"-c:a", "pcm_s16le", "-ar", "16000", "-ac", "1",
			"-reset_timestamps", "1",
			pattern,
		)
	} else {
		// Linux / PulseAudio
		source := device
		if source == "" || source == "default" {
			source = GetDefaultMonitorSource()
		}

		log.Printf("Starting audio listener on source: %s", source)

		cmd = exec.CommandContext(ctx, "ffmpeg",
			"-f", "pulse", "-i", source,
			"-f", "segment", "-segment_time", segmentTime,
			"-c:a", "pcm_s16le", "-ar", "16000", "-ac", "1",
			"-reset_timestamps", "1",
			pattern,
		)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start ffmpeg: %w", err)
	}
	l.ffmpegCmd = cmd

	go l.watchFiles(ctx)

	return nil
}

func (l *Listener) watchFiles(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Failed to create fsnotify watcher: %v", err)
		return
	}
	defer watcher.Close()

	if err := watcher.Add(l.outputDir); err != nil {
		log.Printf("Failed to watch tmp dir: %v", err)
		return
	}

	var lastFile string

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.stop:
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}

			if event.Op&fsnotify.Create == fsnotify.Create {
				if strings.HasSuffix(event.Name, ".wav") {
					if lastFile != "" && lastFile != event.Name {
						// Enqueue previous file
						l.fileQueue <- lastFile
					}
					lastFile = event.Name
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("Watcher error: %v", err)
		}
	}
}

func (l *Listener) worker() {
	for path := range l.fileQueue {
		// Wait a bit ensuring file closed
		time.Sleep(100 * time.Millisecond)

		// Check if audio is silent before transcribing
		if l.isSilent(path) {
			os.Remove(path)
			continue
		}

		// Start timing for transcription
		transcribeStart := time.Now()

		// Send to python
		// We hold a lock just in case, though this is the only writer
		l.mu.Lock()
		_, err := fmt.Fprintln(l.pythonStdin, path)
		l.mu.Unlock()

		if err != nil {
			log.Printf("Failed to send path to transcriber: %v", err)
			continue
		}

		// Read result
		// Assuming strict 1:1 request/response
		if l.pythonStdout.Scan() {
			text := strings.TrimSpace(l.pythonStdout.Text())
			transcribeDuration := time.Since(transcribeStart)
			if text != "" {
				// Include timing with transcription
				l.transcriptions <- fmt.Sprintf("%s|%.2f", text, transcribeDuration.Seconds())
			}
		} else {
			if err := l.pythonStdout.Err(); err != nil {
				log.Printf("Error reading from transcriber: %v", err)
			}
			// Scanner closed?
			return
		}

		// Remove file
		os.Remove(path)
	}
}

func (l *Listener) SubmitFile(path string) {
	l.fileQueue <- path
}

func (l *Listener) Transcriptions() <-chan string {
	return l.transcriptions
}

func (l *Listener) Stop() {
	close(l.stop)
	close(l.fileQueue)

	if l.ffmpegCmd != nil && l.ffmpegCmd.Process != nil {
		l.ffmpegCmd.Process.Kill()
	}

	if l.pythonCmd != nil && l.pythonCmd.Process != nil {
		l.pythonCmd.Process.Kill()
	}

	os.RemoveAll(l.outputDir)
}

func GetDefaultMonitorSource() string {
	out, err := exec.Command("pactl", "get-default-sink").Output()
	if err == nil {
		sink := strings.TrimSpace(string(out))
		if sink != "" {
			return sink + ".monitor"
		}
	}
	return "default.monitor"
}

func getWhisperModel() string {
	return translator.DefaultWhisperModel
}

func (l *Listener) isSilent(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", path,
		"-af", "volumedetect",
		"-f", "null", "-",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	output := string(out)

	if idx := strings.Index(output, "mean_volume:"); idx != -1 {
		volumeStr := output[idx+12:]
		if end := strings.Index(volumeStr, " dB"); end != -1 {
			volumeStr = volumeStr[:end]
			if vol, err := strconv.ParseFloat(strings.TrimSpace(volumeStr), 64); err == nil {
				return vol < -50
			}
		}
	}

	return false
}
