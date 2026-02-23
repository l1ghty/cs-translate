package setup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func CheckAndInstallNvidiaContainerToolkit(scanner *bufio.Scanner) error {
	if runtime.GOOS != "linux" {
		return nil
	}

	fmt.Println("Checking for nvidia-container-toolkit...")

	checkCmd := exec.Command("nvidia-container-runtime", "--version")
	if err := checkCmd.Run(); err == nil {
		fmt.Println("✔ nvidia-container-toolkit is already installed")
		return nil
	}

	fmt.Println("nvidia-container-toolkit is required for GPU support in Docker.")
	fmt.Println("Do you want to install it now? [Y/n]: ")
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input != "" && strings.ToLower(input) != "y" && strings.ToLower(input) != "yes" {
			return fmt.Errorf("nvidia-container-toolkit is required for GPU support")
		}
	}

	if runtime.GOOS == "linux" {
		return installNvidiaContainerToolkitLinux(scanner)
	}

	fmt.Println("nvidia-container-toolkit installation is only supported on Linux.")
	return fmt.Errorf("nvidia-container-toolkit is required for GPU support")
}

func installNvidiaContainerToolkitLinux(scanner *bufio.Scanner) error {
	fmt.Println("Installing nvidia-container-toolkit on Linux...")

	if _, err := exec.LookPath("curl"); err != nil {
		fmt.Println("curl is required for installation.")
		fmt.Print("Do you want to install curl? [Y/n]: ")
		if scanner.Scan() {
			input := strings.TrimSpace(scanner.Text())
			if input == "" || strings.ToLower(input) == "y" || strings.ToLower(input) == "yes" {
				if err := InstallDependency(scanner, "curl"); err != nil {
					return fmt.Errorf("failed to install curl: %w", err)
				}
			} else {
				return fmt.Errorf("curl is required for installation")
			}
		}
	}

	distribution := ""
	osReleaseFile := "/etc/os-release"
	if data, err := os.ReadFile(osReleaseFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "ID=") {
				distribution = strings.TrimPrefix(line, "ID=")
				distribution = strings.Trim(distribution, `"`)
				break
			}
		}
	}

	if distribution == "ubuntu" || distribution == "debian" {
		return installNvidiaContainerToolkitUbuntu(scanner)
	} else if distribution == "fedora" || distribution == "rhel" || distribution == "centos" {
		return installNvidiaContainerToolkitFedora(scanner)
	} else if distribution == "arch" {
		return installNvidiaContainerToolkitArch(scanner)
	}

	fmt.Printf("Unsupported distribution: %s\n", distribution)
	fmt.Println("Please install nvidia-container-toolkit manually.")
	return fmt.Errorf("unsupported distribution for automatic installation")
}

func installNvidiaContainerToolkitUbuntu(scanner *bufio.Scanner) error {
	fmt.Println("Setting up nvidia-container-toolkit for Ubuntu/Debian...")

	distribution := "$(. /etc/os-release;echo $ID$VERSION_ID)"

	addRepoCmd := fmt.Sprintf(`distribution=%s && \
		curl -fsSL https://nvidia.github.io/nvidia-docker/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg && \
		curl -s -L https://nvidia.github.io/nvidia-docker/$distribution/nvidia-docker.list | \
		sudo tee /etc/apt/sources.list.d/nvidia-docker.list > /dev/null`, distribution)

	cmd := exec.Command("sh", "-c", addRepoCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add nvidia-docker repository: %w", err)
	}

	updateCmd := exec.Command("sudo", "apt-get", "update")
	updateCmd.Stdout = os.Stdout
	updateCmd.Stderr = os.Stderr
	if err := updateCmd.Run(); err != nil {
		return fmt.Errorf("failed to update apt: %w", err)
	}

	installCmd := exec.Command("sudo", "apt-get", "install", "-y", "nvidia-container-toolkit")
	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("failed to install nvidia-container-toolkit: %w", err)
	}

	fmt.Println("Restarting Docker service...")
	restartCmd := exec.Command("sudo", "systemctl", "restart", "docker")
	restartCmd.Stdout = os.Stdout
	restartCmd.Stderr = os.Stderr
	restartCmd.Run()

	fmt.Println("✔ nvidia-container-toolkit installed successfully")
	return nil
}

func installNvidiaContainerToolkitFedora(scanner *bufio.Scanner) error {
	fmt.Println("Setting up nvidia-container-toolkit for Fedora/RHEL...")

	distribution := "$(. /etc/os-release;echo $ID$VERSION_ID)"

	addRepoCmd := fmt.Sprintf(`distribution=%s && \
		curl -fsSL https://nvidia.github.io/nvidia-docker/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg && \
		curl -s -L https://nvidia.github.io/nvidia-docker/$distribution/nvidia-docker.list | \
		sudo tee /etc/apt/sources.list.d/nvidia-docker.list > /dev/null`, distribution)

	cmd := exec.Command("sh", "-c", addRepoCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to add nvidia-docker repository: %w", err)
	}

	installCmd := exec.Command("sudo", "dnf", "install", "-y", "nvidia-container-toolkit")
	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("failed to install nvidia-container-toolkit: %w", err)
	}

	fmt.Println("Restarting Docker service...")
	restartCmd := exec.Command("sudo", "systemctl", "restart", "docker")
	restartCmd.Stdout = os.Stdout
	restartCmd.Stderr = os.Stderr
	restartCmd.Run()

	fmt.Println("✔ nvidia-container-toolkit installed successfully")
	return nil
}

func installNvidiaContainerToolkitArch(scanner *bufio.Scanner) error {
	fmt.Println("Setting up nvidia-container-toolkit for Arch Linux...")

	installCmd := exec.Command("sudo", "pacman", "-S", "--noconfirm", "nvidia-container-toolkit")
	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("failed to install nvidia-container-toolkit: %w", err)
	}

	fmt.Println("Restarting Docker service...")
	restartCmd := exec.Command("sudo", "systemctl", "restart", "docker")
	restartCmd.Stdout = os.Stdout
	restartCmd.Stderr = os.Stderr
	restartCmd.Run()

	fmt.Println("✔ nvidia-container-toolkit installed successfully")
	return nil
}
