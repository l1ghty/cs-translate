// Package hotkey provides global hotkey detection using Linux evdev.
package hotkey

import (
	"context"
)

// Key codes (Linux evdev KEY_* constants)
const (
	KeyF1  = 59
	KeyF2  = 60
	KeyF3  = 61
	KeyF4  = 62
	KeyF5  = 63
	KeyF6  = 64
	KeyF7  = 65
	KeyF8  = 66
	KeyF9  = 67
	KeyF10 = 68
	KeyF11 = 87
	KeyF12 = 88
)

// Listener watches for a specific key press and sends on a channel.
type Listener struct {
	keyChan chan struct{}
	keyCode uint16
}

// NewListener creates a hotkey listener for the given key code.
func NewListener(keyCode uint16) *Listener {
	return &Listener{
		keyChan: make(chan struct{}, 1),
		keyCode: keyCode,
	}
}

// KeyPressed returns a channel that receives a value each time the hotkey is pressed.
func (l *Listener) KeyPressed() <-chan struct{} {
	return l.keyChan
}

// Start begins listening for the hotkey. It blocks until the context is cancelled.
// Call this in a goroutine.
func (l *Listener) Start(ctx context.Context) error {
	return l.listen(ctx)
}
