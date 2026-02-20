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
	"github.com/micha/cs-ingame-translate/setup"
	"github.com/micha/cs-ingame-translate/translator"
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
		fmt.Println(audio.GetDeviceHelpText())
		devices, err := audio.GetAvailableDevices()
		if err != nil {
			fmt.Printf("Error listing devices: %v\n", err)
		} else {
			fmt.Println("Available audio devices:")
			for i, device := range devices {
				fmt.Printf("  %d. %s\n", i+1, device)
			}
		}
		os.Exit(0)
	}

	scanner := bufio.NewScanner(os.Stdin)

	// Mode Selection
	fmt.Println("Select Mode:")
	fmt.Println("1. CS2 In-Game Translate (Monitor Console Log)")
	fmt.Println("2. Voice Command/Echo Mode (Record Output + F9 Trigger)")
	fmt.Print("Enter choice [1]: ")

	mode := "1"
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "2" {
			mode = "2"
		}
	}

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
		fmt.Print("Enable Voice Transcription (uses Docker by default)? [y/N]: ")
		if scanner.Scan() {
			input := strings.TrimSpace(scanner.Text())
			if strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" {
				*useVoice = true
			}
		}
	}

	// --- Environment Check & Setup ---
	if err := setup.EnsureEnvironment(scanner, *useVoice); err != nil {
		log.Fatalf("Setup failed: %v", err)
	}

	ctx := context.Background()
	tr, err := translator.NewOllamaTranslator(ctx, *ollamaModel, *targetLang)
	if err != nil {
		log.Fatalf("Error creating translator: %v", err)
	}
	defer tr.Close()

	fmt.Printf("Using Ollama model '%s' for translation to %s\n", *ollamaModel, *targetLang)

	// Initialize Audio Listener if enabled
	var audioListener *audio.Listener
	if *useVoice {
		tmpFile, err := os.CreateTemp("", "transcriber-*.py")
		if err != nil {
			log.Fatalf("Failed to create temp file for transcriber: %v", err)
		}
		defer os.Remove(tmpFile.Name())

		if _, err := tmpFile.Write(transcriberScript); err != nil {
			log.Fatalf("Failed to write transcriber script: %v", err)
		}
		if err := tmpFile.Close(); err != nil {
			log.Fatalf("Failed to close temp transcriber file: %v", err)
		}

		log.Println("Initializing Audio Transcription Engine...")
		audioListener, err = audio.NewListener(tmpFile.Name())
		if err != nil {
			log.Printf("Warning: Failed to create audio listener: %v", err)
		} else {
			defer audioListener.Stop()
		}
	}

	if isEchoMode {
		if audioListener == nil {
			log.Fatal("Echo mode requires working audio transcription. Please ensure dependencies are met.")
		}
		runEchoMode(ctx, tr, audioListener, *audioDevice, preRecCmd, preRecDir, preRecPath)
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

func runEchoMode(ctx context.Context, tr *translator.OllamaTranslator, listener *audio.Listener, device string, initialCmd *exec.Cmd, tmpDir string, initialPath string) {
	fmt.Println("\n=== Echo Mode Started ===")
	fmt.Println("Listening to system output audio...")
	fmt.Println("Press F9 to capture the last 15 seconds, transcribe, and translate.")
	fmt.Println("Press Ctrl+C to exit.")

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
		case <-hk.KeyPressed():
			fmt.Println("\n[F9] Capturing...")

			// Stop current recording gracefully to finalize WAV header
			if currentCmd != nil && currentCmd.Process != nil {
				currentCmd.Process.Signal(syscall.SIGTERM)
				done := make(chan error, 1)
				go func() { done <- currentCmd.Wait() }()
				select {
				case <-done:
				case <-time.After(1 * time.Second):
					currentCmd.Process.Kill()
				}
			}

			// Rename current to last
			lastRecPath := filepath.Join(tmpDir, fmt.Sprintf("rec_%d.wav", time.Now().UnixNano()))
			os.Rename(currentRecPath, lastRecPath)

			// Start new recording immediately
			var err error
			currentCmd, err = startAudioRecording(ctx, currentRecPath, device)
			if err != nil {
				log.Printf("Failed to restart recording: %v", err)
			}

			// Process last recording in background
			go func(inputPath string) {
				defer os.Remove(inputPath)

				// Slice last 15s
				slicePath := filepath.Join(tmpDir, fmt.Sprintf("slice_%d.wav", time.Now().UnixNano()))
				// ffmpeg -sseof -15 -i input -c copy output
				sliceCmd := exec.Command("ffmpeg", "-sseof", "-15", "-i", inputPath, "-y", slicePath)
				if out, err := sliceCmd.CombinedOutput(); err != nil {
					log.Printf("Slice failed: %v\n%s", err, string(out))
					return
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

func findLogFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get user home directory: %v", err)
	}
	var potentialPaths []string
	switch runtime.GOOS {
	case "windows":
		potentialPaths = []string{
			`C:\Program Files (x86)\Steam\steamapps\common\Counter-Strike Global Offensive\game\csgo\console.log`,
			`D:\SteamLibrary\steamapps\common\Counter-Strike Global Offensive\game\csgo\console.log`,
		}
	case "linux":
		potentialPaths = []string{
			filepath.Join(home, ".steam/steam/steamapps/common/Counter-Strike Global Offensive/game/csgo/console.log"),
			filepath.Join(home, ".local/share/Steam/steamapps/common/Counter-Strike Global Offensive/game/csgo/console.log"),
		}
	case "darwin":
		potentialPaths = []string{
			filepath.Join(home, "Library/Application Support/Steam/steamapps/common/Counter-Strike Global Offensive/game/csgo/console.log"),
		}
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
	for _, p := range potentialPaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("could not find console.log in common locations for %s", runtime.GOOS)
}

func checkCondebug(scanner *bufio.Scanner) error {
	// ... Simplified version of original checkCondebug ...
	// Since original was long and mostly heuristics, I'll copy the core logic
	// But to save space and tokens, I'll rely on the fact that the user can skip it.
	// Actually, let's copy the full logic to ensure feature parity.

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var dataPaths []string
	switch runtime.GOOS {
	case "windows":
		dataPaths = []string{`C:\Program Files (x86)\Steam\userdata`}
	case "linux":
		dataPaths = []string{
			filepath.Join(home, ".steam/steam/userdata"),
			filepath.Join(home, ".local/share/Steam/userdata"),
		}
	case "darwin":
		dataPaths = []string{filepath.Join(home, "Library/Application Support/Steam/userdata")}
	}

	foundConfig := false
	configured := false

	for _, dataPath := range dataPaths {
		entries, err := os.ReadDir(dataPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			configPath := filepath.Join(dataPath, entry.Name(), "config", "localconfig.vdf")
			contentBytes, err := os.ReadFile(configPath)
			if err != nil {
				continue
			}
			foundConfig = true
			if strings.Contains(string(contentBytes), "-condebug") {
				configured = true // naive check
				break
			}
		}
		if configured {
			break
		}
	}

	if !foundConfig {
		fmt.Println("Warning: Could not verify launch options.")
		return nil
	}

	if !configured {
		fmt.Println("CS2 launch option '-condebug' not detected.")
		fmt.Printf("Do you want to open Steam properties for CS2 to set it? [Y/n]: ")
		if scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text == "" || strings.ToLower(text) == "y" || strings.ToLower(text) == "yes" {
				return openSteamSettings()
			}
		}
	}
	return nil
}

func openSteamSettings() error {
	url := "steam://gameproperties/730"
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		return fmt.Errorf("unsupported OS")
	}
	return cmd.Start()
}

func stopDockerContainer() {
	cmd := exec.Command("docker", "stop", "cs-translate")
	cmd.Run()
}
