package setup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func SetupPythonEnv(scanner *bufio.Scanner) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	pythonExe := "python3"
	if runtime.GOOS == "windows" {
		pythonExe = "python"
	}

	if _, err := exec.LookPath(pythonExe); err != nil {
		found := false
		if runtime.GOOS == "linux" {
			if _, err := exec.LookPath("python"); err == nil {
				pythonExe = "python"
				found = true
			}
		}

		if !found {
			fmt.Printf("Error: Python interpreter (%s) not found.\n", pythonExe)
			if err := InstallDependency(scanner, "python"); err != nil {
				PrintManualInstallInstructions("python")
				return err
			}
			if _, err := exec.LookPath(pythonExe); err != nil {
				if runtime.GOOS == "windows" {
					pythonExe = "python"
				}
				if _, err := exec.LookPath(pythonExe); err != nil {
					return fmt.Errorf("python still not found after installation")
				}
			}
		}
	}
	fmt.Printf("✔ Python interpreter found (%s).\n", pythonExe)

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
					if runtime.GOOS == "linux" {
						fmt.Println("Error: Failed to create venv. You might need to install 'python3-venv'.")
						if err := InstallDependency(scanner, "python3-venv"); err == nil {
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
	pipExe := filepath.Join(venvDir, "bin", "pip")
	if runtime.GOOS == "windows" {
		pipExe = filepath.Join(venvDir, "Scripts", "pip.exe")
	}

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
