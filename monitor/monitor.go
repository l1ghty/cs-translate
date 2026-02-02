package monitor

import (
	"fmt"
	"io"

	"github.com/nxadm/tail"
)

// Monitor watches a file for new lines
type Monitor struct {
	filePath string
	tail     *tail.Tail
}

// NewMonitor creates a new file monitor
func NewMonitor(filePath string) (*Monitor, error) {
	t, err := tail.TailFile(filePath, tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: false,
		Poll:      true, // Polling might be necessary for some setups, especially if file is rotated/recreated
		Location:  &tail.SeekInfo{Offset: 0, Whence: io.SeekEnd},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to tail file: %w", err)
	}

	return &Monitor{
		filePath: filePath,
		tail:     t,
	}, nil
}

// Lines returns the channel of new lines
func (m *Monitor) Lines() chan *tail.Line {
	return m.tail.Lines
}

// Stop stops the monitor
func (m *Monitor) Stop() {
	m.tail.Cleanup()
	m.tail.Stop()
}

// CreateDummyFile creates a dummy log file for testing
func CreateDummyFile(path string) error {
	// This is a helper for testing
	// Implementation deferred
	return nil
}
