package main

import (
	"bufio"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/micha/cs-ingame-translate/audio"
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

	// --- Voice Transcription Setup ---
	// We use local Whisper, so no API key needed.
	// But we might want a flag to disable it?
	useVoice := flag.Bool("voice", false, "Enable voice transcription (local Whisper)")

	// If not provided in args, we can ask interactively or default to false
	if !*useVoice {
		// Check if user explicitly passed false? flag package defaults to false.
		// Let's ask user.
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

	// Check if -condebug is configured
	if err := checkCondebug(scanner); err != nil {
		fmt.Printf("Warning: Could not verify launch options: %v\n", err)
	} else {
		// If checkCondebug returned nil, it means we either found it or prompted the user
		// We can't really block here easily without user input, but checkCondebug logic will handle the "not found" case
	}

	// If not provided, try to find it automatically
	// If not provided, try to find it automatically
	path := *logPath
	if path == "" {
		fmt.Println("Auto-detecting log file location...")
		firstAttempt := true
		for {
			var err error
			path, err = findLogFile()
			if err == nil {
				if !firstAttempt {
					fmt.Println("") // New line after waiting message
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

	ctx := context.Background()
	tr, err := translator.NewOllamaTranslator(ctx, *ollamaModel, *targetLang)
	if err != nil {
		log.Fatalf("Error creating translator: %v", err)
	}
	defer tr.Close()

	fmt.Printf("Using Ollama model '%s' for translation to %s\n", *ollamaModel, *targetLang)

	// Start Audio Listener if enabled
	type transcriptionListener interface {
		Start(context.Context, string) error
		Stop()
		Transcriptions() <-chan string
	}
	var audioListener transcriptionListener
	if *useVoice {
		// Extract embedded transcriber script to temp file
		tmpFile, err := os.CreateTemp("", "transcriber-*.py")
		if err != nil {
			log.Fatalf("Failed to create temp file for transcriber: %v", err)
		}
		defer os.Remove(tmpFile.Name()) // Clean up on exit

		if _, err := tmpFile.Write(transcriberScript); err != nil {
			log.Fatalf("Failed to write transcriber script: %v", err)
		}
		if err := tmpFile.Close(); err != nil {
			log.Fatalf("Failed to close temp transcriber file: %v", err)
		}

		// Use FFmpeg-based audio listener
		log.Println("Initializing FFmpeg audio capture...")
		audioListener, err = audio.NewListener(tmpFile.Name())
		if err != nil {
			log.Printf("Warning: Failed to create FFmpeg audio listener: %v", err)
			log.Println("Make sure you have python3 installed and 'pip install openai-whisper'")
			log.Println("Also ensure ffmpeg is installed and available in your PATH")
		} else {
			if err := audioListener.Start(ctx, *audioDevice); err != nil {
				log.Printf("Warning: Failed to start FFmpeg audio capture: %v", err)
				audioListener.Stop()
				audioListener = nil
			}
		}

		if audioListener != nil {
			fmt.Printf("Local Audio transcription enabled (Whisper '%s' model).\n", translator.DefaultWhisperModel)
			defer audioListener.Stop()
		}
	}

	// Handle Ctrl+C
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	// We handle shutdown in the main loop to ensure cleanup
	go func() {
		<-c
	}()

	fmt.Println("Waiting for chat messages...")

	logLines := mon.Lines()
	var audioChan <-chan string
	if audioListener != nil {
		audioChan = audioListener.Transcriptions()
	}

	// Voice context buffer for last 10 seconds of transcriptions
	type voiceTranscription struct {
		text      string
		timestamp time.Time
	}
	var voiceContext []voiceTranscription

	// Main Event Loop
loop:
	for {
		select {
		case <-c:
			fmt.Println("\nStopping...")
			stopDockerContainer()
			break loop

		case line, ok := <-logLines:
			if !ok {
				log.Println("Log monitor closed unexpectedly")
				break loop
			}
			if line.Err != nil {
				log.Printf("Error reading line: %v", line.Err)
				continue
			}

			msg := parser.ParseLine(line.Text)
			if msg != nil {
				// Translate standard chat
				translated, err := tr.Translate(ctx, msg.MessageContent)
				if err != nil {
					log.Printf("Translation error: %v", err)
					translated = "[Translation Pending/Error]"
				}

				outputChat(msg.PlayerName, translated, msg.IsDead, msg.OriginalText)
			}

		case text, ok := <-audioChan:
			if !ok {
				audioChan = nil
				continue
			}

			// Parse transcription and timing (format: "text|duration")
			transcribeDuration := 0.0
			transcribedText := text
			if idx := strings.LastIndex(text, "|"); idx != -1 {
				if duration, err := fmt.Sscanf(text[idx+1:], "%f", &transcribeDuration); err == nil && duration == 1 {
					transcribedText = text[:idx]
				}
			}

			// Add to voice context buffer
			now := time.Now()
			voiceContext = append(voiceContext, voiceTranscription{text: transcribedText, timestamp: now})

			// Clean up old entries (older than 10 seconds)
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

			// Build context string from recent transcriptions (excluding current)
			var contextText strings.Builder
			for _, v := range voiceContext[:len(voiceContext)-1] {
				if contextText.Len() > 0 {
					contextText.WriteString("\n")
				}
				contextText.WriteString(v.text)
			}

			// Measure translation time
			translateStart := time.Now()

			// Translate with context if available
			var translated string
			var err error
			if contextText.Len() > 0 {
				translated, err = tr.TranslateWithContext(ctx, transcribedText, translator.VoiceContext{ContextText: contextText.String()})
			} else {
				translated, err = tr.Translate(ctx, transcribedText)
			}
			translateDuration := time.Since(translateStart)

			if err != nil {
				log.Printf("Translation error (voice): %v", err)
				translated = transcribedText // Fallback to original transcription
			}

			// Display with timing: "Voice: <text> [transcribe: X.XXs, translate: X.XXs]"
			fmt.Printf("Voice %.2fs: %s \n", transcribeDuration, transcribedText)
			outputChat(fmt.Sprintf("voice %.2fs: ", translateDuration.Seconds()), translated, false, "")
		}
	}
}

func outputChat(name, text string, isDead bool, originalLine string) {
	if originalLine != "" {
		fmt.Println(originalLine)
	}

	prefix := ""
	if isDead {
		prefix = "*DEAD* "
	}

	// Green color: \033[1;32m, Reset: \033[0m
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
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	var dataPaths []string
	switch runtime.GOOS {
	case "windows":
		dataPaths = []string{
			`C:\Program Files (x86)\Steam\userdata`,
		}
	case "linux":
		dataPaths = []string{
			filepath.Join(home, ".steam/steam/userdata"),
			filepath.Join(home, ".local/share/Steam/userdata"),
		}
	case "darwin":
		dataPaths = []string{
			filepath.Join(home, "Library/Application Support/Steam/userdata"),
		}
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
			userID := entry.Name()
			configPath := filepath.Join(dataPath, userID, "config", "localconfig.vdf")

			contentBytes, err := os.ReadFile(configPath)
			if err != nil {
				continue
			}
			foundConfig = true
			content := string(contentBytes)

			// Very naive check for "730" and "-condebug"
			// This is not a robust VDF parser, but it should work for standard cases
			// We look for "730" then subsequent "LaunchOptions" that contains "-condebug"
			// But since we can't easily parse nesting without a real parser, we will do a proximity check or simple index check

			// Strategy: Find "730" key. Then check if "LaunchOptions" appears before the next app key (which is hard to know) or closing brace.
			// Better: Just check if the file contains "-condebug" in a context that looks like it belongs to 730 is too hard with regex.
			// Let's assume if "-condebug" is present in the file, it's LIKELY for CS2 if checking 730 nearby is complex.
			// BUT, other games might use it? Unlikely to be common for other games to use same flag?
			// Actually -condebug is a Source engine flag. Dota 2 (570) or TF2 (440) might use it.

			// Refined check:
			// Find index of "730" as a section header (followed by opening brace)
			// Find index of "LaunchOptions" AFTER "730"
			// Check if "-condebug" is in the value of that LaunchOptions

			// Look for "730" followed by whitespace and opening brace to ensure it's a section header
			// This prevents matching "730" when it appears as a value elsewhere in the file
			idx730 := -1
			searchPattern := "\"730\""
			searchStart := 0
			for {
				idx := strings.Index(content[searchStart:], searchPattern)
				if idx == -1 {
					break
				}
				idx += searchStart

				// Check if this is followed by whitespace and opening brace (section header)
				// Skip past the closing quote
				afterQuote := idx + len(searchPattern)
				if afterQuote < len(content) {
					// Look ahead for opening brace, allowing whitespace/newlines
					remaining := content[afterQuote:]
					trimmed := strings.TrimSpace(remaining)
					if len(trimmed) > 0 && trimmed[0] == '{' {
						idx730 = idx
						break
					}
				}
				searchStart = idx + 1
			}
			if idx730 == -1 {
				continue
			}

			// Look for the LaunchOptions key strictly after "730"
			rest := content[idx730:]
			idxLaunch := strings.Index(rest, "\"LaunchOptions\"")

			// Need to verify "LaunchOptions" belongs to "730".
			// In VDF, keys are usually quoted. nested objects use braces.
			// We can't guarantee it without parsing.
			// However, localconfig.vdf usually sorts apps. "730" should be its own block.
			// Let's just check if "-condebug" exists in the file for now.
			// Simpler: if parsing fails, we might annoy the user.
			// But the request is explicit.

			// Let's try to find the "LaunchOptions" line within the "730" block.
			// We can limit the search window to say 2000 characters after "730" or until next entry?
			// This is risky.

			// Let's trust that if "730" is there, we want to see "-condebug" closely following it.

			if idxLaunch != -1 {
				// check value
				// "LaunchOptions"		"-condebug"
				// Extract the value
				startVal := idxLaunch + len("\"LaunchOptions\"")
				// Find first quote of value
				valStart := strings.Index(rest[startVal:], "\"")
				if valStart != -1 {
					valStart += startVal + 1
					valEnd := strings.Index(rest[valStart:], "\"")
					if valEnd != -1 {
						valEnd += valStart
						value := rest[valStart:valEnd]
						if strings.Contains(value, "-condebug") {
							configured = true
							break
						}
					}
				}
			}
		}
		if configured {
			break
		}
	}

	if !foundConfig {
		// Could not find any config file, can't verify.
		// We shouldn't block, maybe just warn log.
		fmt.Println("Warning: Could not locate Steam localconfig.vdf to verify launch options.")
		return nil
	}

	if !configured {
		fmt.Println("CS2 launch option '-condebug' not detected.")
		fmt.Printf("Do you want to open Steam properties for CS2 to set it? [Y/n]: ")

		if scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text == "" || strings.ToLower(text) == "y" || strings.ToLower(text) == "yes" {
				fmt.Println("Opening Steam properties...")
				return openSteamSettings()
			}
		}
		fmt.Println("Skipping Steam properties.")
	}

	return nil
}

func openSteamSettings() error {
	var cmd *exec.Cmd
	url := "steam://gameproperties/730"

	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		return fmt.Errorf("unsupported OS for opening steam link")
	}

	return cmd.Start()
}

func stopDockerContainer() {
	containerName := "cs-translate"
	cmd := exec.Command("docker", "stop", containerName)
	if err := cmd.Run(); err != nil {
		log.Printf("Warning: failed to stop docker container: %v", err)
	}
}
