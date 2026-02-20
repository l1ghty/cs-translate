//go:build windows

package hotkey

import (
	"context"
	"fmt"
)

func (l *Listener) listen(ctx context.Context) error {
	return fmt.Errorf("hotkey listener is not yet supported on Windows; use -voice with continuous mode or run on Linux")
}
