// Package ui draws a single animated status line (spinner, progress bars,
// elapsed time) at the bottom of the terminal. Regular output printed through
// Printf/Println/Writer appears above it without being garbled.
//
// When stdout is not a terminal (piped, redirected to a file) animations are
// turned off and only start/finish messages are printed.
package ui

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/term"
)

var (
	mu       sync.Mutex
	out      io.Writer = os.Stdout
	errOut   io.Writer = os.Stderr
	isTTY              = term.IsTerminal(int(os.Stdout.Fd()))
	active   *Status
	drawnLen int // runes of the status line currently on screen
	widthFn  = func() int {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			return w
		}
		return 80
	}
)

// Spinner frames: braille dots, or plain ASCII where console fonts lack them.
var frames = func() []string {
	if runtime.GOOS == "windows" {
		return []string{"|", "/", "-", "\\"}
	}
	return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
}()

// Interactive reports whether animations are shown.
func Interactive() bool { return isTTY }

// Status is an animated status line. Only one is visible at a time.
type Status struct {
	start time.Time
	tasks []*Task
	frame int
	stop  chan struct{}
	done  bool
}

// Task is one part of a status line, e.g. "Speech model: downloading 40%".
// Several tasks share a line when work runs in parallel.
type Task struct {
	st          *Status
	name, text  string
	done, total int64
	unit        string
}

// Start shows a new status line with a spinner. It replaces any status that
// is already shown.
func Start(text string) *Status {
	mu.Lock()
	defer mu.Unlock()
	if active != nil {
		active.finishLocked()
	}
	s := &Status{start: time.Now(), stop: make(chan struct{})}
	s.tasks = []*Task{{st: s, text: text}}
	active = s
	if isTTY {
		s.drawLocked()
		go s.animate()
	} else if text != "" {
		fmt.Fprintln(out, text+"...")
	}
	return s
}

func (s *Status) animate() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			mu.Lock()
			if active == s {
				s.frame++
				s.drawLocked()
			}
			mu.Unlock()
		}
	}
}

// Set changes the status text.
func (s *Status) Set(text string) { s.tasks[0].Set(text) }

// Progress shows a progress bar; total <= 0 removes it. unit "B" formats bytes.
func (s *Status) Progress(done, total int64, unit string) { s.tasks[0].Progress(done, total, unit) }

// Task adds a named part to the status line, for work running in parallel.
func (s *Status) Task(name string) *Task {
	mu.Lock()
	defer mu.Unlock()
	t := &Task{st: s, name: name}
	s.tasks = append(s.tasks, t)
	s.redrawLocked()
	return t
}

// Done removes the status line and prints msg (if not empty) in its place.
func (s *Status) Done(msg string) {
	mu.Lock()
	defer mu.Unlock()
	if s.done {
		return
	}
	s.finishLocked()
	if msg != "" {
		fmt.Fprintln(out, msg)
	}
}

// Stop removes the status line.
func (s *Status) Stop() { s.Done("") }

func (s *Status) finishLocked() {
	if s.done {
		return
	}
	s.done = true
	close(s.stop)
	if active == s {
		clearLocked()
		active = nil
	}
}

// Set changes the task's text.
func (t *Task) Set(text string) {
	mu.Lock()
	defer mu.Unlock()
	if t.text == text {
		return
	}
	t.text = text
	t.st.redrawLocked()
}

// Progress shows a progress bar for the task; total <= 0 removes it.
func (t *Task) Progress(done, total int64, unit string) {
	mu.Lock()
	defer mu.Unlock()
	if total <= 0 {
		done, total = 0, 0
	}
	t.done, t.total, t.unit = done, total, unit
	t.st.redrawLocked()
}

// Done removes the task from its status line and prints msg (if not empty).
func (t *Task) Done(msg string) {
	mu.Lock()
	defer mu.Unlock()
	s := t.st
	for i, x := range s.tasks {
		if x == t && i > 0 {
			s.tasks = append(s.tasks[:i:i], s.tasks[i+1:]...)
			break
		}
	}
	if msg != "" {
		printLocked(out, msg+"\n")
	} else {
		s.redrawLocked()
	}
}

func (s *Status) redrawLocked() {
	if active == s && isTTY {
		s.drawLocked()
	}
}

func (s *Status) drawLocked() {
	line := s.render(widthFn() - 1) // never fill the last column: avoids auto-wrap
	n := utf8.RuneCountInString(line)
	pad := ""
	if drawnLen > n {
		pad = strings.Repeat(" ", drawnLen-n)
	}
	// Overwrite in place; the next draw or clear starts again with \r.
	fmt.Fprint(out, "\r"+line+pad)
	drawnLen = n + len(pad)
}

func clearLocked() {
	if drawnLen > 0 {
		fmt.Fprint(out, "\r"+strings.Repeat(" ", drawnLen)+"\r")
		drawnLen = 0
	}
}

// render builds the status line, fitted into width runes.
func (s *Status) render(width int) string {
	var parts []string
	for _, t := range s.tasks {
		if r := t.render(); r != "" {
			parts = append(parts, r)
		}
	}
	body := strings.Join(parts, "  │  ")
	prefix := frames[s.frame%len(frames)] + " "
	suffix := ""
	if el := time.Since(s.start); el >= 2*time.Second {
		suffix = "  " + formatDuration(el)
	}
	room := width - utf8.RuneCountInString(prefix) - utf8.RuneCountInString(suffix)
	return prefix + truncate(body, room) + suffix
}

func (t *Task) render() string {
	text := t.text
	if t.name != "" {
		if text != "" {
			text = t.name + ": " + text
		} else {
			text = t.name
		}
	}
	if t.total > 0 {
		if text != "" {
			text += " "
		}
		text += Bar(t.done, t.total, 20) + " " + amount(t.done, t.total, t.unit)
	}
	return text
}

// Bar renders a progress bar like [██████░░░░] 60%.
func Bar(done, total int64, width int) string {
	if done > total {
		done = total
	}
	if done < 0 {
		done = 0
	}
	filled := int(int64(width) * done / total)
	pct := int(100 * done / total)
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + fmt.Sprintf("] %3d%%", pct)
}

func amount(done, total int64, unit string) string {
	switch unit {
	case "B":
		return humanBytes(done) + " / " + humanBytes(total)
	case "":
		return fmt.Sprintf("%d / %d", done, total)
	}
	return fmt.Sprintf("%d / %d %s", done, total, unit)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func truncate(s string, room int) string {
	if room <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= room {
		return s
	}
	r := []rune(s)
	if room == 1 {
		return "…"
	}
	return string(r[:room-1]) + "…"
}

// printLocked writes text above the status line and redraws the status.
func printLocked(w io.Writer, text string) {
	if active != nil && isTTY {
		clearLocked()
		fmt.Fprint(w, text)
		active.drawLocked()
		return
	}
	fmt.Fprint(w, text)
}

// Printf prints formatted text above the status line. Text printed while a
// status is shown should end with a newline.
func Printf(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	printLocked(out, fmt.Sprintf(format, args...))
}

// Println prints a line above the status line.
func Println(args ...any) {
	mu.Lock()
	defer mu.Unlock()
	printLocked(out, fmt.Sprintln(args...))
}

type stderrWriter struct{}

func (stderrWriter) Write(p []byte) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	printLocked(errOut, string(p))
	return len(p), nil
}

// Writer returns a writer (for log.SetOutput) that prints to stderr above the status line.
func Writer() io.Writer { return stderrWriter{} }
