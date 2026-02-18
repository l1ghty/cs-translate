package setup

import (
	"bufio"
	"encoding/json"
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

// EnsureEnvironment checks for required dependencies and offers to install them if possible
func EnsureEnvironment(scanner *bufio.Scanner, useVoice bool) error {
	// Setup Ollama for translation
	if err := setupOllama(scanner); err != nil {
		return fmt.Errorf("failed to setup Ollama: %w", err)
	}

	// Setup Python environment for voice transcription.
	if useVoice {
		if err := setupPythonEnv(scanner); err != nil {
			return fmt.Errorf("failed to setup python environment: %w", err)
		}
	}

	return nil
}

// OllamaVersionResponse represents the response from Ollama version API
type OllamaVersionResponse struct {
	Version string `json:"version"`
}

// setupOllama checks for Ollama installation and pulls the required model
func setupOllama(scanner *bufio.Scanner) error {
	fmt.Println("Checking Ollama installation...")

	// Check if Ollama is running
	ollamaURL := "http://localhost:11434"
	if envURL := os.Getenv("OLLAMA_HOST"); envURL != "" {
		ollamaURL = envURL
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(ollamaURL + "/api/version")
	if err != nil {
		fmt.Printf("Ollama is not running or not accessible at %s\n", ollamaURL)
		fmt.Println("Ollama is required for translation.")
		fmt.Print("Do you want to install Ollama? [Y/n]: ")
		if scanner.Scan() {
			input := strings.TrimSpace(scanner.Text())
			if input == "" || strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" {
				if err := installOllama(scanner); err != nil {
					return err
				}
			} else {
				return fmt.Errorf("Ollama is required for translation")
			}
		}
		// Recheck after installation
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

	// Check and pull the model
	model := "llama3.2"
	fmt.Printf("Checking for Ollama model '%s'...\n", model)
	if err := checkAndPullModel(scanner, model); err != nil {
		return err
	}

	return nil
}

// checkAndPullModel checks if a model exists and pulls it if not
func checkAndPullModel(scanner *bufio.Scanner, model string) error {
	ollamaURL := "http://localhost:11434"
	if envURL := os.Getenv("OLLAMA_HOST"); envURL != "" {
		ollamaURL = envURL
	}

	// Check if model exists
	checkCmd := exec.Command("ollama", "list")
	output, err := checkCmd.CombinedOutput()
	if err != nil {
		// ollama command might not be in PATH, but server is running
		// Try to check via API
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
		// Check if model in output
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

// installOllama attempts to install Ollama on the system
func installOllama(scanner *bufio.Scanner) error {
	if runtime.GOOS == "windows" {
		fmt.Println("Installing Ollama for Windows...")
		fmt.Println("Downloading Ollama installer...")

		// Download Ollama installer
		installerURL := "https://ollama.com/download/OllamaSetup.exe"
		tmpDir := os.TempDir()
		installerPath := filepath.Join(tmpDir, "OllamaSetup.exe")

		if err := downloadFile(installerURL, installerPath); err != nil {
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
		// Wait a moment for service to start
		time.Sleep(3 * time.Second)
		return nil
	}

	// Linux installation
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
	// Start ollama in background
	ollamaCmd := exec.Command("ollama", "serve")
	ollamaCmd.Stdout = os.Stdout
	ollamaCmd.Stderr = os.Stderr
	if err := ollamaCmd.Start(); err != nil {
		fmt.Printf("Warning: Could not start Ollama service: %v\n", err)
	}
	time.Sleep(2 * time.Second)

	return nil
}

// downloadFile downloads a file from URL to destination
func downloadFile(url string, dest string) error {
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

func printManualInstallInstructions(pkg string) {
	if pkg == "python" {
		fmt.Println("Please install Python 3.9+ from python.org")
	}
}

func setupPythonEnv(scanner *bufio.Scanner) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Python Executable Name
	pythonExe := "python3"
	if runtime.GOOS == "windows" {
		pythonExe = "python"
	}

	// Check if Python installed
	if _, err := exec.LookPath(pythonExe); err != nil {
		// Try "python" on Linux just in case
		found := false
		if runtime.GOOS == "linux" {
			if _, err := exec.LookPath("python"); err == nil {
				pythonExe = "python"
				found = true
			}
		}

		if !found {
			fmt.Printf("Error: Python interpreter (%s) not found.\n", pythonExe)
			if err := installDependency(scanner, "python"); err != nil {
				printManualInstallInstructions("python")
				return err
			}
			// Recheck
			if _, err := exec.LookPath(pythonExe); err != nil {
				// Try again with fallback check logic or just fail
				if runtime.GOOS == "windows" {
					pythonExe = "python" // Should be in path now?
				}
				if _, err := exec.LookPath(pythonExe); err != nil {
					return fmt.Errorf("python still not found after installation")
				}
			}
		}
	}
	fmt.Printf("✔ Python interpreter found (%s).\n", pythonExe)

	// Check for venv directory
	venvDir := filepath.Join(cwd, "venv")
	if _, err := os.Stat(venvDir); os.IsNotExist(err) {
		fmt.Printf("Python virtual environment 'venv' not found.\n")
		fmt.Print("Do you want to create it automatically? [Y/n]: ")
		if scanner.Scan() {
			input := strings.TrimSpace(scanner.Text())
			if input == "" || strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" {
				fmt.Println("Creating virtual environment...")
				cmd := exec.Command(pythonExe, "-m", "venv", "venv")
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					// On Linux, venv module might be missing (python3-venv package)
					if runtime.GOOS == "linux" {
						fmt.Println("Error: Failed to create venv. You might need to install 'python3-venv'.")
						// Try to install python3-venv
						if err := installDependency(scanner, "python3-venv"); err == nil {
							fmt.Println("Retrying venv creation...")
							if err := cmd.Run(); err != nil {
								return fmt.Errorf("failed to create venv after installing package: %w", err)
							}
							fmt.Println("✔ Virtual environment created.")
							goto VenvCreated
						}
					}
					return fmt.Errorf("failed to create venv: %w", err)
				}
				fmt.Println("✔ Virtual environment created.")
			} else {
				return fmt.Errorf("virtual environment is required for voice transcription")
			}
		}
	} else {
		fmt.Println("✔ Virtual environment 'venv' exists.")
	}

VenvCreated:
	// Check if openai-whisper is installed in venv
	pipExe := filepath.Join(venvDir, "bin", "pip")
	if runtime.GOOS == "windows" {
		pipExe = filepath.Join(venvDir, "Scripts", "pip.exe")
	}

	// Run python from venv and try import
	pythonVenvExe := filepath.Join(venvDir, "bin", "python3")
	if runtime.GOOS == "windows" {
		pythonVenvExe = filepath.Join(venvDir, "Scripts", "python.exe")
	}

	fmt.Println("Checking for 'openai-whisper' package...")
	checkCmd := exec.Command(pythonVenvExe, "-c", "import whisper; print('ok')")
	if err := checkCmd.Run(); err != nil {
		fmt.Println("'openai-whisper' package not found in venv.")
		fmt.Print("Do you want to install it now? (This will download PyTorch ~1GB) [Y/n]: ")
		if scanner.Scan() {
			input := strings.TrimSpace(scanner.Text())
			if input == "" || strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" {
				fmt.Println("Installing openai-whisper...")
				installCmd := exec.Command(pipExe, "install", "openai-whisper")
				installCmd.Stdout = os.Stdout
				installCmd.Stderr = os.Stderr
				if err := installCmd.Run(); err != nil {
					return fmt.Errorf("failed to install openai-whisper: %w", err)
				}
				fmt.Println("✔ 'openai-whisper' installed successfully.")
			} else {
				return fmt.Errorf("openai-whisper is required for voice transcription")
			}
		}
	} else {
		fmt.Println("✔ 'openai-whisper' is already installed.")
	}

	return nil
}

// installDependency attempts to install a package using detected package manager
func installDependency(scanner *bufio.Scanner, pkgName string) error {
	pm, cmdArgs := detectPackageManager(pkgName)
	if pm == "" {
		return fmt.Errorf("no supported package manager found")
	}

	fmt.Printf("Package manager '%s' detected.\n", pm)
	fmt.Printf("Do you want to install '%s' using %s? [Y/n]: ", pkgName, pm)
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input != "" && strings.ToLower(input) != "y" && strings.ToLower(input) != "yes" {
			return fmt.Errorf("installation aborted by user")
		}
	}

	fmt.Printf("Running: %s %s\n", pm, strings.Join(cmdArgs, " "))
	cmd := exec.Command(pm, cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin // Connect stdin for potential sudo password prompt or confirmations

	return cmd.Run()
}

func detectPackageManager(pkgName string) (string, []string) {
	if runtime.GOOS == "windows" {
		// Winget (Preferred)
		if _, err := exec.LookPath("winget"); err == nil {
			// Map generic names to Winget IDs
			id := pkgName
			if pkgName == "python" {
				id = "Python.Python.3.11"
			}
			return "winget", []string{"install", "-e", "--id", id}
		}
		// Chocolatey
		if _, err := exec.LookPath("choco"); err == nil {
			return "choco", []string{"install", pkgName, "-y"}
		}
		// Scoop
		if _, err := exec.LookPath("scoop"); err == nil {
			return "scoop", []string{"install", pkgName}
		}
	} else {
		// Linux
		// Debian/Ubuntu
		if _, err := exec.LookPath("apt-get"); err == nil {
			return "sudo", []string{"apt-get", "install", "-y", pkgName}
		}
		// Fedora/RHEL
		if _, err := exec.LookPath("dnf"); err == nil {
			return "sudo", []string{"dnf", "install", "-y", pkgName}
		}
		// Arch
		if _, err := exec.LookPath("pacman"); err == nil {
			// "python" typically points to python3 on Arch
			target := pkgName
			if pkgName == "python3-venv" {
				// Arch includes venv in python package usually, but let's be safe
				// Wait, Arch doesn't split python usually.
				// If we are asking for python3-venv on Arch, it might fail if package doesn't exist.
				// Let's assume if we need python3-venv, we might be on Debian, but if we are on Arch, check if pacman has it?
				// Actually, better to just return nil if we think it's not relevant?
				// Or let it try and fail.
			}
			return "sudo", []string{"pacman", "-S", "--noconfirm", target}
		}
		// Zypper (OpenSUSE)
		if _, err := exec.LookPath("zypper"); err == nil {
			return "sudo", []string{"zypper", "install", "-y", pkgName}
		}
	}
	return "", nil
}
