package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func checkCondebug(scanner *bufio.Scanner) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	dataPaths := getUserdataPaths(home)
	if len(dataPaths) == 0 {
		return nil
	}

	foundConfig, configured := findCondebugInConfigs(dataPaths)

	if !foundConfig {
		fmt.Println("Warning: Could not verify launch options.")
		return nil
	}

	if !configured {
		fmt.Println("CS2 launch option '-condebug' not detected.")
		fmt.Printf("Do you want to open Steam properties for CS2 to set it? [Y/n]: ")
		if scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text == "" || strings.ToLower(text) == "y" || strings.ToLower(text) == "yes" {
				return openSteamSettings()
			}
		}
	}
	return nil
}

func findCondebugInConfigs(dataPaths []string) (bool, bool) {
	found := false
	for _, configPath := range getConfigFilePaths(dataPaths) {
		contentBytes, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}
		found = true
		if strings.Contains(string(contentBytes), "-condebug") {
			return true, true
		}
	}
	return found, false
}

func openSteamSettings() error {
	url := "steam://gameproperties/730"
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		return fmt.Errorf("unsupported OS")
	}
	return cmd.Start()
}
