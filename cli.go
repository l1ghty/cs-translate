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
	"github.com/micha/cs-ingame-translate/ui"
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
	st := ui.Start("Looking for audio devices")
	cfg.Progress = func(p audio.Progress) { st.Set(p.Message) }
	devices, def, err := audio.ListDevices(context.Background(), cfg)
	st.Stop()
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
	st := ui.Start(fmt.Sprintf("Installing speech engine and Whisper '%s' model", cfg.Model))
	cfg.Progress = func(p audio.Progress) {
		st.Set(p.Message)
		st.Progress(p.Done, p.Total, p.Unit)
	}
	info, err := audio.Prepare(context.Background(), cfg)
	st.Stop()
	if err != nil {
		log.Fatalf("Setup failed: %v", err)
	}
	fmt.Printf("✔ Speech recognition ready (%s on %s, %s)\n", info.Model, info.Device, info.ComputeType)
	fmt.Printf("✔ Setup complete. Data is stored in %s\n", env.DataDir)
}

// Modes offered at startup.
const (
	modeCS2  = "1"
	modeEcho = "2"
	modeAuto = "3"
)

func selectMode(scanner *bufio.Scanner, voiceInterval int) string {
	fmt.Println("Select Mode:")
	fmt.Println("1. CS2 In-Game Translate (console chat, optional live voice transcription)")
	fmt.Println("2. Echo: chat translation + press F9 to transcribe and translate the last 15 seconds")
	fmt.Println("   of voice (lightest option: speech recognition only runs when you press F9)")
	fmt.Println("3. Auto voice: chat translation + voice is transcribed and translated automatically,")
	fmt.Printf("   each time a sentence ends or after %d seconds of speech, whichever comes first\n", voiceInterval)
	fmt.Print("Enter choice [1]: ")

	if scanner.Scan() {
		switch choice := strings.TrimSpace(scanner.Text()); choice {
		case modeEcho, modeAuto:
			return choice
		}
	}
	return modeCS2
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

func (v *voiceTranslator) printOriginal(t audio.Transcript) {
	ui.Printf("\nVoice [%s, %.1fs audio, recognised in %.2fs]: %s\n", t.Language, t.AudioSeconds, t.ProcessingSeconds, t.Text)
}

// printTranslation translates t and prints the result (nothing if t is
// already in the target language).
func (v *voiceTranslator) printTranslation(ctx context.Context, t audio.Transcript) {
	start := time.Now()
	translated, didTranslate := v.translate(ctx, t)
	if didTranslate {
		ui.Printf("\033[1;32mTranslated (%.2fs): %s\033[0m\n", time.Since(start).Seconds(), translated)
	}
}
