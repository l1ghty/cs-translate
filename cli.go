package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/micha/cs-ingame-translate/audio"
	"github.com/micha/cs-ingame-translate/translator"
)

func listAudioDevices() {
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

func selectMode(scanner *bufio.Scanner) string {
	fmt.Println("Select Mode:")
	fmt.Println("1. CS2 In-Game Translate (Monitor Console Log)")
	fmt.Println("2. Additionally listening to system output audio " +
		"\nPress F9 to capture the last 15 seconds, transcribe, and translate.")
	fmt.Print("Enter choice [1]: ")

	mode := "1"
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "2" {
			mode = "2"
		}
	}
	return mode
}

func promptVoiceEnable(scanner *bufio.Scanner) bool {
	fmt.Print("Enable Voice Transcription (uses Docker by default)? [y/N]: ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" {
			return true
		}
	}
	return false
}

func initAudioListener(useVoice bool) *audio.Listener {
	if !useVoice {
		return nil
	}

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
	audioListener, err := audio.NewListener(tmpFile.Name())
	if err != nil {
		log.Printf("Warning: Failed to create audio listener: %v", err)
		return nil
	}
	return audioListener
}

type voiceContextItem struct {
	text      string
	timestamp time.Time
}

func parseTranscription(text string) (string, float64) {
	transcribeDuration := 0.0
	transcribedText := text
	if idx := strings.LastIndex(text, "|"); idx != -1 {
		if n, err := fmt.Sscanf(text[idx+1:], "%f", &transcribeDuration); err == nil && n == 1 {
			transcribedText = text[:idx]
		}
	}
	return transcribedText, transcribeDuration
}

func pruneOldContext(context []voiceContextItem, cutoff time.Time) []voiceContextItem {
	for i, v := range context {
		if v.timestamp.After(cutoff) {
			return context[i:]
		}
	}
	return context
}

func buildContextString(context []voiceContextItem) string {
	if len(context) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, v := range context[:len(context)-1] {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(v.text)
	}
	return sb.String()
}

func handleVoiceTranscription(ctx context.Context, tr *translator.OllamaTranslator, text string, voiceContext []voiceContextItem) (string, string, float64) {
	transcribedText, transcribeDuration := parseTranscription(text)

	now := time.Now()
	voiceContext = append(voiceContext, voiceContextItem{text: transcribedText, timestamp: now})

	cutoff := now.Add(-10 * time.Second)
	voiceContext = pruneOldContext(voiceContext, cutoff)

	contextText := buildContextString(voiceContext)

	translateStart := time.Now()
	var translated string
	var err error
	if len(contextText) > 0 {
		translated, err = tr.TranslateWithContext(ctx, transcribedText, translator.VoiceContext{ContextText: contextText})
	} else {
		translated, err = tr.Translate(ctx, transcribedText)
	}
	translateDuration := time.Since(translateStart)

	if err != nil {
		translated = transcribedText
	}

	return translated, fmt.Sprintf("voice %.2fs: ", translateDuration.Seconds()), transcribeDuration
}

func sliceAudioFile(inputPath, tmpDir string, listener *audio.Listener) {
	go func() {
		defer os.Remove(inputPath)

		slicePath := filepath.Join(tmpDir, fmt.Sprintf("slice_%d.wav", time.Now().UnixNano()))

		sliceCmd := exec.Command("ffmpeg", "-sseof", "-15", "-i", inputPath, "-c", "copy", "-y", slicePath)
		if out, err := sliceCmd.CombinedOutput(); err != nil {
			log.Printf("Quick slice failed, trying re-encode: %v", err)
			sliceCmd = exec.Command("ffmpeg", "-sseof", "-15", "-i", inputPath, "-c:a", "pcm_s16le", "-y", slicePath)
			if out2, err2 := sliceCmd.CombinedOutput(); err2 != nil {
				log.Printf("Slice failed: %v\n%s\n%s", err2, string(out), string(out2))
				return
			}
		}

		absPath, _ := filepath.Abs(slicePath)
		listener.SubmitFile(absPath)
	}()
}

func stopRecordingGracefully(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	if runtime.GOOS == "windows" {
		cmd.Process.Kill()
	} else {
		cmd.Process.Signal(syscall.SIGTERM)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		cmd.Process.Kill()
		<-done
	}
}

func renameWithRetry(from, to string) error {
	for i := 0; i < 10; i++ {
		if err := os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return os.Rename(from, to)
}
