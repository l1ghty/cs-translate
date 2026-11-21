package overlay

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func start(t *testing.T, lan bool) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := Start(ctx, Options{LAN: lan})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(t *testing.T, url, host string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// readEvent returns the next "data:" event from an SSE stream.
func readEvent(t *testing.T, r *bufio.Reader) Event {
	t.Helper()
	done := make(chan Event, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				var e Event
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e)
				done <- e
				return
			}
		}
	}()
	select {
	case e := <-done:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

func TestPageAndEvents(t *testing.T) {
	s := start(t, false)
	base := s.URLs()[0]
	if resp := get(t, base, ""); resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("page: %d %v", resp.StatusCode, resp.Header)
	}
	if resp := get(t, base+"nope", ""); resp.StatusCode != 404 {
		t.Errorf("unknown path: %d", resp.StatusCode)
	}

	resp := get(t, base+"events", "")
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	r := bufio.NewReader(resp.Body)
	time.Sleep(100 * time.Millisecond) // subscribed
	s.Publish(Event{Type: "chat", Name: "l1ght", Team: "T", Original: "privet", Translation: "hello"})
	e := readEvent(t, r)
	if e.Type != "chat" || e.Name != "l1ght" || e.Translation != "hello" || e.Time == 0 {
		t.Errorf("event = %+v", e)
	}
}

func TestReplayForNewViewersWithAge(t *testing.T) {
	s := start(t, false)
	for i := 0; i < replay+5; i++ {
		s.Publish(Event{Type: "voice", Original: "line", Speaker: i + 1})
	}
	s.Publish(Event{Type: "hearing", Original: "live"}) // never replayed
	time.Sleep(20 * time.Millisecond)
	resp := get(t, s.URLs()[0]+"events", "")
	r := bufio.NewReader(resp.Body)
	first := readEvent(t, r)
	if first.Speaker != 6 || first.Age < 1 || first.Type != "voice" { // oldest of the last `replay`
		t.Errorf("first replayed = %+v", first)
	}
}

func TestRejectsOtherHosts(t *testing.T) {
	s := start(t, false)
	if resp := get(t, s.URLs()[0]+"events", "evil.example.com"); resp.StatusCode != 403 {
		t.Errorf("rebinding host got %d", resp.StatusCode)
	}
	if resp := get(t, s.URLs()[0], "localhost"); resp.StatusCode != 200 {
		t.Errorf("localhost got %d", resp.StatusCode)
	}
}

func TestLANNeedsKey(t *testing.T) {
	s := start(t, true)
	if s.token == "" || len(s.token) < 12 {
		t.Fatalf("token %q", s.token)
	}
	base := strings.SplitN(s.URLs()[0], "/?k=", 2)[0]
	if resp := get(t, base+"/", ""); resp.StatusCode != 403 {
		t.Errorf("no key got %d", resp.StatusCode)
	}
	if resp := get(t, base+"/events?k=wrong", ""); resp.StatusCode != 403 {
		t.Errorf("wrong key got %d", resp.StatusCode)
	}
	if resp := get(t, s.URLs()[0], ""); resp.StatusCode != 200 {
		t.Errorf("right key got %d", resp.StatusCode)
	}
	if !strings.Contains(s.URLs()[0], "k="+s.token) {
		t.Error("URL lacks the key")
	}
}

func TestPublishNeverBlocksAndNilIsSafe(t *testing.T) {
	var none *Server
	none.Publish(Event{Type: "chat"})
	none.Close()

	s := start(t, false)
	resp := get(t, s.URLs()[0]+"events", "")
	_ = resp // a viewer that never reads
	time.Sleep(50 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5000; i++ {
			s.Publish(Event{Type: "voice", Original: strings.Repeat("x", 200)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow viewer")
	}
}
