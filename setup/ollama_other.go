//go:build !windows

package setup

import "time"

// hideOllamaWindow is only needed on Windows, where the installer opens Ollama's app.
func hideOllamaWindow(time.Duration) {}
