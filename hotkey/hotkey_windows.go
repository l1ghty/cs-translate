//go:build windows

package hotkey

import (
	"context"
	"fmt"
	"time"

	"github.com/moutend/go-hook/pkg/keyboard"
	"github.com/moutend/go-hook/pkg/types"
)

// Map Linux evdev key codes to Windows virtual key codes (approximate)
func mapLinuxToWindows(linuxCode uint16) types.VKCode {
	switch linuxCode {
	case KeyF1:
		return types.VK_F1
	case KeyF2:
		return types.VK_F2
	case KeyF3:
		return types.VK_F3
	case KeyF4:
		return types.VK_F4
	case KeyF5:
		return types.VK_F5
	case KeyF6:
		return types.VK_F6
	case KeyF7:
		return types.VK_F7
	case KeyF8:
		return types.VK_F8
	case KeyF9:
		return types.VK_F9
	case KeyF10:
		return types.VK_F10
	case KeyF11:
		return types.VK_F11
	case KeyF12:
		return types.VK_F12
	default:
		// Default fallback if unknown, or handle appropriately
		return types.VK_F9
	}
}

func (l *Listener) listen(ctx context.Context) error {
	// Create channel for keyboard events
	keyboardChan := make(chan types.KeyboardEvent, 100)

	// Install hook
	if err := keyboard.Install(nil, keyboardChan); err != nil {
		return fmt.Errorf("failed to install keyboard hook: %w", err)
	}
	defer keyboard.Uninstall()

	targetVK := mapLinuxToWindows(l.keyCode)

	// Keep processing events until context is cancelled
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-keyboardChan:
			// Check for key down event (WM_KEYDOWN = 0x0100, WM_SYSKEYDOWN = 0x0104) and matching key code
			if (event.Message == types.WM_KEYDOWN || event.Message == types.WM_SYSKEYDOWN) && event.VKCode == targetVK {
				// Non-blocking send to keyChan
				select {
				case l.keyChan <- struct{}{}:
					// Simple debounce to prevent rapid firing if key is held down
					// In a real loop we might want to track key state, but for F9 trigger this is usually fine
					time.Sleep(300 * time.Millisecond)
				default:
				}
			}
		}
	}
}
