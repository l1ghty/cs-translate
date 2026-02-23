package setup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/micha/cs-ingame-translate/translator"
)

type OllamaVersionResponse struct {
	Version string `json:"version"`
}

func SetupOllama(scanner *bufio.Scanner) error {
	fmt.Println("Checking Ollama installation...")

	useDocker := os.Getenv("USE_DOCKER_OLLAMA") != "0"

	if os.Getenv("USE_DOCKER_OLLAMA") == "" && runtime.GOOS == "windows" {
		if err := CheckDocker(); err != nil {
			fmt.Println("Docker not detected. Defaulting to native installation.")
			useDocker = false
		} else {
			fmt.Println("Select installation method:")
			fmt.Println("1. Docker (Recommended - Unified container)")
			fmt.Println("2. Native (Run Ollama and Python directly on Windows)")
			fmt.Print("Enter choice [1]: ")
			if scanner.Scan() {
				input := strings.TrimSpace(scanner.Text())
				if input == "2" {
					useDocker = false
				}
			}
		}

		if !useDocker {
			os.Setenv("USE_DOCKER_OLLAMA", "0")
			os.Setenv("USE_DOCKER_WHISPER", "0")
		}
	}

	if useDocker {
		return SetupDockerContainer(scanner)
	}

	ollamaURL := translator.OllamaHost

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(ollamaURL + "/api/version")
	if err != nil {
		fmt.Printf("Ollama is not running or not accessible at %s\n", ollamaURL)
		fmt.Println("Ollama is required for translation.")
		fmt.Println("you can set USE_DOCKER_OLLAMA=0 for no isolation in docker (more performant).")
		fmt.Print("Do you want to install Ollama (with docker)? [Y/n]: ")
		if scanner.Scan() {
			input := strings.TrimSpace(scanner.Text())
			if input == "" || strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" {
				if err := InstallOllama(scanner); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("Ollama is required for translation")
			}
		}
		resp, err = client.Get(ollamaURL + "/api/version")
		if err != nil {
			return fmt.Errorf("Ollama still not accessible after installation")
		}
	}
	defer resp.Body.Close()

	var versionResp OllamaVersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&versionResp); err == nil {
		fmt.Printf("✔ Ollama is running (version: %s)\n", versionResp.Version)
	} else {
		fmt.Println("✔ Ollama is running")
	}

	model := translator.DefaultOllamaModel
	fmt.Printf("Checking for Ollama model '%s'...\n", model)
	return CheckAndPullModel(scanner, model)
}

func InstallOllama(scanner *bufio.Scanner) error {
	if runtime.GOOS == "windows" {
		fmt.Println("Installing Ollama for Windows...")
		fmt.Println("Downloading Ollama installer...")

		installerURL := "https://ollama.com/download/OllamaSetup.exe"
		tmpDir := os.TempDir()
		installerPath := filepath.Join(tmpDir, "OllamaSetup.exe")

		if err := DownloadFile(installerURL, installerPath); err != nil {
			fmt.Printf("Failed to download installer: %v\n", err)
			fmt.Println("Please download Ollama manually from: https://ollama.com")
			return fmt.Errorf("failed to download Ollama")
		}
		defer os.Remove(installerPath)

		fmt.Println("Running Ollama installer...")
		cmd := exec.Command(installerPath)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("installer failed: %w", err)
		}

		fmt.Println("✔ Ollama installed. Starting service...")
		time.Sleep(3 * time.Second)
		return nil
	}

	fmt.Println("Installing Ollama for Linux...")
	cmd := exec.Command("sh", "-c", "curl -fsSL https://ollama.com/install.sh | sh")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Automatic installation failed: %v\n", err)
		fmt.Println("Please install Ollama manually:")
		fmt.Println("  curl -fsSL https://ollama.com/install.sh | sh")
		return fmt.Errorf("failed to install Ollama")
	}

	fmt.Println("✔ Ollama installed successfully")
	fmt.Println("Starting Ollama service...")

	port := translator.DefaultOllamaPort
	ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		fmt.Printf("Port %d is already in use. Looking for an available port...\n", port)
		ln.Close()
		port, err = translator.FindAvailablePort(port + 1)
		if err != nil {
			return fmt.Errorf("could not find an available port: %w", err)
		}
		fmt.Printf("Using alternative port: %d\n", port)
		fmt.Println("Note: You'll need to set OLLAMA_HOST to use this port.")
		fmt.Printf("Run: export OLLAMA_HOST=http://localhost:%d\n", port)
	} else {
		ln.Close()
	}

	env := os.Environ()
	env = append(env, fmt.Sprintf("OLLAMA_HOST=localhost:%d", port))
	ollamaCmd := exec.Command("ollama", "serve")
	ollamaCmd.Env = env
	ollamaCmd.Stdout = os.Stdout
	ollamaCmd.Stderr = os.Stderr
	if err := ollamaCmd.Start(); err != nil {
		fmt.Printf("Warning: Could not start Ollama service: %v\n", err)
	}
	time.Sleep(2 * time.Second)

	return nil
}

func DownloadFile(url string, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
