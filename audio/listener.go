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
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Listener struct {
	outputDir      string
	ffmpegCmd      *exec.Cmd
	pythonCmd      *exec.Cmd
	pythonStdin    io.WriteCloser
	pythonStdout   *bufio.Scanner
	stop           chan struct{}
	transcriptions chan string
	mu             sync.Mutex // Protects python process access if needed, though we use a loop
	fileQueue      chan string
}

func NewListener(scriptPath string) (*Listener, error) {
	tmpDir, err := os.MkdirTemp("", "cs-translate-audio")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	// Verify the script exists
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("transcriber script not found at %s", scriptPath)
	}

	// Prepare python command
	cwd, _ := os.Getwd()
	// Check for venv python first, then system python
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

	cmd := exec.Command(pythonPath, "-u", scriptPath) // -u for unbuffered binary stdout
	// Actually python script uses flush=True so -u might not be strictly needed but good practice.

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get python stdin: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get python stdout: %w", err)
	}

	// Redirect stderr to our stderr for debugging
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start transcriber.py: %w", err)
	}

	scanner := bufio.NewScanner(stdout)

	// Wait for "READY"
	if scanner.Scan() {
		text := scanner.Text()
		if !strings.Contains(text, "READY") {
			// Might be a warning or loading message if not filtered correctly
			log.Printf("Transcriber initialization: %s", text)
			// Loop until READY
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
	}

	go l.worker()

	return l, nil
}

func (l *Listener) Start(ctx context.Context, device string) error {
	var cmd *exec.Cmd
	pattern := filepath.Join(l.outputDir, "audio_%03d.wav")

	if runtime.GOOS == "windows" {
		// Windows: Use dshow
		// If device is empty or default, we can't easily guess.
		// "audio=Stereo Mix (Realtek High Definition Audio)" is typical for loopback if enabled.
		// "audio=Microphone (Realtek High Definition Audio)" for mic.
		// We will default to a generic error/warning if not specified,
		// but let's try to support "default" if user provided nothing, which might fail or need specific handling.

		inputDevice := device
		if inputDevice == "" || inputDevice == "default" {
			// On Windows, there is no simple "default" for dshow that reliably works for everyone without configuration.
			// We list available devices for the user.
			devices, err := listWindowsAudioDevices()
			msg := "On Windows, you must specify the audio device name using -audiodevice.\n"
			if err == nil && devices != "" {
				msg += "Available Devices:\n" + devices
			} else {
				msg += "Could not list devices automatically. Run 'ffmpeg -list_devices true -f dshow -i dummy' to see them."
			}
			return fmt.Errorf("%s", msg)
		}

		log.Printf("Starting audio listener on Windows device: %s", inputDevice)

		// If the user provided "Microphone", we format it as audio="Microphone"
		// We prepend "audio=" if not present (simple heuristic)
		arg := inputDevice
		if !strings.HasPrefix(arg, "audio=") {
			arg = fmt.Sprintf("audio=%s", inputDevice)
		}

		cmd = exec.CommandContext(ctx, "ffmpeg",
			"-f", "dshow", "-i", arg,
			"-f", "segment", "-segment_time", "5",
			"-c:a", "pcm_s16le", "-ar", "16000", "-ac", "1",
			"-reset_timestamps", "1",
			pattern,
		)
	} else {
		// Linux / PulseAudio
		source := device
		if source == "" || source == "default" {
			source = getDefaultMonitorSource()
		}

		log.Printf("Starting audio listener on source: %s", source)

		cmd = exec.CommandContext(ctx, "ffmpeg",
			"-f", "pulse", "-i", source,
			"-f", "segment", "-segment_time", "5",
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
			if text != "" {
				l.transcriptions <- text
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

func getDefaultMonitorSource() string {
	out, err := exec.Command("pactl", "get-default-sink").Output()
	if err == nil {
		sink := strings.TrimSpace(string(out))
		if sink != "" {
			return sink + ".monitor"
		}
	}
	return "default.monitor"
}

func listWindowsAudioDevices() (string, error) {
	// ffmpeg -list_devices true -f dshow -i dummy
	// This command usually returns exit code 1 because "dummy" input fails,
	// but the device list is printed to stderr.
	cmd := exec.Command("ffmpeg", "-list_devices", "true", "-f", "dshow", "-i", "dummy")
	// We combine stdout/stderr because it prints to stderr
	out, _ := cmd.CombinedOutput()
	output := string(out)

	// Parse the output to be friendlier?
	// Output format is usually: [dshow @ ...]  "Device Name"
	// Let's just return the raw output relevant lines to keep it simple but filter a bit.
	var result strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(output))
	printing := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "DirectShow audio devices") {
			printing = true
			continue
		}
		if strings.Contains(line, "DirectShow video devices") {
			printing = false
		}
		if printing {
			// Filter out internal ffmpeg logs
			if strings.Contains(line, "]  \"") {
				// Line looks like: [dshow @ 0000...]  "Microphone (Realtek Audio)"
				// We want to extract just the name to show clearly, or keep the line.
				// Let's keep the line but maybe trim the prefix?
				parts := strings.SplitN(line, "] ", 2)
				if len(parts) == 2 {
					result.WriteString(" - " + strings.TrimSpace(parts[1]) + "\n")
				}
			}
		}
	}

	// If dshow found nothing, try WASAPI
	if result.Len() == 0 {
		cmd = exec.Command("ffmpeg", "-list_devices", "true", "-f", "wasapi", "-i", "dummy")
		out, _ = cmd.CombinedOutput()
		output = string(out) // wasapi output

		scanner = bufio.NewScanner(strings.NewReader(output))
		// For WASAPI, it usually lists devices directly
		// Example: [wasapi @ ...] "Microphone (Realtek Audio)"
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "]  \"") {
				parts := strings.SplitN(line, "] ", 2)
				if len(parts) == 2 {
					name := strings.TrimSpace(parts[1])
					// WASAPI devices often look like "Microphone (Realtek Audio)" or "Output (Realtek Audio)"
					// We only want input devices usually? WASAPI lists both.
					// But let's list all for now.
					result.WriteString(" - (WASAPI) " + name + "\n")
				}
			}
		}
	}

	if result.Len() == 0 {
		return output, nil // Fallback to raw output of last attempt
	}

	return result.String(), nil
}
