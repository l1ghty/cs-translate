package ui

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func fakeTerminal(t *testing.T, width int) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldErr, oldTTY, oldW := out, errOut, isTTY, widthFn
	out, errOut, isTTY = &buf, &buf, true
	widthFn = func() int { return width }
	t.Cleanup(func() {
		if active != nil {
			active.Stop()
		}
		out, errOut, isTTY, widthFn = oldOut, oldErr, oldTTY, oldW
		drawnLen = 0
	})
	return &buf
}

func TestBar(t *testing.T) {
	if got := Bar(5, 10, 10); got != "[█████░░░░░]  50%" {
		t.Errorf("Bar = %q", got)
	}
	if got := Bar(20, 10, 4); got != "[████] 100%" {
		t.Errorf("overfull Bar = %q", got)
	}
}

func TestRenderFitsWidth(t *testing.T) {
	fakeTerminal(t, 40)
	s := Start("Loading a model with a really long name that cannot fit")
	task := s.Task("Speech")
	task.Progress(50<<20, 100<<20, "B")
	line := s.render(39)
	if n := utf8.RuneCountInString(line); n > 39 {
		t.Errorf("line has %d runes > 39: %q", n, line)
	}
	if !strings.HasSuffix(line, "…") {
		t.Errorf("expected truncation: %q", line)
	}
}

func TestTaskRender(t *testing.T) {
	fakeTerminal(t, 200)
	s := Start("")
	a := s.Task("Translation model")
	a.Set("loading")
	b := s.Task("Speech model")
	b.Set("downloading")
	b.Progress(1<<20, 4<<20, "B")
	line := s.render(199)
	for _, want := range []string{"Translation model: loading", "Speech model: downloading [", "25%", "1.0 MB / 4.0 MB"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %q", want, line)
		}
	}
	a.Done("ok")
	if line := s.render(199); strings.Contains(line, "Translation") {
		t.Errorf("finished task still shown: %q", line)
	}
}

func TestPrintAboveStatus(t *testing.T) {
	buf := fakeTerminal(t, 80)
	s := Start("Working")
	buf.Reset()
	Println("hello")
	got := buf.String()
	// cleared status, the line, then the status redrawn
	if !strings.HasPrefix(got, "\r") || !strings.Contains(got, "hello\n") || !strings.HasSuffix(strings.TrimRight(got, " "), "Working") {
		t.Errorf("unexpected output %q", got)
	}
	buf.Reset()
	s.Done("finished")
	if got := buf.String(); !strings.HasSuffix(got, "\rfinished\n") {
		t.Errorf("Done output %q", got)
	}
	if active != nil {
		t.Error("status still active")
	}
}

func TestNonInteractive(t *testing.T) {
	buf := fakeTerminal(t, 80)
	isTTY = false
	s := Start("Downloading")
	s.Progress(1, 2, "B")
	s.Set("other")
	s.Done("✔ done")
	if got := buf.String(); got != "Downloading...\n✔ done\n" {
		t.Errorf("non-interactive output %q", got)
	}
}
