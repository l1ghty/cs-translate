// Package setup installs and configures everything cs-translate needs:
// a native Ollama server plus translation model, and (for voice) uv, which
// in turn provides Python and the speech-recognition packages. No Docker,
// no system Python, no ffmpeg and no admin rights are required for voice.
package setup

import (
	"bufio"
	"fmt"
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
