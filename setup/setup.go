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

// EnsureEnvironment checks for required dependencies and offers to install them if possible
func EnsureEnvironment(scanner *bufio.Scanner, useVoice bool) error {
	// 1. Check FFmpeg (always required for audio capture)
	if err := checkFFmpeg(); err != nil {
		fmt.Println("Error: FFmpeg is required but not found in PATH.")

		// Attempt auto-install
		if err := installDependency(scanner, "ffmpeg"); err != nil {
			fmt.Printf("Could not install FFmpeg automatically: %v\n", err)
			printManualInstallInstructions("ffmpeg")
			return err
		}

		// Recheck
		if err := checkFFmpeg(); err != nil {
			fmt.Println("Error: FFmpeg still not found after installation attempt. Please restart your shell or install manually.")
			return err
		}
		fmt.Println("✔ FFmpeg installed and found.")
	} else {
		fmt.Println("✔ FFmpeg found.")
	}

	// 2. Setup Python environment for Voice Transcription
	if useVoice {
		if err := setupPythonEnv(scanner); err != nil {
			return fmt.Errorf("failed to setup python environment: %w", err)
		}
	}

	return nil
}

func checkFFmpeg() error {
	_, err := exec.LookPath("ffmpeg")
	return err
}

func printManualInstallInstructions(pkg string) {
	if pkg == "ffmpeg" {
		if runtime.GOOS == "linux" {
			fmt.Println("Please run: sudo apt install ffmpeg (or equivalent for your distro)")
		} else if runtime.GOOS == "windows" {
			fmt.Println("Please download FFmpeg from https://gyan.dev/ffmpeg/builds/ and add it to your PATH.")
		}
	} else if pkg == "python" {
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
			if pkgName == "ffmpeg" {
				id = "Gyan.FFmpeg"
			} else if pkgName == "python" {
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
