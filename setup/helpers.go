package setup

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// AssumeYes answers every setup question with its default ("yes"), for
// unattended installs (-yes flag).
var AssumeYes bool

// AppDir is where cs-translate keeps its private tools (uv), the speech
// worker and downloaded models:
//
//	Windows: %LOCALAPPDATA%\cs-translate
//	macOS:   ~/Library/Caches/cs-translate
//	Linux:   ~/.cache/cs-translate
func AppDir() (string, error) {
	if dir := os.Getenv("CS_TRANSLATE_HOME"); dir != "" {
		return dir, os.MkdirAll(dir, 0o755)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "cs-translate")
	return dir, os.MkdirAll(dir, 0o755)
}

// askYesNo asks a question that defaults to yes.
func askYesNo(scanner *bufio.Scanner, question string) bool {
	fmt.Printf("%s [Y/n]: ", question)
	if AssumeYes {
		fmt.Println("y")
		return true
	}
	if !scanner.Scan() {
		return false
	}
	in := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return in == "" || in == "y" || in == "yes"
}

func runVisible(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// downloadFile fetches url into dest.
func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
