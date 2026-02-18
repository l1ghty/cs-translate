//go:build !windows
// +build !windows

package audio

import (
	"os/exec"
	"strings"
)

// getPlatformDevices returns available audio devices on Linux/macOS
func getPlatformDevices() ([]string, error) {
	// Try PulseAudio first
	out, err := exec.Command("pactl", "list", "sources", "short").Output()
	if err == nil {
		var devices []string
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				devices = append(devices, parts[1])
			}
		}
		if len(devices) > 0 {
			return devices, nil
		}
	}

	// Fallback: try to list via ffmpeg
	return listFFmpegDevices()
}

// GetDefaultDeviceName returns the default audio device name for Linux/macOS
func GetDefaultDeviceName() string {
	return "default"
}

// GetDeviceHelpText returns platform-specific help for device selection
func GetDeviceHelpText() string {
	return `Linux/macOS Audio Device Selection:
- Use -audiodevice flag to specify a device name
- On Linux: Uses PulseAudio/PipeWire monitor sources
- Common format: <sink_name>.monitor (e.g., "alsa_output.pci-0000_00_1b.0.analog-stereo.monitor")
- Run 'pactl list sources short' to see available sources
`
}

func listFFmpegDevices() ([]string, error) {
	cmd := exec.Command("ffmpeg", "-list_devices", "true", "-f", "pulse", "-i", "dummy")
	out, _ := cmd.CombinedOutput()

	var devices []string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if strings.Contains(line, "Monitor") {
			// Extract device name from ffmpeg output
			parts := strings.Split(line, "\"")
			if len(parts) >= 2 {
				devices = append(devices, parts[1])
			}
		}
	}

	return devices, nil
}
