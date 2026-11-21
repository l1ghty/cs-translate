package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/micha/cs-ingame-translate/audio"
	"github.com/micha/cs-ingame-translate/callouts"
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
	st := ui.Start(fmt.Sprintf("Installing speech engine, Whisper '%s' model and fast translation models", cfg.Model))
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
	if cfg.FastTranslate {
		fmt.Printf("✔ Fast translation models downloaded (%s)\n", strings.Join(cfg.FastLanguages, ", "))
	}
	fmt.Printf("✔ Setup complete. Data is stored in %s\n", env.DataDir)
}

// Modes offered at startup.
const (
	modeCS2  = "1"
	modeEcho = "2"
	modeAuto = "3"
)

// selectMode shows the start menu until a mode is picked. Option 0 changes
// the target language (saved for next time); the result is returned with it.
func selectMode(scanner *bufio.Scanner, voiceInterval int, lang string) (string, string) {
	for {
		fmt.Println("Select Mode:")
		fmt.Printf("0. Change target language (currently: %s)\n", lang)
		fmt.Println("1. CS2 In-Game Translate (console chat, optional live voice transcription)")
		fmt.Println("2. Echo: chat translation + press F9 to transcribe and translate the last 15 seconds")
		fmt.Println("   of voice (lightest option: speech recognition only runs when you press F9)")
		fmt.Println("3. Auto voice: chat translation + voice is transcribed and translated automatically,")
		fmt.Printf("   each time a sentence ends or after %d seconds of speech, whichever comes first\n", voiceInterval)
		fmt.Print("Enter choice [1]: ")

		if !scanner.Scan() {
			return modeCS2, lang
		}
		switch choice := strings.TrimSpace(scanner.Text()); choice {
		case "0":
			lang = selectLanguage(scanner, lang)
			fmt.Println()
		case modeEcho, modeAuto:
			return choice, lang
		default:
			return modeCS2, lang
		}
	}
}

// commonLanguages are the most used languages on Steam.
var commonLanguages = []string{"English", "Chinese", "Russian", "Spanish", "Portuguese"}

// selectLanguage asks for a target language and saves it for next time.
func selectLanguage(scanner *bufio.Scanner, current string) string {
	fmt.Println("\nTranslate into:")
	for i, l := range commonLanguages {
		fmt.Printf("%d. %s\n", i+1, l)
	}
	fmt.Printf("Enter a number or another language name [keep %s]: ", current)
	if !scanner.Scan() {
		return current
	}
	in := strings.TrimSpace(scanner.Text())
	lang := current
	if n, err := strconv.Atoi(in); err == nil {
		if n < 1 || n > len(commonLanguages) {
			fmt.Println("Unknown choice, keeping " + current)
			return current
		}
		lang = commonLanguages[n-1]
	} else if in != "" {
		r := []rune(in)
		lang = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	if err := saveTargetLanguage(lang); err != nil {
		log.Printf("Could not save the target language: %v", err)
	}
	fmt.Printf("✔ Target language: %s\n", lang)
	return lang
}

// The chosen target language is kept in the app's data folder, so it
// survives restarts. An explicit -lang flag always wins.
func targetLanguageFile() (string, error) {
	dir, err := setup.AppDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "target-language"), nil
}

func savedTargetLanguage() string {
	path, err := targetLanguageFile()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func saveTargetLanguage(lang string) error {
	path, err := targetLanguageFile()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(lang+"\n"), 0o644)
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
	worker     *audio.Worker // offline fast translation; may be nil
	targetCode string
	history    []voiceContextItem
}

func newVoiceTranslator(tr *translator.OllamaTranslator, worker *audio.Worker) *voiceTranslator {
	return &voiceTranslator{tr: tr, worker: worker, targetCode: translator.LanguageCode(tr.TargetLanguage())}
}

const voiceContextWindow = 10 * time.Second

// translate returns the translation of t, or the original text if it is
// already in the target language (no LLM call needed).
func (v *voiceTranslator) translate(ctx context.Context, t audio.Transcript) (string, bool) {
	text, _, translated := v.translateVia(ctx, t)
	return text, translated
}

// fastMaxWords: longer speech goes to the LLM, which handles it better than
// the small offline model; the fast path is for short callouts.
const fastMaxWords = 12

// translateVia is translate, also reporting which engine answered
// ("cache", "fast" or "" for the LLM / no translation).
func (v *voiceTranslator) translateVia(ctx context.Context, t audio.Transcript) (text, via string, translated bool) {
	now := time.Now()
	contextText := v.contextText(now)
	v.history = append(v.history, voiceContextItem{text: t.Text, timestamp: now})

	if v.targetCode != "" && t.Language == v.targetCode && t.LanguageProbability >= 0.5 {
		return t.Text, "", false
	}
	if v.targetCode == "en" {
		if fast, ok := callouts.Lookup(t.Language, t.Text); ok {
			return fast, "cache", true // common callout: no LLM call needed
		}
		if v.worker != nil && len(strings.Fields(t.Text)) <= fastMaxWords {
			if out, ok := v.worker.Translate(ctx, t.Language, t.Text); ok {
				return out, "fast", true
			}
		}
	}
	out, err := v.tr.TranslateWithContext(ctx, t.Text, translator.VoiceContext{ContextText: contextText, Voice: true})
	if err != nil {
		return t.Text, "", false
	}
	return out, "", true
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
	translated, via, didTranslate := v.translateVia(ctx, t)
	if didTranslate {
		tag := ""
		if via != "" {
			tag = ", " + via
		}
		ui.Printf("\033[1;32mTranslated (%.2fs%s): %s\033[0m\n", time.Since(start).Seconds(), tag, translated)
	}
}
