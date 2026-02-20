//go:build !linux && !windows

package hotkey

import (
	"context"
	"fmt"
)

func (l *Listener) listen(ctx context.Context) error {
	return fmt.Errorf("hotkey listener is not supported on this platform")
}
