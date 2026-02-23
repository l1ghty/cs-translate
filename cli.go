package main

import (
	"bufio"
	"fmt"
	"log"
	"os"

	"github.com/micha/cs-ingame-translate/audio"
	"strings"
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
