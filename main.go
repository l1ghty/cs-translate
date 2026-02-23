package main

import (
	"bufio"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/micha/cs-ingame-translate/audio"
	"github.com/micha/cs-ingame-translate/hotkey"
	"github.com/micha/cs-ingame-translate/monitor"
	"github.com/micha/cs-ingame-translate/parser"
	"github.com/micha/cs-ingame-translate/translator"
	"github.com/nxadm/tail"
)

//go:embed transcriber.py
var transcriberScript []byte

func main() {
	logPath := flag.String("log", "", "Path to the CS2 console log file")
	ollamaModel := flag.String("model", translator.DefaultOllamaModel, "Ollama model to use for translation")
	targetLang := flag.String("lang", "English", "Target language for translation")
	audioDevice := flag.String("audiodevice", "", "Audio device to monitor (default: auto-detect)")
	listDevices := flag.Bool("list-audio-devices", false, "List available audio devices and exit")
	useVoice := flag.Bool("voice", false, "Enable voice transcription (local Whisper)")

	flag.Parse()

	// List audio devices if requested
	if *listDevices {
		listAudioDevices()
	}

	scanner := bufio.NewScanner(os.Stdin)

	mode := selectMode(scanner)
	isEchoMode := mode == "2"

	var preRecCmd *exec.Cmd
	var preRecDir string
	var preRecPath string

	// Voice setup logic
	if isEchoMode {
		*useVoice = true
		// Start recording immediately
		var err error
		preRecDir, err = os.MkdirTemp("", "cs-echo-rec")
		if err != nil {
			log.Fatalf("Failed to create temp dir: %v", err)
		}
		preRecPath = filepath.Join(preRecDir, "current.wav")

		// Context for recording (separate from main ctx which might be cancelled?)
		// Actually use background context for now
		preRecCmd, err = startAudioRecording(context.Background(), preRecPath, *audioDevice)
		if err != nil {
			log.Printf("Warning: Failed to start early recording: %v", err)
		} else {
			fmt.Println("Background recording started.")
		}
	} else if !*useVoice {
		*useVoice = promptVoiceEnable(scanner)
	}

	// --- Environment Check & Setup ---
	if err := ensureEnvironment(scanner, *useVoice); err != nil {
		log.Fatalf("Setup failed: %v", err)
	}

	ctx := context.Background()
	tr, err := translator.NewOllamaTranslator(ctx, *ollamaModel, *targetLang)
	if err != nil {
		log.Fatalf("Error creating translator: %v", err)
	}
	defer tr.Close()

	fmt.Printf("Using Ollama model '%s' for translation to %s\n", *ollamaModel, *targetLang)

	audioListener := initAudioListener(*useVoice)
	if audioListener != nil {
		defer audioListener.Stop()
	}

	if isEchoMode {
		if audioListener == nil {
			log.Fatal("Echo mode requires working audio transcription. Please ensure dependencies are met.")
		}
		runEchoMode(ctx, scanner, tr, audioListener, *logPath, *audioDevice, preRecCmd, preRecDir, preRecPath)
	} else {
		// Clean up pre-recording if it happened (shouldn't happen here but safe)
		if preRecCmd != nil && preRecCmd.Process != nil {
			preRecCmd.Process.Kill()
		}
		if preRecDir != "" {
			os.RemoveAll(preRecDir)
		}
		runCS2Mode(ctx, scanner, tr, audioListener, *logPath, *audioDevice, *useVoice)
	}
}

func startAudioRecording(ctx context.Context, path, device string) (*exec.Cmd, error) {
	source := device
	if source == "" || source == "default" {
		if runtime.GOOS == "linux" {
			source = audio.GetDefaultMonitorSource()
		} else {
			// Windows fallback (simplified)
			source = "virtual-audio-capturer"
		}
	}

	args := []string{}
	if runtime.GOOS == "linux" {
		args = []string{"-f", "pulse", "-i", source}
	} else {
		args = []string{"-f", "dshow", "-i", "audio=" + source}
	}

	// Add output format
	args = append(args, "-c:a", "pcm_s16le", "-ar", "16000", "-ac", "1", "-y", path)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	// Suppress stderr to avoid spam, but keep it for debugging if needed
	// cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start ffmpeg: %w", err)
	}
	return cmd, nil
}

func runEchoMode(ctx context.Context, scanner *bufio.Scanner, tr *translator.OllamaTranslator, listener *audio.Listener, logPath string, device string, initialCmd *exec.Cmd, tmpDir string, initialPath string) {
	fmt.Println("\n=== Echo Mode Started ===")
	fmt.Println("Listening to system output audio + Monitoring CS2 Console...")
	fmt.Println("Press F9 to capture the last 15 seconds, transcribe, and translate.")
	fmt.Println("Press Ctrl+C to exit.")

	// --- Console Monitor Setup ---
	// Find log file
	path := logPath
	if path == "" {
		fmt.Println("Auto-detecting log file location...")
		path, _ = findLogFile() // Ignore error, just try once silently or use empty
		if path != "" {
			fmt.Printf("Found log file: %s\n", path)
		} else {
			fmt.Println("Warning: Could not auto-detect log file. Console translation disabled until restart with -log flag.")
		}
	}

	var mon *monitor.Monitor
	var logLines chan *tail.Line
	if path != "" {
		fmt.Printf("Monitoring log file: %s\n", path)
		var err error
		mon, err = monitor.NewMonitor(path)
		if err != nil {
			log.Printf("Error creating monitor: %v", err)
		} else {
			// defer mon.Stop() // Cannot defer in loop/long running function easily if not careful, but okay here as we return on exit
			// Actually we should handle stop manually on exit
			logLines = mon.Lines()
		}
	}
	// -----------------------------

	if tmpDir == "" {
		// Fallback if pre-recording failed or didn't run
		var err error
		tmpDir, err = os.MkdirTemp("", "cs-echo-rec")
		if err != nil {
			log.Fatalf("Failed to create temp dir: %v", err)
		}
	}
	defer os.RemoveAll(tmpDir)

	currentRecPath := initialPath
	if currentRecPath == "" {
		currentRecPath = filepath.Join(tmpDir, "current.wav")
	}

	currentCmd := initialCmd
	if currentCmd == nil {
		var err error
		currentCmd, err = startAudioRecording(ctx, currentRecPath, device)
		if err != nil {
			log.Printf("Failed to start recording: %v", err)
		}
	}

	defer func() {
		if currentCmd != nil && currentCmd.Process != nil {
			currentCmd.Process.Kill()
		}
	}()

	// Hotkey Listener
	hk := hotkey.NewListener(hotkey.KeyF9)
	hkErr := make(chan error, 1)
	go func() {
		if err := hk.Start(ctx); err != nil {
			hkErr <- err
		}
	}()

	transcriptions := listener.Transcriptions()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-interrupt:
			fmt.Println("\nStopping...")
			return
		case err := <-hkErr:
			log.Printf("Hotkey error: %v", err)
			return

		// Console Monitor Case
		case line, ok := <-logLines:
			if !ok {
				logLines = nil // Stop listening if closed
				continue
			}
			if line.Err != nil {
				continue
			}
			msg := parser.ParseLine(line.Text)
			if msg != nil {
				translated, err := tr.Translate(ctx, msg.MessageContent)
				if err != nil {
					translated = "[Translation Pending/Error]"
				}
				outputChat(msg.PlayerName, translated, msg.IsDead, msg.OriginalText)
			}

		case <-hk.KeyPressed():
			fmt.Println("\n[F9] Capturing...")

			// Stop current recording gracefully to finalize WAV header
			if currentCmd != nil && currentCmd.Process != nil {
				// Try to stop gracefully
				if runtime.GOOS == "windows" {
					// Windows doesn't support graceful SIGTERM to subprocess easily
					// Try sending Ctrl+Break event if possible, or just Kill.
					// For now, we'll try Kill immediately as it's the most reliable way to release the file handle
					currentCmd.Process.Kill()
				} else {
					currentCmd.Process.Signal(syscall.SIGTERM)
				}

				done := make(chan error, 1)
				go func() { done <- currentCmd.Wait() }()
				select {
				case <-done:
				case <-time.After(500 * time.Millisecond):
					currentCmd.Process.Kill()
					<-done // Wait for release
				}
			}

			// Check if file exists before renaming
			if _, err := os.Stat(currentRecPath); os.IsNotExist(err) {
				log.Printf("Recording file not found: %s (Audio capture might have failed to start)", currentRecPath)
				// Restart recording to try again
				var err error
				currentCmd, err = startAudioRecording(ctx, currentRecPath, device)
				if err != nil {
					log.Printf("Failed to restart recording: %v", err)
				}
				continue
			}

			// Rename current to last
			lastRecPath := filepath.Join(tmpDir, fmt.Sprintf("rec_%d.wav", time.Now().UnixNano()))

			// Retry rename loop for Windows locking issues
			var renameErr error
			for i := 0; i < 10; i++ {
				renameErr = os.Rename(currentRecPath, lastRecPath)
				if renameErr == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}

			if renameErr != nil {
				log.Printf("Failed to rename recording file: %v", renameErr)
				// Clean up and restart
				os.Remove(currentRecPath)
				var err error
				currentCmd, err = startAudioRecording(ctx, currentRecPath, device)
				if err != nil {
					log.Printf("Failed to restart recording: %v", err)
				}
				continue
			}

			// Start new recording immediately
			var err error
			currentCmd, err = startAudioRecording(ctx, currentRecPath, device)
			if err != nil {
				log.Printf("Failed to restart recording: %v", err)
			} else {
				// On Windows, monitor if ffmpeg stays alive for a second
				go func() {
					time.Sleep(1 * time.Second)
					if currentCmd != nil && currentCmd.Process != nil {
						// Check if process still running
						// os.FindProcess(pid) always succeeds on Unix, on Windows it finds the handle.
						// A better check is to see if Wait() returns quickly, but we can't call Wait twice.
						// We'll trust the user sees "ffmpeg exited" errors if it fails.
					}
				}()
			}

			// Process last recording in background
			go func(inputPath string) {
				defer os.Remove(inputPath)

				// Slice last 15s
				slicePath := filepath.Join(tmpDir, fmt.Sprintf("slice_%d.wav", time.Now().UnixNano()))

				// ffmpeg command to slice
				// On Windows, if the file header is corrupt due to Kill, we might need to be lenient
				sliceCmd := exec.Command("ffmpeg", "-sseof", "-15", "-i", inputPath, "-c", "copy", "-y", slicePath)
				if out, err := sliceCmd.CombinedOutput(); err != nil {
					// Fallback: try re-encoding if copy fails (e.g. corrupt header)
					log.Printf("Quick slice failed, trying re-encode: %v", err)
					sliceCmd = exec.Command("ffmpeg", "-sseof", "-15", "-i", inputPath, "-c:a", "pcm_s16le", "-y", slicePath)
					if out2, err2 := sliceCmd.CombinedOutput(); err2 != nil {
						log.Printf("Slice failed: %v\n%s\n%s", err2, string(out), string(out2))
						return
					}
				}

				// Submit to transcriber
				absPath, _ := filepath.Abs(slicePath)
				listener.SubmitFile(absPath)
			}(lastRecPath)

		case text := <-transcriptions:
			parts := strings.Split(text, "|")
			content := parts[0]
			fmt.Printf("\nOriginal: %s\n", content)

			translated, err := tr.Translate(ctx, content)
			if err != nil {
				log.Printf("Translation error: %v", err)
				continue
			}
			// Color output
			fmt.Printf("\033[1;32mTranslated: %s\033[0m\n", translated)
		}
	}
}

func runCS2Mode(ctx context.Context, scanner *bufio.Scanner, tr *translator.OllamaTranslator, audioListener *audio.Listener, logPath string, audioDevice string, useVoice bool) {
	// Check if -condebug is configured
	if err := checkCondebug(scanner); err != nil {
		fmt.Printf("Warning: Could not verify launch options: %v\n", err)
	}

	// Find log file
	path := logPath
	if path == "" {
		fmt.Println("Auto-detecting log file location...")
		firstAttempt := true
		for {
			var err error
			path, err = findLogFile()
			if err == nil {
				if !firstAttempt {
					fmt.Println("")
				}
				fmt.Printf("Found log file: %s\n", path)
				break
			}
			if firstAttempt {
				fmt.Println("Log file not found yet. Waiting for CS2 to start...")
				firstAttempt = false
			}
			fmt.Print(".")
			time.Sleep(2 * time.Second)
		}
	}

	fmt.Printf("Monitoring log file: %s\n", path)

	mon, err := monitor.NewMonitor(path)
	if err != nil {
		log.Fatalf("Error creating monitor: %v", err)
	}
	defer mon.Stop()

	if useVoice && audioListener != nil {
		if err := audioListener.Start(ctx, audioDevice); err != nil {
			log.Printf("Warning: Failed to start audio capture: %v", err)
		} else {
			fmt.Printf("Local Audio transcription enabled (Whisper '%s' model).\n", translator.DefaultWhisperModel)
		}
	}

	// Handle Ctrl+C
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	logLines := mon.Lines()
	var audioChan <-chan string
	if audioListener != nil {
		audioChan = audioListener.Transcriptions()
	}

	// Voice context buffer logic (simplified from original main)
	type voiceTranscription struct {
		text      string
		timestamp time.Time
	}
	var voiceContext []voiceTranscription

	fmt.Println("Waiting for chat messages...")

loop:
	for {
		select {
		case <-c:
			fmt.Println("\nStopping...")
			stopDockerContainer()
			break loop

		case line, ok := <-logLines:
			if !ok {
				break loop
			}
			if line.Err != nil {
				continue
			}
			msg := parser.ParseLine(line.Text)
			if msg != nil {
				translated, err := tr.Translate(ctx, msg.MessageContent)
				if err != nil {
					translated = "[Translation Pending/Error]"
				}
				outputChat(msg.PlayerName, translated, msg.IsDead, msg.OriginalText)
			}

		case text, ok := <-audioChan:
			if !ok {
				audioChan = nil
				continue
			}

			// Parse transcription
			transcribeDuration := 0.0
			transcribedText := text
			if idx := strings.LastIndex(text, "|"); idx != -1 {
				if n, err := fmt.Sscanf(text[idx+1:], "%f", &transcribeDuration); err == nil && n == 1 {
					transcribedText = text[:idx]
				}
			}

			// Add to context
			now := time.Now()
			voiceContext = append(voiceContext, voiceTranscription{text: transcribedText, timestamp: now})

			// Prune old context
			cutoff := now.Add(-10 * time.Second)
			validIdx := 0
			for i, v := range voiceContext {
				if v.timestamp.After(cutoff) {
					validIdx = i
					break
				}
			}
			if validIdx > 0 {
				voiceContext = voiceContext[validIdx:]
			}

			// Build context string
			var contextText strings.Builder
			for _, v := range voiceContext[:len(voiceContext)-1] {
				if contextText.Len() > 0 {
					contextText.WriteString("\n")
				}
				contextText.WriteString(v.text)
			}

			// Translate
			translateStart := time.Now()
			var translated string
			var err error
			if contextText.Len() > 0 {
				translated, err = tr.TranslateWithContext(ctx, transcribedText, translator.VoiceContext{ContextText: contextText.String()})
			} else {
				translated, err = tr.Translate(ctx, transcribedText)
			}
			translateDuration := time.Since(translateStart)

			if err != nil {
				translated = transcribedText
			}

			fmt.Printf("Voice %.2fs: %s \n", transcribeDuration, transcribedText)
			outputChat(fmt.Sprintf("voice %.2fs: ", translateDuration.Seconds()), translated, false, "")
		}
	}
}

// ... Helper functions (copied from original) ...

func outputChat(name, text string, isDead bool, originalLine string) {
	if originalLine != "" {
		fmt.Println(originalLine)
	}
	prefix := ""
	if isDead {
		prefix = "*DEAD* "
	}
	fmt.Printf("\033[1;32m%s%s : %s\033[0m\n", prefix, name, text)
}
