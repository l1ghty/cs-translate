package setup

import (
	"bufio"
	_ "embed"
	"fmt"
	"os"
)

//go:embed Dockerfile
var dockerfileContent []byte

//go:embed transcriber.py
var transcriberScript []byte

func EnsureEnvironment(scanner *bufio.Scanner, useVoice bool) error {
	if err := SetupOllama(scanner); err != nil {
		return fmt.Errorf("failed to setup Ollama: %w", err)
	}

	if useVoice {
		if os.Getenv("USE_DOCKER_WHISPER") != "0" {
			fmt.Println("Using Docker for Whisper transcription (already running in unified container)")
			os.Setenv("USE_DOCKER_WHISPER", "1")
		} else {
			if err := SetupPythonEnv(scanner); err != nil {
				return fmt.Errorf("failed to setup python environment: %w", err)
			}
		}
	}

	return nil
}
