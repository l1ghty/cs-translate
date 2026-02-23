package setup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/micha/cs-ingame-translate/translator"
)

func SetupDockerContainer(scanner *bufio.Scanner) error {
	fmt.Println("Setting up Docker container with Ollama and Whisper...")

	if err := CheckDocker(); err != nil {
		return fmt.Errorf("docker is required: %w", err)
	}

	if err := CheckAndInstallNvidiaContainerToolkit(scanner); err != nil {
		return fmt.Errorf("nvidia-container-toolkit is required for GPU support: %w", err)
	}

	containerName := "cs-translate"

	if running := checkContainerRunning(containerName); running {
		fmt.Println("Docker container already running")
	} else if exists := checkContainerExists(containerName); exists {
		fmt.Println("Starting existing Docker container...")
		if err := startContainer(containerName); err != nil {
			return fmt.Errorf("failed to start container: %w", err)
		}
	} else {
		if err := buildAndRunContainer(containerName); err != nil {
			return err
		}
	}

	if err := waitForOllama(); err != nil {
		return err
	}

	model := translator.DefaultOllamaModel
	fmt.Printf("Checking for Ollama model '%s'...\n", model)
	return CheckAndPullDockerModel(scanner, model)
}

func CheckDocker() error {
	cmd := exec.Command("docker", "ps")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker is not running or not installed: %w", err)
	}
	return nil
}

func checkContainerRunning(name string) bool {
	cmd := exec.Command("docker", "ps", "--filter", "name="+name, "--format", "{{.Names}}")
	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == name
}

func checkContainerExists(name string) bool {
	cmd := exec.Command("docker", "ps", "-a", "--filter", "name="+name, "--format", "{{.Names}}")
	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == name
}

func startContainer(name string) error {
	cmd := exec.Command("docker", "start", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildAndRunContainer(name string) error {
	fmt.Println("Building Docker container (first time only, this may take a few minutes)...")

	tmpDir, err := os.MkdirTemp("", "cs-translate-docker")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), dockerfileContent, 0644); err != nil {
		return fmt.Errorf("failed to write Dockerfile: %w", err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "transcriber.py"), transcriberScript, 0644); err != nil {
		return fmt.Errorf("failed to write transcriber.py: %w", err)
	}

	buildCmd := exec.Command("docker", "build", "-t", "cs-translate:latest", ".")
	buildCmd.Dir = tmpDir
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("failed to build docker image: %w", err)
	}

	rmCmd := exec.Command("docker", "rm", "-f", name)
	rmCmd.Run()

	volCreateCmd := exec.Command("docker", "volume", "create", "cs-translate-models")
	volCreateCmd.Run()

	hostPort := translator.DefaultOllamaPort
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", hostPort))
	if err != nil {
		fmt.Printf("Port %d is already in use. Looking for an available port...\n", hostPort)
		hostPort, err = translator.FindAvailablePort(hostPort + 1)
		if err != nil {
			return fmt.Errorf("could not find an available port: %w", err)
		}
		fmt.Printf("Using alternative port: %d\n", hostPort)
		fmt.Println("Note: You'll need to set OLLAMA_HOST to use this port.")
		fmt.Printf("Run: export OLLAMA_HOST=http://localhost:%d\n", hostPort)
	} else {
		ln.Close()
	}

	portStr := fmt.Sprintf("%d:%d", hostPort, translator.DefaultOllamaPort)
	runCmd := exec.Command("docker", "run", "-d",
		"--gpus", "all",
		"--name", name,
		"-p", portStr,
		"-v", "cs-translate-models:/data",
		"--privileged",
		"cs-translate:latest")
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	if err := runCmd.Run(); err != nil {
		return fmt.Errorf("failed to start docker container: %w", err)
	}

	time.Sleep(5 * time.Second)
	return nil
}

func waitForOllama() error {
	client := &http.Client{Timeout: 10 * time.Second}
	ollamaURL := translator.OllamaHost

	fmt.Println("Waiting for Ollama to be ready...")
	for i := 0; i < 30; i++ {
		resp, err := client.Get(ollamaURL + "/api/version")
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(2 * time.Second)
	}

	resp, err := client.Get(ollamaURL + "/api/version")
	if err != nil {
		return fmt.Errorf("Ollama Docker container not responding: %w", err)
	}
	defer resp.Body.Close()

	var versionResp OllamaVersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&versionResp); err == nil {
		fmt.Printf("✔ Ollama is running in Docker (version: %s)\n", versionResp.Version)
	} else {
		fmt.Println("✔ Ollama is running in Docker")
	}
	return nil
}

func CheckAndPullDockerModel(scanner *bufio.Scanner, model string) error {
	ollamaURL := translator.OllamaHost

	modelURL := fmt.Sprintf("%s/api/tags", ollamaURL)
	resp, err := http.Get(modelURL)
	if err == nil {
		defer resp.Body.Close()

		var tagsResp struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err == nil {
			for _, m := range tagsResp.Models {
				if strings.HasPrefix(m.Name, model) {
					fmt.Printf("✔ Model '%s' is already installed\n", model)
					return nil
				}
			}
		}
	}

	fmt.Printf("Model '%s' not found.\n", model)
	fmt.Printf("Do you want to download '%s'? (~2GB, required for translation) [Y/n]: ", model)
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" || strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" {
			fmt.Printf("Pulling model '%s' in Docker... (this may take a few minutes)\n", model)
			pullCmd := exec.Command("docker", "exec", "cs-translate", "ollama", "pull", model)
			pullCmd.Stdout = os.Stdout
			pullCmd.Stderr = os.Stderr
			if err := pullCmd.Run(); err != nil {
				return fmt.Errorf("failed to pull model: %w", err)
			}
			fmt.Printf("✔ Model '%s' downloaded successfully\n", model)
		} else {
			return fmt.Errorf("model '%s' is required for translation", model)
		}
	}

	return nil
}

func CheckAndPullModel(scanner *bufio.Scanner, model string) error {
	ollamaURL := translator.OllamaHost

	checkCmd := exec.Command("ollama", "list")
	output, err := checkCmd.CombinedOutput()
	if err != nil {
		modelURL := fmt.Sprintf("%s/api/tags", ollamaURL)
		resp, err := http.Get(modelURL)
		if err != nil {
			fmt.Println("Warning: Could not check installed models")
			goto PullModel
		}
		defer resp.Body.Close()

		var tagsResp struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
			fmt.Println("Warning: Could not parse installed models")
			goto PullModel
		}

		for _, m := range tagsResp.Models {
			if strings.HasPrefix(m.Name, model) {
				fmt.Printf("✔ Model '%s' is already installed\n", model)
				return nil
			}
		}
	} else {
		if strings.Contains(string(output), model) {
			fmt.Printf("✔ Model '%s' is already installed\n", model)
			return nil
		}
	}

PullModel:
	fmt.Printf("Model '%s' not found.\n", model)
	fmt.Printf("Do you want to download '%s'? (~2GB, required for translation) [Y/n]: ", model)
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" || strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" {
			fmt.Printf("Pulling model '%s'... (this may take a few minutes)\n", model)
			pullCmd := exec.Command("ollama", "pull", model)
			pullCmd.Stdout = os.Stdout
			pullCmd.Stderr = os.Stderr
			if err := pullCmd.Run(); err != nil {
				return fmt.Errorf("failed to pull model: %w", err)
			}
			fmt.Printf("✔ Model '%s' downloaded successfully\n", model)
		} else {
			return fmt.Errorf("model '%s' is required for translation", model)
		}
	}

	return nil
}
