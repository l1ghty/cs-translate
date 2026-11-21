// Package overlay serves what cs-translate hears and reads as a small web
// page plus a live event stream, so translations can be shown somewhere other
// than the terminal: on a phone propped under the monitor, on a second
// screen, or as an OBS / overlay-tool "browser source" with a transparent
// background. Lines fade out by themselves a few seconds after they appear.
//
// The server only listens on this PC (127.0.0.1) unless LAN access is asked
// for; then every request needs a random key that is part of the printed URL,
// because the page shows the game's chat.
package overlay

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var page []byte

// Event is one line for the overlay.
type Event struct {
	Type        string `json:"type"`          // "chat", "voice" or "hearing"
	Time        int64  `json:"time"`          // unix milliseconds
	Age         int64  `json:"age,omitempty"` // ms since it happened, set when a line is replayed (viewers' clocks differ)
	Name        string `json:"name,omitempty"`
	Team        string `json:"team,omitempty"`
	Dead        bool   `json:"dead,omitempty"`
	Speaker     int    `json:"speaker,omitempty"`
	Language    string `json:"language,omitempty"`
	Original    string `json:"original,omitempty"`
	Translation string `json:"translation,omitempty"`
}

// replay is how many recent events a newly connected viewer gets, so a phone
// that reconnects doesn't miss what was just said.
const replay = 10

// Server is a running overlay server. A nil *Server ignores Publish.
type Server struct {
	srv   *http.Server
	ln    net.Listener
	token string
	hosts map[string]bool // Host headers (without port) that are accepted

	mu   sync.Mutex
	subs map[chan []byte]struct{}
	ring []Event
}

// Options configure Start.
type Options struct {
	Port int  // 0 = pick a free port
	LAN  bool // listen on all interfaces and require a key, instead of this PC only
}

// Start begins serving until ctx is done.
func Start(ctx context.Context, o Options) (*Server, error) {
	host := "127.0.0.1"
	if o.LAN {
		host = "0.0.0.0"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(o.Port)))
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, subs: map[chan []byte]struct{}{},
		hosts: map[string]bool{"localhost": true, "127.0.0.1": true, "[::1]": true}}
	if o.LAN {
		s.token = randomKey()
		for _, ip := range LANAddrs() {
			s.hosts[ip] = true
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.guard(s.servePage))
	mux.HandleFunc("/events", s.guard(s.serveEvents))
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go s.srv.Serve(ln)
	go func() {
		<-ctx.Done()
		s.Close()
	}()
	return s, nil
}

// Close stops the server and disconnects viewers.
func (s *Server) Close() {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		s.srv.Close()
	}
}

// Port is the TCP port being served.
func (s *Server) Port() int { return s.ln.Addr().(*net.TCPAddr).Port }

// URLs are the addresses to open: this PC's, plus the LAN ones when enabled.
func (s *Server) URLs() []string {
	q := "/"
	if s.token != "" {
		q = "/?k=" + url.QueryEscape(s.token)
	}
	urls := []string{fmt.Sprintf("http://127.0.0.1:%d%s", s.Port(), q)}
	if s.token != "" {
		for _, ip := range LANAddrs() {
			urls = append(urls, fmt.Sprintf("http://%s:%d%s", ip, s.Port(), q))
		}
	}
	return urls
}

// Publish sends an event to every viewer. It never blocks: a viewer that
// can't keep up misses lines rather than slowing the translator down.
func (s *Server) Publish(e Event) {
	if s == nil {
		return
	}
	if e.Time == 0 {
		e.Time = time.Now().UnixMilli()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Type != "hearing" { // live text is only worth showing live
		s.ring = append(s.ring, e)
		if len(s.ring) > replay {
			s.ring = s.ring[1:]
		}
	}
	for ch := range s.subs {
		select {
		case ch <- b:
		default:
		}
	}
}

// guard rejects requests for other host names (DNS rebinding: a web page on
// the internet must not be able to read the chat through the browser) and,
// with LAN access, requests without the key.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := strings.LastIndex(host, ":"); i >= 0 && !strings.HasSuffix(host, "]") {
			host = host[:i]
		}
		if !s.hosts[host] {
			http.Error(w, "unknown host", http.StatusForbidden)
			return
		}
		if s.token != "" && r.URL.Query().Get("k") != s.token {
			http.Error(w, "missing or wrong key", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (s *Server) servePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	w.Write(page)
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch := make(chan []byte, 32)
	s.mu.Lock()
	old := append([]Event(nil), s.ring...)
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	fmt.Fprint(w, "retry: 2000\n\n")
	now := time.Now().UnixMilli()
	for _, e := range old {
		e.Age = max(now-e.Time, 1)
		if b, err := json.Marshal(e); err == nil {
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
	}
	fl.Flush()
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", b)
		case <-keep.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		}
		fl.Flush()
	}
}

// LANAddrs lists this PC's private IPv4 addresses.
func LANAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip := n.IP.To4(); ip != nil && ip.IsPrivate() {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

func randomKey() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(errors.New("no randomness available"))
	}
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
