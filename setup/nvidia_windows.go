package setup

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ─── Windows NVIDIA Docker Setup Integration ───

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

func printHeader() {
	fmt.Println(colorCyan + colorBold + `
╔══════════════════════════════════════════════════════════════╗
║     Docker NVIDIA GPU Installer für Windows (WSL2)           ║
║     Installiert NVIDIA Container Toolkit in WSL2             ║
╚══════════════════════════════════════════════════════════════╝` + colorReset)
	fmt.Println()
}

func info(msg string) {
	fmt.Printf(colorCyan+"[INFO]  "+colorReset+"%s\n", msg)
}

func success(msg string) {
	fmt.Printf(colorGreen+"[OK]    "+colorReset+"%s\n", msg)
}

func warn(msg string) {
	fmt.Printf(colorYellow+"[WARN]  "+colorReset+"%s\n", msg)
}

func fail(msg string) {
	fmt.Printf(colorRed+"[FAIL]  "+colorReset+"%s\n", msg)
}

func step(n int, total int, msg string) {
	fmt.Printf(colorBold+"\n[%d/%d] %s"+colorReset+"\n", n, total, msg)
	fmt.Println(strings.Repeat("─", 60))
}

func runPowershell(command string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func runPowershellInteractive(command string) error {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runWSL(args ...string) (string, error) {
	cmd := exec.Command("wsl", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func runWSLInteractive(args ...string) error {
	cmd := exec.Command("wsl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func prompt(msg string) bool {
	fmt.Printf(colorYellow+"\n%s [j/n]: "+colorReset, msg)
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))
	return input == "j" || input == "ja" || input == "y" || input == "yes"
}

// Prüft ob Administrator
func checkAdmin() bool {
	out, err := runPowershell(`([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)`)
	if err != nil {
		return false
	}
	return strings.ToLower(out) == "true"
}

// Prüft ob WSL2 installiert ist
func checkWSL2() bool {
	out, err := runPowershell(`(Get-WindowsOptionalFeature -Online -FeatureName Microsoft-Windows-Subsystem-Linux).State`)
	if err != nil {
		return false
	}
	wslEnabled := strings.Contains(out, "Enabled")

	out2, err2 := runPowershell(`(Get-WindowsOptionalFeature -Online -FeatureName VirtualMachinePlatform).State`)
	if err2 != nil {
		return false
	}
	vmEnabled := strings.Contains(out2, "Enabled")

	return wslEnabled && vmEnabled
}

// Prüft ob eine Linux-Distribution in WSL vorhanden ist
func checkWSLDistro() string {
	out, err := runWSL("--list", "--quiet")
	if err != nil || strings.TrimSpace(out) == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		// Null-Bytes entfernen (WSL gibt manchmal UTF-16 aus)
		clean := strings.ReplaceAll(line, "\x00", "")
		clean = strings.TrimSpace(clean)
		if clean != "" {
			return clean
		}
	}
	return ""
}

// Prüft ob Docker Desktop installiert ist
func checkDockerDesktop() bool {
	out, _ := runPowershell(`Get-Command docker -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Source`)
	return strings.Contains(out, "docker")
}

// Prüft NVIDIA Treiber
func checkNvidiaDriver() bool {
	_, err := exec.LookPath("nvidia-smi")
	if err != nil {
		// Auch in System32 suchen
		out, err2 := runPowershell(`& "C:\Windows\System32\nvidia-smi.exe" --query-gpu=name --format=csv,noheader 2>$null`)
		return err2 == nil && out != ""
	}
	cmd := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// Aktiviert WSL2-Features
func enableWSL2() error {
	info("Aktiviere WSL-Feature...")
	err := runPowershellInteractive(`Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Windows-Subsystem-Linux -NoRestart`)
	if err != nil {
		return fmt.Errorf("WSL-Feature konnte nicht aktiviert werden: %w", err)
	}

	info("Aktiviere Virtual Machine Platform...")
	err = runPowershellInteractive(`Enable-WindowsOptionalFeature -Online -FeatureName VirtualMachinePlatform -NoRestart`)
	if err != nil {
		return fmt.Errorf("VM Platform konnte nicht aktiviert werden: %w", err)
	}

	info("Setze WSL2 als Standard...")
	cmd := exec.Command("wsl", "--set-default-version", "2")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	return nil
}

// Installiert Ubuntu in WSL
func installUbuntu() error {
	info("Lade und installiere Ubuntu 22.04 LTS in WSL2...")
	warn("Dieser Vorgang kann einige Minuten dauern...")
	cmd := exec.Command("wsl", "--install", "-d", "Ubuntu-22.04")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// Installiert NVIDIA Container Toolkit in WSL
func installNvidiaContainerToolkit(distro string) error {
	info("Installiere NVIDIA Container Toolkit in WSL (" + distro + ")...")

	script := `
set -e
echo "==> Aktualisiere Paketlisten..."
sudo apt-get update -qq

echo "==> Installiere curl und gpg..."
sudo apt-get install -y curl gpg

echo "==> Füge NVIDIA GPG-Schlüssel hinzu..."
curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg

echo "==> Füge NVIDIA Repository hinzu..."
curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
  sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
  sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list

echo "==> Aktualisiere Paketlisten mit NVIDIA Repo..."
sudo apt-get update -qq

echo "==> Installiere nvidia-container-toolkit..."
sudo apt-get install -y nvidia-container-toolkit

echo "==> Konfiguriere NVIDIA Container Toolkit für Docker..."
sudo nvidia-ctk runtime configure --runtime=docker

echo "==> Starte Docker-Daemon neu..."
sudo service docker restart 2>/dev/null || true

echo ""
echo "NVIDIA Container Toolkit erfolgreich installiert!"
`

	return runWSLInteractive("-d", distro, "--", "bash", "-c", script)
}

// Testet GPU-Zugriff in Docker
func testGPU(distro string) {
	info("Teste NVIDIA GPU-Zugriff in Docker...")
	fmt.Println()

	err := runWSLInteractive("-d", distro, "--", "bash", "-c",
		`docker run --rm --gpus all nvidia/cuda:12.0.0-base-ubuntu22.04 nvidia-smi`)

	if err != nil {
		warn("Test fehlgeschlagen. Mögliche Ursachen:")
		fmt.Println("  • Docker Desktop ist nicht gestartet")
		fmt.Println("  • WSL2-Integration in Docker Desktop nicht aktiviert")
		fmt.Println("  • NVIDIA-Treiber nicht aktuell genug")
		fmt.Println("  • Das CUDA-Image wird noch heruntergeladen (erneut versuchen)")
	} else {
		success("GPU-Test erfolgreich! Docker kann auf die NVIDIA GPU zugreifen.")
	}
}

// Zeigt Konfigurationshinweise für Docker Desktop
func showDockerDesktopInstructions() {
	fmt.Println(colorCyan + colorBold + `
╔══════════════════════════════════════════════════════════════╗
║           Docker Desktop Konfiguration                        ║
╚══════════════════════════════════════════════════════════════╝` + colorReset)
	fmt.Println(`
Bitte stelle sicher, dass in Docker Desktop folgendes aktiviert ist:

  1. Docker Desktop öffnen
  2. Einstellungen → Resources → WSL Integration
  3. "Enable integration with my default WSL distro" aktivieren
  4. Deine WSL-Distribution auswählen und aktivieren
  5. "Apply & Restart" klicken

  6. Einstellungen → General
     → "Use the WSL 2 based engine" muss aktiviert sein

Nach dem Neustart von Docker Desktop sollte GPU-Unterstützung 
verfügbar sein.
`)
}

func showSummary(steps []string) {
	fmt.Println(colorGreen + colorBold + `
╔══════════════════════════════════════════════════════════════╗
║                    Zusammenfassung                            ║
╚══════════════════════════════════════════════════════════════╝` + colorReset)
	for _, s := range steps {
		fmt.Println("  ✓ " + s)
	}
	fmt.Println()
	fmt.Println(colorBold + "Schnelltest-Befehl (in WSL ausführen):" + colorReset)
	fmt.Println("  docker run --rm --gpus all nvidia/cuda:12.0.0-base-ubuntu22.04 nvidia-smi")
	fmt.Println()
}

func waitSeconds(n int) {
	for i := n; i > 0; i-- {
		fmt.Printf("\r  Warte %d Sekunden...   ", i)
		time.Sleep(1 * time.Second)
	}
	fmt.Println()
}

func SetupNvidiaDockerWindows() error {
	printHeader()

	// Betriebssystem prüfen
	if runtime.GOOS != "windows" {
		fail("Dieses Programm ist nur für Windows konzipiert!")
		return fmt.Errorf("this program is designed for Windows only")
	}

	var completedSteps []string
	totalSteps := 5

	// ─── Schritt 1: Voraussetzungen prüfen ───────────────────────────────────
	step(1, totalSteps, "Systemvoraussetzungen prüfen")

	if !checkAdmin() {
		fail("Programm muss als Administrator ausgeführt werden!")
		fmt.Println("  Rechtsklick → 'Als Administrator ausführen'")
		return fmt.Errorf("program must be run as Administrator")
	}
	success("Administratorrechte vorhanden")

	if !checkNvidiaDriver() {
		fail("NVIDIA-Treiber nicht gefunden!")
		fmt.Println("  Bitte installiere den aktuellen NVIDIA-Treiber:")
		fmt.Println("  https://www.nvidia.com/drivers")
		fmt.Println()
		if !prompt("Trotzdem fortfahren?") {
			return fmt.Errorf("aborted by user")
		}
	} else {
		success("NVIDIA-Treiber gefunden")
	}

	if !checkDockerDesktop() {
		warn("Docker Desktop nicht gefunden oder nicht im PATH")
		fmt.Println("  Bitte installiere Docker Desktop: https://www.docker.com/products/docker-desktop")
		fmt.Println()
		if !prompt("Trotzdem fortfahren?") {
			return fmt.Errorf("aborted by user")
		}
	} else {
		success("Docker Desktop gefunden")
	}

	// ─── Schritt 2: WSL2 aktivieren ──────────────────────────────────────────
	step(2, totalSteps, "WSL2 prüfen und aktivieren")

	wsl2Ready := checkWSL2()
	if wsl2Ready {
		success("WSL2 ist bereits aktiviert")
		completedSteps = append(completedSteps, "WSL2 bereits aktiv")
	} else {
		warn("WSL2-Features sind nicht vollständig aktiviert")
		if prompt("WSL2 jetzt aktivieren? (Neustart möglicherweise erforderlich)") {
			if err := enableWSL2(); err != nil {
				fail("Fehler beim Aktivieren von WSL2: " + err.Error())
				return fmt.Errorf("failed to enable WSL2: %w", err)
			}
			success("WSL2-Features aktiviert")
			completedSteps = append(completedSteps, "WSL2 aktiviert")
			warn("Ein Neustart könnte erforderlich sein. Starte das Programm danach erneut.")
		}
	}

	// WSL Update
	info("Aktualisiere WSL-Kernel...")
	_ = runPowershellInteractive(`wsl --update`)
	_ = exec.Command("wsl", "--set-default-version", "2").Run()

	// ─── Schritt 3: Linux-Distribution sicherstellen ─────────────────────────
	step(3, totalSteps, "WSL2 Linux-Distribution prüfen")

	distro := checkWSLDistro()
	if distro != "" {
		success("WSL-Distribution gefunden: " + distro)
		completedSteps = append(completedSteps, "WSL-Distribution: "+distro)
	} else {
		warn("Keine WSL-Distribution gefunden")
		if prompt("Ubuntu 22.04 LTS jetzt installieren?") {
			if err := installUbuntu(); err != nil {
				fail("Installation fehlgeschlagen: " + err.Error())
				warn("Versuche manuell: wsl --install -d Ubuntu-22.04")
			}
			waitSeconds(3)
			distro = checkWSLDistro()
			if distro == "" {
				distro = "Ubuntu-22.04"
			}
			success("Ubuntu installiert: " + distro)
			completedSteps = append(completedSteps, "Ubuntu 22.04 installiert")
		} else {
			fail("Keine WSL-Distribution verfügbar. Abbruch.")
			return fmt.Errorf("no WSL distribution available")
		}
	}

	// ─── Schritt 4: NVIDIA Container Toolkit installieren ────────────────────
	step(4, totalSteps, "NVIDIA Container Toolkit installieren")

	// Prüfen ob bereits installiert
	checkOut, _ := runWSL("-d", distro, "--", "bash", "-c",
		"dpkg -l nvidia-container-toolkit 2>/dev/null | grep -c '^ii' || echo 0")
	checkOut = strings.TrimSpace(checkOut)

	if checkOut == "1" {
		success("NVIDIA Container Toolkit ist bereits installiert")
		completedSteps = append(completedSteps, "NVIDIA Container Toolkit bereits installiert")

		if prompt("Trotzdem neu installieren/aktualisieren?") {
			if err := installNvidiaContainerToolkit(distro); err != nil {
				fail("Installation fehlgeschlagen: " + err.Error())
			} else {
				success("NVIDIA Container Toolkit aktualisiert")
				completedSteps = append(completedSteps, "NVIDIA Container Toolkit aktualisiert")
			}
		}
	} else {
		if prompt("NVIDIA Container Toolkit in WSL installieren?") {
			if err := installNvidiaContainerToolkit(distro); err != nil {
				fail("Installation fehlgeschlagen: " + err.Error())
				fmt.Println()
				warn("Versuche es manuell in WSL:")
				fmt.Println("  curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg")
				fmt.Println("  curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list")
				fmt.Println("  sudo apt-get update && sudo apt-get install -y nvidia-container-toolkit")
				fmt.Println("  sudo nvidia-ctk runtime configure --runtime=docker")
			} else {
				success("NVIDIA Container Toolkit erfolgreich installiert")
				completedSteps = append(completedSteps, "NVIDIA Container Toolkit installiert")
			}
		}
	}

	// ─── Schritt 5: Docker Desktop konfigurieren & testen ────────────────────
	step(5, totalSteps, "Docker Desktop konfigurieren & GPU testen")

	showDockerDesktopInstructions()

	if prompt("GPU-Zugriff in Docker jetzt testen?") {
		warn("Stelle sicher, dass Docker Desktop läuft und WSL-Integration aktiviert ist!")
		fmt.Println()
		testGPU(distro)
		completedSteps = append(completedSteps, "GPU-Test durchgeführt")
	}

	// ─── Zusammenfassung ──────────────────────────────────────────────────────
	showSummary(completedSteps)

	fmt.Println(colorBold + "Nützliche Links:" + colorReset)
	fmt.Println("  • NVIDIA Container Toolkit Doku: https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html")
	fmt.Println("  • Docker Desktop WSL2 Backend:   https://docs.docker.com/desktop/windows/wsl/")
	fmt.Println("  • NVIDIA Treiber:                https://www.nvidia.com/drivers")
	fmt.Println()

	fmt.Print("Drücke Enter zum Beenden...")
	bufio.NewReader(os.Stdin).ReadString('\n')
	return nil
}
