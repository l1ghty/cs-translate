// Package setup installs and configures everything cs-translate needs:
// a native Ollama server plus translation model, and (for voice) uv, which
// in turn provides Python and the speech-recognition packages. No Docker,
// no system Python, no ffmpeg and no admin rights are required for voice.
package setup

import (
	"bufio"
	"fmt"

	"github.com/micha/cs-ingame-translate/translator"
)

// Environment is the result of a successful setup.
type Environment struct {
	UV      string // path to uv, empty when voice is disabled
	DataDir string // AppDir()
}

// EnsureEnvironment checks (and if needed installs) all dependencies.
func EnsureEnvironment(scanner *bufio.Scanner, ollamaModel string, useVoice bool) (*Environment, error) {
	dir, err := AppDir()
	if err != nil {
		return nil, fmt.Errorf("cannot create data directory: %w", err)
	}
	env := &Environment{DataDir: dir}

	if missing := missingComponents(ollamaModel, useVoice); len(missing) > 0 && !AssumeYes {
		fmt.Println("These components are missing and need to be installed:")
		for _, m := range missing {
			fmt.Println("  • " + m)
		}
		confirmAll = askYesNo(scanner, "Install all of them now, without asking again?")
		if !confirmAll {
			fmt.Println("OK, you will be asked before each step.")
		}
	}

	if err := SetupOllama(scanner, ollamaModel); err != nil {
		return nil, err
	}

	if useVoice {
		uv, err := EnsureUV()
		if err != nil {
			return nil, err
		}
		env.UV = uv
	}
	return env, nil
}

// missingComponents lists what setup will have to install, so the user can
// agree to all of it at once.
func missingComponents(ollamaModel string, useVoice bool) []string {
	var missing []string
	base := translator.OllamaHost
	_, err := ollamaVersion(base)
	running := err == nil
	if !running && findOllama() == "" {
		missing = append(missing, "Ollama (runs the translation model locally)")
	}
	// The model list can only be checked while Ollama runs; an installed but
	// stopped Ollama usually has the model already, so don't guess there.
	if (running && !hasModel(base, ollamaModel)) || (!running && findOllama() == "") {
		missing = append(missing, fmt.Sprintf("Translation model '%s' (one-time download, ~1-2 GB)", ollamaModel))
	}
	if useVoice && findUV() == "" {
		missing = append(missing, "Speech engine: uv (~20 MB), Python, faster-whisper, a Whisper model and small offline translation models (~75 MB per language)")
	}
	return missing
}
