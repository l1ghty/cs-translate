// Package audio provides cross-platform audio capture functionality
// using FFmpeg for all platforms.
package audio

// GetAvailableDevices returns a list of available audio devices
func GetAvailableDevices() ([]string, error) {
	return getPlatformDevices()
}
