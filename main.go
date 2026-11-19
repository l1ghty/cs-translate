package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/micha/cs-ingame-translate/audio"
	"github.com/micha/cs-ingame-translate/hotkey"
	"github.com/micha/cs-ingame-translate/monitor"
	"github.com/micha/cs-ingame-translate/parser"
	"github.com/micha/cs-ingame-translate/setup"
	"github.com/micha/cs-ingame-translate/translator"
	"github.com/micha/cs-ingame-translate/ui"
	"github.com/nxadm/tail"
)

// captureSeconds is how much recent audio F9 transcribes in echo mode.
const captureSeconds = 15

func main() {
	logPath := flag.String("log", "", "Path to the CS2 console log file (default: auto-detect)")
	ollamaModel := flag.String("model", translator.DefaultOllamaModel, "Ollama model to use for translation")
	targetLang := flag.String("lang", "English", "Target language for translation")
	useVoice := flag.Bool("voice", false, "Enable live voice transcription")
	audioDevice := flag.String("audiodevice", "", "Audio device to capture (default: what you hear, i.e. the default output)")
	listDevices := flag.Bool("list-audio-devices", false, "List available audio devices and exit")
	whisperModel := flag.String("whisper-model", "auto", "Speech model: auto, tiny, base, small, medium, large-v3-turbo, distil-large-v3.5 (English only)")
	whisperDevice := flag.String("whisper-device", "cpu", "Run speech recognition on 'cpu' (recommended while gaming) or 'cuda' (NVIDIA GPU)")
	whisperThreads := flag.Int("whisper-threads", 0, "CPU threads for speech recognition (0 = auto, leaves most cores to the game)")
	voiceLang := flag.String("voice-lang", "", "Language(s) spoken in voice chat: one code (ru) or a list (ru,en,de) to keep detection within them (default: auto-detect)")
	voiceInterval := flag.Int("voice-interval", 10, "Live voice: transcribe after this many seconds of speech even if the speaker hasn't paused yet")
	translateCPU := flag.Bool("translate-cpu", false, "Run the translation model on the CPU only, keeping the GPU free for the game")
	setupOnly := flag.Bool("setup", false, "Install and download everything needed, then exit")
	assumeYes := flag.Bool("yes", false, "Answer 'yes' to all setup questions (unattended install)")
	flag.Parse()
	setup.AssumeYes = *assumeYes
	log.SetOutput(ui.Writer()) // log lines must not break the status line

	scanner := bufio.NewScanner(os.Stdin)
	speechCfg := audio.Config{
		Model:       *whisperModel,
		Device:      *whisperDevice,
		Threads:     *whisperThreads,
		Language:    *voiceLang,
		AudioDevice: *audioDevice,
		BufferSecs:  captureSeconds * 2,

		MaxSpeechSecs: max(*voiceInterval, 1),
	}

	if *listDevices {
		listAudioDevices(speechCfg)
		return
	}
	if *setupOnly {
		runSetup(scanner, *ollamaModel, speechCfg)
		return
	}

	mode := selectMode(scanner, speechCfg.MaxSpeechSecs)
	isEchoMode := mode == modeEcho
	needsVoice := mode != modeCS2 // modes 2 and 3 are about voice
	if needsVoice {
		*useVoice = true
	} else if !*useVoice {
		*useVoice = promptVoiceEnable(scanner)
	}

	env, err := setup.EnsureEnvironment(scanner, *ollamaModel, *useVoice)
	if err != nil {
		log.Fatalf("Setup failed: %v", err)
	}

	// From here on Ctrl+C shuts down cleanly (stops the speech worker, unloads the model).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	trOpts := translator.Options{CPUOnly: *translateCPU}
	if *translateCPU {
		trOpts.Threads = audio.AutoThreads(runtime.NumCPU())
	}
	tr, err := translator.NewOllamaTranslator(ctx, *ollamaModel, *targetLang, trOpts)
	if err != nil {
		log.Fatalf("Error creating translator: %v", err)
	}
	defer tr.Close()
	fmt.Printf("Translating to %s with Ollama model '%s'\n", *targetLang, *ollamaModel)

	if *useVoice {
		speechCfg.UV = env.UV
		speechCfg.DataDir = env.DataDir
		speechCfg.Live = !isEchoMode // echo mode only transcribes on F9: zero load while idle
	}
	worker, err := loadModels(ctx, tr, *ollamaModel, *useVoice, speechCfg)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		if needsVoice {
			log.Fatalf("Voice transcription unavailable: %v", err)
		}
		log.Printf("Warning: voice transcription disabled: %v", err)
	}
	if worker != nil {
		defer worker.Close()
	}
	go tr.KeepWarm(ctx) // stay loaded so translations are instant even after quiet periods

	s := newSession(ctx, tr, worker)
	switch mode {
	case modeEcho:
		runEchoMode(ctx, scanner, s, *logPath)
	case modeAuto:
		runAutoMode(ctx, s, *logPath, speechCfg.MaxSpeechSecs)
	default:
		runCS2Mode(ctx, scanner, s, *logPath)
	}
	s.busy.stop()
	fmt.Println("\nStopping...")
}

// loadModels loads the translation model and (if voice is on) the speech
// model in parallel and warms both up, so the first F9 / chat line / voice
// line is handled right away instead of waiting for models to load.
func loadModels(ctx context.Context, tr *translator.OllamaTranslator, model string, voice bool, cfg audio.Config) (*audio.Worker, error) {
	st := ui.Start("")
	defer st.Stop()

	trTask := st.Task("Translation model")
	trTask.Set("loading into memory")
	trDone := make(chan struct{})
	go func() {
		defer close(trDone)
		start := time.Now()
		if err := tr.Preload(ctx); err != nil {
			if ctx.Err() != nil {
				trTask.Done("")
				return
			}
			trTask.Done(fmt.Sprintf("⚠ Could not preload the translation model (%v); it will load on first use.", err))
			return
		}
		trTask.Done(fmt.Sprintf("✔ Translation model '%s' loaded (%s)", model, seconds(time.Since(start))))
	}()

	var worker *audio.Worker
	var err error
	if voice {
		cfg = cfg.Resolve()
		spTask := st.Task("Speech model")
		cfg.Progress = func(p audio.Progress) {
			spTask.Set(p.Message)
			spTask.Progress(p.Done, p.Total, p.Unit)
		}
		start := time.Now()
		worker, err = audio.Start(ctx, cfg)
		if err == nil {
			i := worker.Info
			spTask.Done(fmt.Sprintf("✔ Speech recognition ready: Whisper '%s' on %s (%s, %d thread(s)), loaded in %s\n  Listening to: %s",
				i.Model, i.Device, i.ComputeType, i.Threads, seconds(time.Since(start)), i.AudioDevice))
		} else {
			spTask.Done("")
		}
	}
	<-trDone
	return worker, err
}

func seconds(d time.Duration) string { return fmt.Sprintf("%.1fs", d.Seconds()) }

// openLogMonitor tails the CS2 console log. If wait is true it keeps looking
// until CS2 has created the file.
func openLogMonitor(ctx context.Context, logPath string, wait bool) *monitor.Monitor {
	path := logPath
	if path == "" {
		var st *ui.Status
		for {
			p, err := findLogFile()
			if err == nil {
				if st != nil {
					st.Stop()
				}
				path = p
				break
			}
			if !wait {
				fmt.Println("Warning: CS2 console.log not found, chat translation disabled (use -log to set it).")
				return nil
			}
			if st == nil {
				st = ui.Start("Waiting for CS2 to start (console.log not found yet)")
			}
			select {
			case <-ctx.Done():
				st.Stop()
				return nil
			case <-time.After(2 * time.Second):
			}
		}
	}
	fmt.Printf("Monitoring log file: %s\n", path)
	mon, err := monitor.NewMonitor(path)
	if err != nil {
		log.Printf("Error monitoring log file: %v", err)
		return nil
	}
	return mon
}

func runEchoMode(ctx context.Context, scanner *bufio.Scanner, s *session, logPath string) {
	fmt.Println("\n=== Echo Mode Started ===")
	fmt.Println("Listening to system audio + monitoring the CS2 console.")
	fmt.Printf("Press F9 (or Enter in this window) to transcribe and translate the last %d seconds.\n", captureSeconds)
	fmt.Println("Press Ctrl+C to exit.")

	var logLines chan *tail.Line
	if mon := openLogMonitor(ctx, logPath, false); mon != nil {
		defer mon.Stop()
		logLines = mon.Lines()
	}

	hk := hotkey.NewListener(hotkey.KeyF9)
	hkErr := make(chan error, 1)
	go func() {
		if err := hk.Start(ctx); err != nil && ctx.Err() == nil {
			hkErr <- err
		}
	}()

	// Enter in the terminal always works too (e.g. macOS, or Linux without 'input' group).
	enter := make(chan struct{}, 1)
	go func() {
		for scanner.Scan() {
			select {
			case enter <- struct{}{}:
			default:
			}
		}
	}()

	// This loop never waits for transcription or translation (they run in
	// the background), so F9 is always handled the moment it is pressed.
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-hkErr:
			ui.Printf("Global F9 hotkey unavailable (%v).\nPress Enter in this window to capture instead.\n", err)
		case line, ok := <-logLines:
			if !ok {
				logLines = nil
				continue
			}
			s.chatLine(line)
		case <-hk.KeyPressed():
			s.capture("F9")
		case <-enter:
			s.capture("Enter")
		case t, ok := <-s.worker.Transcripts():
			if !ok {
				log.Println("Speech worker stopped unexpectedly.")
				return
			}
			s.transcript(t)
		}
	}
}

// runAutoMode transcribes and translates voice on its own: the speech worker
// hands over each utterance as soon as the speaker pauses, or once it has
// gone on for the voice interval. CS2 chat is translated too, if found.
func runAutoMode(ctx context.Context, s *session, logPath string, interval int) {
	fmt.Println("\n=== Auto Voice Mode Started ===")
	fmt.Println("Listening to system audio + monitoring the CS2 console.")
	fmt.Printf("Voice is translated when a sentence ends, or after %d seconds of speech.\n", interval)
	fmt.Println("Press Ctrl+C to exit.")

	var logLines chan *tail.Line
	if mon := openLogMonitor(ctx, logPath, false); mon != nil {
		defer mon.Stop()
		logLines = mon.Lines()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-logLines:
			if !ok {
				logLines = nil
				continue
			}
			s.chatLine(line)
		case t, ok := <-s.worker.Transcripts():
			if !ok {
				log.Println("Speech worker stopped unexpectedly.")
				return
			}
			s.transcript(t)
		}
	}
}

func runCS2Mode(ctx context.Context, scanner *bufio.Scanner, s *session, logPath string) {
	if err := checkCondebug(scanner); err != nil {
		fmt.Printf("Warning: Could not verify launch options: %v\n", err)
	}

	mon := openLogMonitor(ctx, logPath, true)
	if mon == nil {
		return
	}
	defer mon.Stop()
	logLines := mon.Lines()

	var transcripts <-chan audio.Transcript
	if s.worker != nil {
		transcripts = s.worker.Transcripts()
	}

	fmt.Println("Waiting for chat messages...")
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-logLines:
			if !ok {
				return
			}
			s.chatLine(line)
		case t, ok := <-transcripts:
			if !ok {
				log.Println("Speech worker stopped; continuing with chat translation only.")
				transcripts = nil
				continue
			}
			s.transcript(t)
		}
	}
}

// session handles chat lines, F9 captures and transcripts. Translations run
// one at a time in a background goroutine so the event loop never blocks.
type session struct {
	ctx    context.Context
	tr     *translator.OllamaTranslator
	worker *audio.Worker
	vt     *voiceTranslator
	busy   *busy
	jobs   chan func()
}

func newSession(ctx context.Context, tr *translator.OllamaTranslator, worker *audio.Worker) *session {
	s := &session{
		ctx:    ctx,
		tr:     tr,
		worker: worker,
		vt:     newVoiceTranslator(tr),
		busy:   &busy{},
		jobs:   make(chan func(), 64),
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-s.jobs:
				job()
				s.busy.update(func(b *busy) { b.translating-- })
			}
		}
	}()
	return s
}

func (s *session) translate(job func()) {
	s.busy.update(func(b *busy) { b.translating++ })
	select {
	case s.jobs <- job:
	case <-s.ctx.Done():
	}
}

func (s *session) chatLine(line *tail.Line) {
	if line.Err != nil {
		return
	}
	msg := parser.ParseLine(line.Text)
	if msg == nil {
		return
	}
	s.translate(func() {
		translated, err := s.tr.Translate(s.ctx, msg.MessageContent)
		if err != nil {
			if s.ctx.Err() != nil {
				return
			}
			log.Printf("Translation error: %v", err)
			translated = "[Translation Pending/Error]"
		}
		outputChat(msg.PlayerName, translated, msg.IsDead, msg.OriginalText)
	})
}

func (s *session) capture(trigger string) {
	if err := s.worker.Capture(captureSeconds); err != nil {
		log.Printf("Capture failed: %v", err)
		return
	}
	ui.Printf("\n[%s] Captured the last %d seconds of audio\n", trigger, captureSeconds)
	s.busy.update(func(b *busy) { b.captures++ })
}

func (s *session) transcript(t audio.Transcript) {
	switch t.Kind {
	case audio.KindStart:
		s.busy.update(func(b *busy) {
			if t.Source == "capture" {
				b.clips = t.Pieces
			} else {
				b.live++
			}
		})
	case audio.KindDone:
		s.busy.update(func(b *busy) {
			if t.Source == "capture" {
				b.captures = max(b.captures-1, 0)
				b.clips = 0
			} else {
				b.live = max(b.live-1, 0)
			}
		})
		if t.Source == "capture" && !t.Found {
			ui.Println("No speech found in that audio.")
		}
	case audio.KindText:
		s.vt.printOriginal(t)
		s.translate(func() { s.vt.printTranslation(s.ctx, t) })
	}
}

// busy shows what is running in the background on the status line.
type busy struct {
	mu          sync.Mutex
	st          *ui.Status
	captures    int // F9 captures waiting for or in transcription
	clips       int // utterances in the capture being transcribed
	live        int // live utterances being transcribed
	translating int // translations queued or running
}

func (b *busy) update(f func(*busy)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	f(b)
	var parts []string
	if b.captures > 0 {
		p := fmt.Sprintf("Transcribing the last %ds of voice", captureSeconds)
		if b.clips > 1 {
			p += fmt.Sprintf(" (%d clips)", b.clips)
		}
		if b.captures > 1 {
			p += fmt.Sprintf(", %d more queued", b.captures-1)
		}
		parts = append(parts, p)
	}
	if b.live > 0 {
		parts = append(parts, "Transcribing voice")
	}
	if b.translating > 0 {
		p := "Translating"
		if b.translating > 1 {
			p += fmt.Sprintf(" (%d queued)", b.translating-1)
		}
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		if b.st != nil {
			b.st.Stop()
			b.st = nil
		}
		return
	}
	label := strings.Join(parts, " · ")
	if b.st == nil {
		b.st = ui.Start(label)
	} else {
		b.st.Set(label)
	}
}

func (b *busy) stop() {
	b.update(func(b *busy) { b.captures, b.clips, b.live, b.translating = 0, 0, 0, 0 })
}

func outputChat(name, text string, isDead bool, originalLine string) {
	var sb strings.Builder
	if originalLine != "" {
		sb.WriteString(originalLine + "\n")
	}
	prefix := ""
	if isDead {
		prefix = "*DEAD* "
	}
	fmt.Fprintf(&sb, "\033[1;32m%s%s : %s\033[0m\n", prefix, name, text)
	ui.Printf("%s", sb.String())
}
