package translator

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

const (
	DefaultOllamaPort  = 11434
	DefaultOllamaModel = "hf.co/blackcloud1199/qwen-translation-vi"
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
