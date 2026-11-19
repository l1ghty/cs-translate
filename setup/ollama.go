package setup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/micha/cs-ingame-translate/translator"
	"github.com/micha/cs-ingame-translate/ui"
)

// SetupOllama makes sure a native Ollama server is reachable and the
// translation model is available, installing Ollama if needed.
func SetupOllama(scanner *bufio.Scanner, model string) error {
	base := translator.OllamaHost
	st := ui.Start("Checking Ollama (translation engine)")
	_, err := ollamaVersion(base)
	st.Stop()

	if err != nil {
		bin := findOllama()
		if bin == "" {
			fmt.Println("Ollama is not installed. It runs the local translation model.")
			if !askYesNo(scanner, "Install Ollama now?") {
				return fmt.Errorf("ollama is required for translation (https://ollama.com/download)")
			}
			if err := installOllama(scanner); err != nil {
				return err
			}
			bin = findOllama()
			// The installers start Ollama's tray app, which brings up the
			// server by itself; give it a moment instead of racing it.
			st := ui.Start("Waiting for Ollama to start")
			waitOllama(base, 15*time.Second)
			st.Stop()
		}
		if _, err := ollamaVersion(base); err != nil {
			if bin == "" {
				return fmt.Errorf("ollama was installed but could not be found; open a new terminal or start Ollama, then run again")
			}
			if err := startOllama(bin, base); err != nil {
				return err
			}
		}
	}

	version, err := ollamaVersion(base)
	if err != nil {
		return fmt.Errorf("ollama is not responding at %s: %w", base, err)
	}
	fmt.Printf("✔ Ollama is running (version %s)\n", version)
	return ensureModel(scanner, base, model)
}

func ollamaVersion(base string) (string, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base + "/api/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var v struct {
		Version string `json:"version"`
	}
	json.NewDecoder(resp.Body).Decode(&v)
	return v.Version, nil
}

// findOllama looks for the ollama binary on PATH and in the default install locations.
func findOllama() string {
	if p, err := exec.LookPath("ollama"); err == nil {
		return p
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			candidates = append(candidates, filepath.Join(dir, "Programs", "Ollama", "ollama.exe"))
		}
		candidates = append(candidates, `C:\Program Files\Ollama\ollama.exe`)
	case "darwin":
		candidates = []string{
			"/Applications/Ollama.app/Contents/Resources/ollama",
			"/opt/homebrew/bin/ollama",
			"/usr/local/bin/ollama",
		}
	default:
		candidates = []string{"/usr/local/bin/ollama", "/usr/bin/ollama"}
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func installOllama(scanner *bufio.Scanner) error {
	switch runtime.GOOS {
	case "windows":
		if err := installOllamaWindows(); err != nil {
			return err
		}
	case "darwin":
		if _, err := exec.LookPath("brew"); err == nil {
			if err := runVisible("brew", "install", "ollama"); err != nil {
				return fmt.Errorf("brew install ollama failed: %w", err)
			}
			break
		}
		fmt.Println("Opening https://ollama.com/download - install Ollama, start it, then press Enter here.")
		exec.Command("open", "https://ollama.com/download").Start()
		if !AssumeYes {
			scanner.Scan()
		}
	default:
		tmp := filepath.Join(os.TempDir(), "ollama-install.sh")
		if err := downloadFile("https://ollama.com/install.sh", tmp, "Downloading the Ollama install script"); err != nil {
			return fmt.Errorf("download failed (%v); see https://ollama.com/download/linux", err)
		}
		defer os.Remove(tmp)
		fmt.Println("Running the official Ollama install script (may ask for your sudo password)...")
		if err := runVisible("sh", tmp); err != nil {
			return fmt.Errorf("ollama install script failed: %w", err)
		}
	}
	fmt.Println("✔ Ollama installed")
	return nil
}

// installOllamaWindows installs Ollama without any clicks: through winget
// when it is available, otherwise with the official installer in silent
// mode. Both install per user (%LOCALAPPDATA%\Programs\Ollama), so no
// admin rights are needed.
func installOllamaWindows() error {
	if winget, err := exec.LookPath("winget"); err == nil {
		fmt.Println("Installing Ollama with winget...")
		err := runVisible(winget, "install", "--id", "Ollama.Ollama", "--exact", "--silent",
			"--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
		if err == nil && findOllama() != "" {
			return nil
		}
		fmt.Printf("winget could not install Ollama (%v), using the official installer instead\n", err)
	}
	tmp := filepath.Join(os.TempDir(), "OllamaSetup.exe")
	if err := downloadFile("https://ollama.com/download/OllamaSetup.exe", tmp, "Downloading the Ollama installer"); err != nil {
		return fmt.Errorf("download failed (%v); install Ollama from https://ollama.com/download", err)
	}
	defer os.Remove(tmp)
	st := ui.Start("Installing Ollama")
	err := exec.Command(tmp, "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-").Run()
	st.Stop()
	if err != nil {
		return fmt.Errorf("ollama installer failed (%v); install Ollama from https://ollama.com/download", err)
	}
	return nil
}

// waitOllama polls the Ollama server until it answers or the timeout passes.
func waitOllama(base string, timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); ; time.Sleep(500 * time.Millisecond) {
		if _, err := ollamaVersion(base); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// startOllama starts "ollama serve" in the background and waits for it.
func startOllama(bin, base string) error {
	if u, err := url.Parse(base); err == nil && portInUse(u.Host) {
		return fmt.Errorf("port %s is used by another program and Ollama does not answer there; set OLLAMA_HOST to a free address", u.Port())
	}
	st := ui.Start("Starting Ollama")
	defer st.Stop()
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(),
		"OLLAMA_HOST="+strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://"),
		// keep memory use predictable while gaming
		"OLLAMA_MAX_LOADED_MODELS=1",
		"OLLAMA_NUM_PARALLEL=1",
	)
	if dir, err := AppDir(); err == nil {
		if f, err := os.Create(filepath.Join(dir, "ollama.log")); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
		}
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start ollama: %w", err)
	}
	go cmd.Wait()
	if !waitOllama(base, 30*time.Second) {
		return fmt.Errorf("ollama did not start within 30s")
	}
	return nil
}

func portInUse(hostport string) bool {
	conn, err := net.DialTimeout("tcp", hostport, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// ensureModel pulls the translation model through the Ollama API (works even
// when the ollama CLI is not on PATH yet, e.g. right after installing).
func ensureModel(scanner *bufio.Scanner, base, model string) error {
	if hasModel(base, model) {
		fmt.Printf("✔ Translation model '%s' is installed\n", model)
		return nil
	}
	fmt.Printf("Translation model '%s' is not downloaded yet.\n", model)
	if !askYesNo(scanner, "Download it now? (one-time, ~1-2 GB)") {
		return fmt.Errorf("model '%s' is required for translation", model)
	}
	st := ui.Start("Downloading translation model")
	defer st.Stop()
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	resp, err := http.Post(base+"/api/pull", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("pulling model: %w", err)
	}
	defer resp.Body.Close()

	dec := json.NewDecoder(resp.Body)
	for {
		var p struct {
			Status    string `json:"status"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if err := dec.Decode(&p); err != nil {
			break
		}
		if p.Error != "" {
			return fmt.Errorf("pulling model: %s", p.Error)
		}
		st.Set("Translation model: " + p.Status)
		st.Progress(p.Completed, p.Total, "B")
	}
	st.Set("Checking translation model")
	st.Progress(0, 0, "")
	if !hasModel(base, model) {
		return fmt.Errorf("model '%s' could not be downloaded", model)
	}
	fmt.Printf("✔ Translation model '%s' downloaded\n", model)
	return nil
}

func hasModel(base, model string) bool {
	resp, err := http.Get(base + "/api/tags")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&tags) != nil {
		return false
	}
	for _, m := range tags.Models {
		if m.Name == model || strings.TrimSuffix(m.Name, ":latest") == model || strings.HasPrefix(m.Name, model+":") {
			return true
		}
	}
	return false
}
