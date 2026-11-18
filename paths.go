package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var libraryPathRe = regexp.MustCompile(`"path"\s+"([^"]+)"`)

func findLogFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not get user home directory: %v", err)
	}
	for _, p := range getLogFilePaths(home) {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("could not find console.log in any Steam library on %s", runtime.GOOS)
}

// steamRoots returns the possible Steam installation directories.
func steamRoots(home string) []string {
	switch runtime.GOOS {
	case "windows":
		roots := []string{`C:\Program Files (x86)\Steam`, `C:\Program Files\Steam`}
		if pf := os.Getenv("ProgramFiles(x86)"); pf != "" {
			roots = append(roots, filepath.Join(pf, "Steam"))
		}
		return roots
	case "linux":
		return []string{
			filepath.Join(home, ".steam/steam"),
			filepath.Join(home, ".local/share/Steam"),
			filepath.Join(home, ".var/app/com.valvesoftware.Steam/.local/share/Steam"), // Flatpak
		}
	case "darwin":
		return []string{filepath.Join(home, "Library/Application Support/Steam")}
	}
	return nil
}

// steamLibraries returns every Steam library folder, including ones on other
// drives listed in libraryfolders.vdf.
func steamLibraries(home string) []string {
	seen := map[string]bool{}
	var libs []string
	add := func(p string) {
		key := filepath.Clean(p)
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			seen[key] = true
			libs = append(libs, p)
		}
	}
	for _, root := range steamRoots(home) {
		add(root)
		data, err := os.ReadFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
		if err != nil {
			continue
		}
		for _, m := range libraryPathRe.FindAllStringSubmatch(string(data), -1) {
			add(strings.ReplaceAll(m[1], `\\`, `\`))
		}
	}
	if runtime.GOOS == "windows" {
		add(`D:\SteamLibrary`)
	}
	return libs
}

func getLogFilePaths(home string) []string {
	var paths []string
	for _, lib := range steamLibraries(home) {
		paths = append(paths, filepath.Join(lib, "steamapps", "common", "Counter-Strike Global Offensive", "game", "csgo", "console.log"))
	}
	return paths
}

func getUserdataPaths(home string) []string {
	var paths []string
	for _, root := range steamRoots(home) {
		paths = append(paths, filepath.Join(root, "userdata"))
	}
	return paths
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
