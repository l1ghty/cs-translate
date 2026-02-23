package translator

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

const (
	DefaultOllamaPort    = 11434
	DefaultOllamaBaseURL = "http://localhost"
	DefaultOllamaModel   = "hf.co/blackcloud1199/qwen-translation-vi"
	DefaultWhisperModel  = "turbo"
)

var OllamaHost string

func init() {
	OllamaHost = GetOllamaHost()
}

func GetOllamaHost() string {
	if envHost := os.Getenv("OLLAMA_HOST"); envHost != "" {
		return envHost
	}
	return fmt.Sprintf("%s:%d", DefaultOllamaBaseURL, DefaultOllamaPort)
}

func GetOllamaPort() int {
	if envHost := os.Getenv("OLLAMA_HOST"); envHost != "" {
		_, portStr, err := net.SplitHostPort(envHost)
		if err == nil {
			if port, err := strconv.Atoi(portStr); err == nil {
				return port
			}
		}
	}
	return DefaultOllamaPort
}

func FindAvailablePort(startPort int) (int, error) {
	for port := startPort; port <= 65535; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
		if err == nil {
			ln.Close()
			fmt.Printf("alternative Port %d is available\n", port)
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found in range %d-65535", startPort)
}
