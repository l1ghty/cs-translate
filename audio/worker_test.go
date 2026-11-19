package audio

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAutoSettingsLeaveCoresForTheGame(t *testing.T) {
	for _, tc := range []struct {
		cpus    int
		model   string
		threads int
	}{{2, "base", 1}, {4, "base", 1}, {6, "small", 2}, {8, "small", 2}, {12, "small", 3}, {32, "small", 4}} {
		if got := AutoModel("cpu", tc.cpus); got != tc.model {
			t.Errorf("AutoModel(cpu,%d) = %s, want %s", tc.cpus, got, tc.model)
		}
		if got := AutoThreads(tc.cpus); got != tc.threads {
			t.Errorf("AutoThreads(%d) = %d, want %d", tc.cpus, got, tc.threads)
		}
		if got := AutoThreads(tc.cpus); got*2 > tc.cpus && tc.cpus > 2 {
			t.Errorf("AutoThreads(%d) = %d uses more than half the cores", tc.cpus, got)
		}
	}
	if got := AutoModel("cuda", 4); got != "large-v3-turbo" {
		t.Errorf("AutoModel(cuda) = %s", got)
	}
}

func TestResolveDefaults(t *testing.T) {
	c := Config{Device: "gpu"}.Resolve()
	if c.Device != "cuda" || c.Model != "large-v3-turbo" || c.Threads < 1 || c.BufferSecs != 30 {
		t.Errorf("unexpected resolve: %+v", c)
	}
	c = Config{Model: "tiny", Threads: 3}.Resolve()
	if c.Device != "cpu" || c.Model != "tiny" || c.Threads != 3 {
		t.Errorf("explicit settings changed: %+v", c)
	}
}

// fakeUV writes a script that stands in for "uv run ... transcriber.py": it
// prints uv-style install lines on stderr and speaks the worker protocol.
func fakeUV(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "uv")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStartReportsProgressAndJobs(t *testing.T) {
	uv := fakeUV(t, `
echo "Downloading numpy (15.0MiB)" >&2
# Like real uv: install output comes well before Python starts. stdout and
# stderr are read concurrently, so without a pause the order is a coin toss.
sleep 0.3
echo '{"event":"status","message":"Downloading Whisper model"}'
echo '{"event":"progress","message":"Downloading Whisper model","done":50,"total":100,"unit":"B"}'
echo "[speech] loading" >&2
echo '{"event":"ready","model":"tiny","device":"cpu","compute_type":"int8","threads":1,"audio_device":"x"}'
while read cmd arg; do
  case "$cmd" in
  capture)
    echo '{"event":"job_start","source":"capture","pieces":2}'
    echo '{"event":"transcript","source":"capture","text":"privet","language":"ru"}'
    echo '{"event":"job_done","source":"capture","found":true}';;
  quit) exit 0;;
  esac
done
`)
	var mu sync.Mutex
	var got []Progress
	cfg := Config{UV: uv, DataDir: t.TempDir(), Model: "tiny", Progress: func(p Progress) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}}
	w, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.Info.Model != "tiny" || w.Info.AudioDevice != "x" {
		t.Errorf("info = %+v", w.Info)
	}

	mu.Lock()
	var msgs []string
	sawBar := false
	for _, p := range got {
		msgs = append(msgs, p.Message)
		if p.Total == 100 && p.Done == 50 && p.Unit == "B" {
			sawBar = true
		}
	}
	mu.Unlock()
	joined := strings.Join(msgs, "|")
	if !strings.Contains(joined, "Installing speech engine: Downloading numpy (15.0MiB)") ||
		!strings.Contains(joined, "Downloading Whisper model") || !sawBar {
		t.Errorf("progress = %q (bar %v)", joined, sawBar)
	}

	if err := w.Capture(15); err != nil {
		t.Fatal(err)
	}
	want := []Kind{KindStart, KindText, KindDone}
	for i, k := range want {
		select {
		case tr := <-w.Transcripts():
			if tr.Kind != k {
				t.Fatalf("event %d: kind %v, want %v (%+v)", i, tr.Kind, k, tr)
			}
			if k == KindStart && tr.Pieces != 2 || k == KindText && tr.Text != "privet" || k == KindDone && !tr.Found {
				t.Errorf("event %d: %+v", i, tr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for transcript events")
		}
	}
}

func TestStartFailureShowsLastMessages(t *testing.T) {
	uv := fakeUV(t, `
echo "error: Failed to download numpy" >&2
exit 2
`)
	_, err := Start(context.Background(), Config{UV: uv, DataDir: t.TempDir(), Model: "tiny"})
	if err == nil || !strings.Contains(err.Error(), "Failed to download numpy") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandPassesVoiceInterval(t *testing.T) {
	has := func(c Config) bool {
		cmd, err := c.command(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(strings.Join(cmd.Args, " "), "--max-speech-seconds 7")
	}
	c := Config{UV: "uv", DataDir: t.TempDir(), Model: "tiny", MaxSpeechSecs: 7}
	if has(c) {
		t.Error("interval passed although live mode is off")
	}
	c.Live = true
	if !has(c) {
		t.Error("interval not passed in live mode")
	}
}
