package setup

import (
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procShowWindow = windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow")

const swHide = 0

// hideOllamaWindow hides the window of Ollama's desktop app, which its
// installer opens when it finishes. The app keeps running in the tray (and
// keeps serving), it just doesn't pop up in front of the user; clicking the
// tray icon shows it again. Runs in the background for up to d.
func hideOllamaWindow(d time.Duration) {
	go func() {
		var pids map[uint32]bool
		cb := syscall.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
			var pid uint32
			if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err == nil && pids[pid] && windows.IsWindowVisible(hwnd) {
				procShowWindow.Call(uintptr(hwnd), swHide)
			}
			return 1 // continue enumerating
		})
		for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if pids = processIDs("ollama app.exe"); len(pids) > 0 {
				windows.EnumWindows(cb, nil)
			}
		}
	}()
}

// processIDs returns the IDs of running processes with the given executable name.
func processIDs(exe string) map[uint32]bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	pids := map[uint32]bool{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), exe) {
			pids[e.ProcessID] = true
		}
	}
	return pids
}
