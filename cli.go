package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/micha/cs-ingame-translate/audio"
	"github.com/micha/cs-ingame-translate/setup"
	"github.com/micha/cs-ingame-translate/translator"
)

func listAudioDevices(cfg audio.Config) {
	uv, err := setup.EnsureUV()
	if err != nil {
		log.Fatal(err)
	}
	dir, err := setup.AppDir()
	if err != nil {
		log.Fatal(err)
	}
	cfg.UV, cfg.DataDir = uv, dir
	devices, def, err := audio.ListDevices(context.Background(), cfg)
	if err != nil {
		log.Fatalf("Error listing devices: %v", err)
	}
	fmt.Println("Audio devices (pass a name, or part of it, to -audiodevice):")
	for _, d := range devices {
		kind := "input   "
		if d.Loopback {
			kind = "loopback"
		}
		fmt.Printf("  [%s] %s\n", kind, d.Name)
	}
	if def != "" {
		fmt.Printf("Default: loopback of '%s' (what you hear)\n", def)
	}
}

// runSetup installs and downloads everything up front (-setup).
func runSetup(scanner *bufio.Scanner, ollamaModel string, cfg audio.Config) {
	env, err := setup.EnsureEnvironment(scanner, ollamaModel, true)
	if err != nil {
		log.Fatalf("Setup failed: %v", err)
	}
	cfg.UV, cfg.DataDir = env.UV, env.DataDir
	cfg = cfg.Resolve()
	fmt.Printf("Installing speech engine and downloading Whisper '%s' model...\n", cfg.Model)
	info, err := audio.Prepare(context.Background(), cfg)
	if err != nil {
		log.Fatalf("Setup failed: %v", err)
	}
	fmt.Printf("✔ Speech recognition ready (%s on %s, %s)\n", info.Model, info.Device, info.ComputeType)
	fmt.Printf("✔ Setup complete. Data is stored in %s\n", env.DataDir)
}

func selectMode(scanner *bufio.Scanner) string {
	fmt.Println("Select Mode:")
	fmt.Println("1. CS2 In-Game Translate (console chat, optional live voice transcription)")
	fmt.Println("2. Echo: chat translation + press F9 to transcribe and translate the last 15 seconds")
	fmt.Println("   of voice (lightest option: speech recognition only runs when you press F9)")
	fmt.Print("Enter choice [1]: ")

	if scanner.Scan() && strings.TrimSpace(scanner.Text()) == "2" {
		return "2"
	}
	return "1"
}

func promptVoiceEnable(scanner *bufio.Scanner) bool {
	fmt.Print("Enable live voice transcription (runs locally on the CPU)? [y/N]: ")
	if scanner.Scan() {
		input := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return input == "y" || input == "yes"
	}
	return false
}

type voiceContextItem struct {
	text      string
	timestamp time.Time
}

// voiceTranslator translates speech, using the last few seconds of speech as context.
type voiceTranslator struct {
	tr         *translator.OllamaTranslator
	targetCode string
	history    []voiceContextItem
}

func newVoiceTranslator(tr *translator.OllamaTranslator) *voiceTranslator {
	return &voiceTranslator{tr: tr, targetCode: translator.LanguageCode(tr.TargetLanguage())}
}

const voiceContextWindow = 10 * time.Second

// translate returns the translation of t, or the original text if it is
// already in the target language (no LLM call needed).
func (v *voiceTranslator) translate(ctx context.Context, t audio.Transcript) (string, bool) {
	now := time.Now()
	contextText := v.contextText(now)
	v.history = append(v.history, voiceContextItem{text: t.Text, timestamp: now})

	if v.targetCode != "" && t.Language == v.targetCode && t.LanguageProbability >= 0.5 {
		return t.Text, false
	}
	translated, err := v.tr.TranslateWithContext(ctx, t.Text, translator.VoiceContext{ContextText: contextText})
	if err != nil {
		return t.Text, false
	}
	return translated, true
}

// contextText drops history older than the context window and joins the rest.
func (v *voiceTranslator) contextText(now time.Time) string {
	cutoff := now.Add(-voiceContextWindow)
	i := 0
	for i < len(v.history) && v.history[i].timestamp.Before(cutoff) {
		i++
	}
	v.history = v.history[i:]
	parts := make([]string, len(v.history))
	for j, item := range v.history {
		parts[j] = item.text
	}
	return strings.Join(parts, "\n")
}

func (v *voiceTranslator) print(ctx context.Context, t audio.Transcript) {
	fmt.Printf("\nVoice [%s, %.1fs audio, recognised in %.2fs]: %s\n", t.Language, t.AudioSeconds, t.ProcessingSeconds, t.Text)
	start := time.Now()
	translated, didTranslate := v.translate(ctx, t)
	if didTranslate {
		fmt.Printf("\033[1;32mTranslated (%.2fs): %s\033[0m\n", time.Since(start).Seconds(), translated)
	}
}
