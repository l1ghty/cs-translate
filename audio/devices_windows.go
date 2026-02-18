//go:build windows
// +build windows

package audio

// getPlatformDevices returns the virtual-audio-capturer device on Windows
func getPlatformDevices() ([]string, error) {
	// virtual-audio-capturer from screen-capture-recorder
	// https://github.com/rdp/screen-capture-recorder-to-video-windows-free
	return []string{"virtual-audio-capturer"}, nil
}

// GetDefaultDeviceName returns the default audio device name for Windows
func GetDefaultDeviceName() string {
	return "virtual-audio-capturer"
}

// GetDeviceHelpText returns platform-specific help for device selection
func GetDeviceHelpText() string {
	return `Windows Audio Device Selection:
- Uses virtual-audio-capturer from screen-capture-recorder
- Requires: https://github.com/rdp/screen-capture-recorder-to-video-windows-free
- Install screen-capture-recorder and the virtual audio device will be available
- No device selection needed - uses virtual-audio-capturer by default
`
}
