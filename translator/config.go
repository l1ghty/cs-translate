package translator

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

const (
	DefaultOllamaPort = 11434
	// DefaultOllamaModel is Tencent's Hy-MT2 1.8B, a model made only for
	// translation (33 languages, ~1.1 GB at Q4_K_M), small enough to run
	// next to a game.
	DefaultOllamaModel = "hf.co/tencent/Hy-MT2-1.8B-GGUF:Q4_K_M"
)

// OllamaHost is the base URL of the Ollama API, e.g. http://localhost:11434.
var OllamaHost = GetOllamaHost()

// GetOllamaHost returns the Ollama base URL, honouring OLLAMA_HOST. Ollama
// itself accepts values like "0.0.0.0", "localhost:11434" or
// "http://host:port", so all of those are normalised to a usable URL.
func GetOllamaHost() string {
	return normalizeHost(os.Getenv("OLLAMA_HOST"))
}

func normalizeHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Sprintf("http://localhost:%d", DefaultOllamaPort)
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("http://localhost:%d", DefaultOllamaPort)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil { // no port given
		host, port = u.Hostname(), fmt.Sprint(DefaultOllamaPort)
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "localhost" // listen-all address: connect locally
	}
	return fmt.Sprintf("%s://%s", u.Scheme, net.JoinHostPort(host, port))
}
