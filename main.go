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
	"syscall"
	"time"

	"github.com/micha/cs-ingame-translate/audio"
	"github.com/micha/cs-ingame-translate/hotkey"
	"github.com/micha/cs-ingame-translate/monitor"
	"github.com/micha/cs-ingame-translate/parser"
	"github.com/micha/cs-ingame-translate/setup"
	"github.com/micha/cs-ingame-translate/translator"
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
	translateCPU := flag.Bool("translate-cpu", false, "Run the translation model on the CPU only, keeping the GPU free for the game")
	setupOnly := flag.Bool("setup", false, "Install and download everything needed, then exit")
	assumeYes := flag.Bool("yes", false, "Answer 'yes' to all setup questions (unattended install)")
	flag.Parse()
	setup.AssumeYes = *assumeYes

	scanner := bufio.NewScanner(os.Stdin)
	speechCfg := audio.Config{
		Model:       *whisperModel,
		Device:      *whisperDevice,
		Threads:     *whisperThreads,
		Language:    *voiceLang,
		AudioDevice: *audioDevice,
		BufferSecs:  captureSeconds * 2,
	}

	if *listDevices {
		listAudioDevices(speechCfg)
		return
	}
	if *setupOnly {
		runSetup(scanner, *ollamaModel, speechCfg)
		return
	}

	isEchoMode := selectMode(scanner) == "2"
	if isEchoMode {
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
	fmt.Printf("Using Ollama model '%s' for translation to %s\n", *ollamaModel, *targetLang)

	var worker *audio.Worker
	if *useVoice {
		speechCfg.UV = env.UV
		speechCfg.DataDir = env.DataDir
		speechCfg.Live = !isEchoMode // echo mode only transcribes on F9: zero load while idle
		worker, err = startSpeech(ctx, speechCfg)
		if err != nil {
			if isEchoMode || ctx.Err() != nil {
				log.Fatalf("Voice transcription unavailable: %v", err)
			}
			log.Printf("Warning: voice transcription disabled: %v", err)
		} else {
			defer worker.Close()
		}
	}

	if isEchoMode {
		runEchoMode(ctx, scanner, tr, worker, *logPath)
	} else {
		runCS2Mode(ctx, scanner, tr, worker, *logPath)
	}
	fmt.Println("\nStopping...")
}

func startSpeech(ctx context.Context, cfg audio.Config) (*audio.Worker, error) {
	cfg = cfg.Resolve()
	fmt.Printf("Starting speech recognition (Whisper '%s' on %s, %d thread(s))...\n", cfg.Model, cfg.Device, cfg.Threads)
	fmt.Println("The first start installs the speech engine and downloads the model; this takes a few minutes once.")
	w, err := audio.Start(ctx, cfg)
	if err != nil {
		return nil, err
	}
	fmt.Printf("✔ Speech recognition ready: %s (%s, %s), listening to: %s\n",
		w.Info.Model, w.Info.Device, w.Info.ComputeType, w.Info.AudioDevice)
	return w, nil
}

// openLogMonitor tails the CS2 console log. If wait is true it keeps looking
// until CS2 has created the file.
func openLogMonitor(ctx context.Context, logPath string, wait bool) *monitor.Monitor {
	path := logPath
	if path == "" {
		fmt.Println("Auto-detecting CS2 console log...")
		waiting := false
		for {
			p, err := findLogFile()
			if err == nil {
				if waiting {
					fmt.Println()
				}
				path = p
				break
			}
			if !wait {
				fmt.Println("Warning: CS2 console.log not found, chat translation disabled (use -log to set it).")
				return nil
			}
			if !waiting {
				fmt.Println("Log file not found yet. Waiting for CS2 to start...")
				waiting = true
			}
			fmt.Print(".")
			select {
			case <-ctx.Done():
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

func handleChatLine(ctx context.Context, tr *translator.OllamaTranslator, line *tail.Line) {
	if line.Err != nil {
		return
	}
	msg := parser.ParseLine(line.Text)
	if msg == nil {
		return
	}
	translated, err := tr.Translate(ctx, msg.MessageContent)
	if err != nil {
		log.Printf("Translation error: %v", err)
		translated = "[Translation Pending/Error]"
	}
	outputChat(msg.PlayerName, translated, msg.IsDead, msg.OriginalText)
}

func runEchoMode(ctx context.Context, scanner *bufio.Scanner, tr *translator.OllamaTranslator, worker *audio.Worker, logPath string) {
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

	vt := newVoiceTranslator(tr)
	capture := func(trigger string) {
		fmt.Printf("\n[%s] Transcribing the last %d seconds...\n", trigger, captureSeconds)
		if err := worker.Capture(captureSeconds); err != nil {
			log.Printf("Capture failed: %v", err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case err := <-hkErr:
			fmt.Printf("Global F9 hotkey unavailable (%v).\nPress Enter in this window to capture instead.\n", err)
		case line, ok := <-logLines:
			if !ok {
				logLines = nil
				continue
			}
			handleChatLine(ctx, tr, line)
		case <-hk.KeyPressed():
			capture("F9")
		case <-enter:
			capture("Enter")
		case t, ok := <-worker.Transcripts():
			if !ok {
				log.Println("Speech worker stopped unexpectedly.")
				return
			}
			if t.Empty {
				fmt.Println("No speech found in that audio.")
				continue
			}
			vt.print(ctx, t)
		}
	}
}

func runCS2Mode(ctx context.Context, scanner *bufio.Scanner, tr *translator.OllamaTranslator, worker *audio.Worker, logPath string) {
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
	if worker != nil {
		transcripts = worker.Transcripts()
	}
	vt := newVoiceTranslator(tr)

	fmt.Println("Waiting for chat messages...")
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-logLines:
			if !ok {
				return
			}
			handleChatLine(ctx, tr, line)
		case t, ok := <-transcripts:
			if !ok {
				log.Println("Speech worker stopped; continuing with chat translation only.")
				transcripts = nil
				continue
			}
			vt.print(ctx, t)
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
	fmt.Printf("\033[1;32m%s%s : %s\033[0m\n", prefix, name, text)
}
