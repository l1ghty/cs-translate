package setup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

func PrintManualInstallInstructions(pkg string) {
	if pkg == "python" {
		fmt.Println("Please install Python 3.9+ from python.org")
	}
}

func InstallDependency(scanner *bufio.Scanner, pkgName string) error {
	pm, cmdArgs := detectPackageManager(pkgName)
	if pm == "" {
		return fmt.Errorf("no supported package manager found")
	}

	fmt.Printf("Package manager '%s' detected.\n", pm)
	fmt.Printf("Do you want to install '%s' using %s? [Y/n]: ", pkgName, pm)
	fmt.Scanln()

	fmt.Printf("Running: %s %s\n", pm, cmdArgs)
	cmd := exec.Command(pm, cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}

func detectPackageManager(pkgName string) (string, []string) {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("winget"); err == nil {
			id := pkgName
			if pkgName == "python" {
				id = "Python.Python.3.11"
			}
			return "winget", []string{"install", "-e", "--id", id}
		}
		if _, err := exec.LookPath("choco"); err == nil {
			return "choco", []string{"install", pkgName, "-y"}
		}
		if _, err := exec.LookPath("scoop"); err == nil {
			return "scoop", []string{"install", pkgName}
		}
	} else {
		if _, err := exec.LookPath("apt-get"); err == nil {
			return "sudo", []string{"apt-get", "install", "-y", pkgName}
		}
		if _, err := exec.LookPath("dnf"); err == nil {
			return "sudo", []string{"dnf", "install", "-y", pkgName}
		}
		if _, err := exec.LookPath("pacman"); err == nil {
			target := pkgName
			return "sudo", []string{"pacman", "-S", "--noconfirm", target}
		}
		if _, err := exec.LookPath("zypper"); err == nil {
			return "sudo", []string{"zypper", "install", "-y", pkgName}
		}
	}
	return "", nil
}
