//go:build windows

package ui

import (
	"os"

	"golang.org/x/sys/windows"
)

// Enable ANSI escape sequences (used for colours) in classic Windows consoles.
func init() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		h := windows.Handle(f.Fd())
		var mode uint32
		if windows.GetConsoleMode(h, &mode) == nil {
			windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
		}
	}
}
