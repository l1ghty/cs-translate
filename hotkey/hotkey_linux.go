//go:build linux

package hotkey

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

// inputEvent matches the Linux struct input_event layout.
//
//	struct input_event {
//	    struct timeval time;  // 16 bytes on 64-bit (8 sec + 8 usec)
//	    __u16 type;
//	    __u16 code;
//	    __s32 value;
//	};
type inputEvent struct {
	TimeSec  int64
	TimeUsec int64
	Type     uint16
	Code     uint16
	Value    int32
}

const (
	evKey     = 1 // EV_KEY
	keyPress  = 1 // key down
	inputSize = int(unsafe.Sizeof(inputEvent{}))
)

// findKeyboardDevices returns paths to keyboard event devices.
func findKeyboardDevices() ([]string, error) {
	matches, err := filepath.Glob("/dev/input/event*")
	if err != nil {
		return nil, err
	}

	var keyboards []string
	for _, dev := range matches {
		// Check if this device is a keyboard by reading its name from /sys
		base := filepath.Base(dev)
		namePath := filepath.Join("/sys/class/input", base, "device/name")
		nameBytes, err := os.ReadFile(namePath)
		if err != nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(string(nameBytes)))
		// Look for devices that are likely keyboards
		if strings.Contains(name, "keyboard") || strings.Contains(name, "kbd") {
			keyboards = append(keyboards, dev)
		}
	}

	if len(keyboards) == 0 {
		// Fallback: try all event devices
		return matches, nil
	}

	return keyboards, nil
}

func (l *Listener) listen(ctx context.Context) error {
	devices, err := findKeyboardDevices()
	if err != nil {
		return fmt.Errorf("failed to find keyboard devices: %w", err)
	}

	if len(devices) == 0 {
		return fmt.Errorf("no input devices found in /dev/input/")
	}

	log.Printf("Hotkey listener: monitoring %d device(s) for F9 key", len(devices))

	// Open all keyboard devices and multiplex
	type devReader struct {
		file *os.File
		name string
	}
	var readers []devReader

	for _, dev := range devices {
		f, err := os.Open(dev)
		if err != nil {
			log.Printf("Hotkey: cannot open %s: %v (need root or 'input' group)", dev, err)
			continue
		}
		readers = append(readers, devReader{file: f, name: dev})
	}

	if len(readers) == 0 {
		return fmt.Errorf("could not open any input devices. Run as root or add your user to the 'input' group: sudo usermod -aG input $USER")
	}

	defer func() {
		for _, r := range readers {
			r.file.Close()
		}
	}()

	// Start a goroutine for each device; all send to the same channel
	eventChan := make(chan struct{}, 1)
	for _, r := range readers {
		go func(f *os.File) {
			buf := make([]byte, inputSize)
			for {
				n, err := f.Read(buf)
				if err != nil {
					return
				}
				if n < inputSize {
					continue
				}

				var ev inputEvent
				ev.Type = binary.LittleEndian.Uint16(buf[16:18])
				ev.Code = binary.LittleEndian.Uint16(buf[18:20])
				ev.Value = int32(binary.LittleEndian.Uint32(buf[20:24]))

				if ev.Type == evKey && ev.Code == l.keyCode && ev.Value == keyPress {
					select {
					case eventChan <- struct{}{}:
					default:
					}
				}
			}
		}(r.file)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-eventChan:
			select {
			case l.keyChan <- struct{}{}:
			default:
			}
		}
	}
}
