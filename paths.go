package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func findLogFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get user home directory: %v", err)
	}

	potentialPaths := getLogFilePaths(home)
	for _, p := range potentialPaths {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("could not find console.log in common locations for %s", runtime.GOOS)
}

func getLogFilePaths(home string) []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			`C:\Program Files (x86)\Steam\steamapps\common\Counter-Strike Global Offensive\game\csgo\console.log`,
			`D:\SteamLibrary\steamapps\common\Counter-Strike Global Offensive\game\csgo\console.log`,
		}
	case "linux":
		return []string{
			filepath.Join(home, ".steam/steam/steamapps/common/Counter-Strike Global Offensive/game/csgo/console.log"),
			filepath.Join(home, ".local/share/Steam/steamapps/common/Counter-Strike Global Offensive/game/csgo/console.log"),
		}
	case "darwin":
		return []string{
			filepath.Join(home, "Library/Application Support/Steam/steamapps/common/Counter-Strike Global Offensive/game/csgo/console.log"),
		}
	}
	return nil
}

func getUserdataPaths(home string) []string {
	switch runtime.GOOS {
	case "windows":
		return []string{`C:\Program Files (x86)\Steam\userdata`}
	case "linux":
		return []string{
			filepath.Join(home, ".steam/steam/userdata"),
			filepath.Join(home, ".local/share/Steam/userdata"),
		}
	case "darwin":
		return []string{filepath.Join(home, "Library/Application Support/Steam/userdata")}
	}
	return nil
}

func getConfigFilePaths(dataPaths []string) []string {
	var configs []string
	for _, dataPath := range dataPaths {
		entries, err := os.ReadDir(dataPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				configs = append(configs, filepath.Join(dataPath, entry.Name(), "config", "localconfig.vdf"))
			}
		}
	}
	return configs
}
