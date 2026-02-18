package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Translator defines the interface for translating text
type Translator interface {
	Translate(ctx context.Context, text string) (string, error)
	Close() error
}

// OllamaTranslator implements Translator using local Ollama LLM
type OllamaTranslator struct {
	httpClient *http.Client
	baseURL    string
	model      string
	targetLang string
}

// OllamaRequest represents the request body for Ollama API
type OllamaRequest struct {
	Model   string `json:"model"`
	Prompt  string `json:"prompt"`
	Stream  bool   `json:"stream"`
	Options struct {
		Temperature float64 `json:"temperature"`
	} `json:"options,omitempty"`
}

// OllamaResponse represents the response from Ollama API
type OllamaResponse struct {
	Model    string `json:"model"`
	Response string `json:"response"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

// NewOllamaTranslator creates a new Ollama translator
func NewOllamaTranslator(ctx context.Context, model, targetLang string) (*OllamaTranslator, error) {
	baseURL := os.Getenv("OLLAMA_HOST")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	if model == "" {
		model = "qwen3:0.6b" // Default lightweight model for translation
	}

	if targetLang == "" {
		targetLang = "English"
	}

	return &OllamaTranslator{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL:    baseURL,
		model:      model,
		targetLang: targetLang,
	}, nil
}

// Translate translates the text to the target language using Ollama
func (t *OllamaTranslator) Translate(ctx context.Context, text string) (string, error) {
	// Skip translation for very short or non-text content
	text = strings.TrimSpace(text)
	if text == "" || len(text) < 2 {
		return text, nil
	}

	// Build the translation prompt
	prompt := fmt.Sprintf("Translate the following text to %s. Output ONLY the translation, nothing else:\n\n%s", t.targetLang, text)

	reqBody := OllamaRequest{
		Model:  t.model,
		Prompt: prompt,
		Stream: false,
	}
	reqBody.Options.Temperature = 0.3 // Low temperature for consistent translations

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %v", err)
	}

	url := fmt.Sprintf("%s/api/generate", t.baseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %v", err)
	}

	var ollamaResp OllamaResponse
	if err := json.Unmarshal(body, &ollamaResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %v", err)
	}

	if ollamaResp.Error != "" {
		return "", fmt.Errorf("ollama error: %s", ollamaResp.Error)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama API returned status %d: %s", resp.StatusCode, ollamaResp.Response)
	}

	translation := strings.TrimSpace(ollamaResp.Response)
	if translation == "" {
		return text, nil // Return original if translation is empty
	}

	return translation, nil
}

// Close cleans up resources and unloads the model
func (t *OllamaTranslator) Close() error {
	// Unload the model from memory
	url := fmt.Sprintf("%s/api/generate", t.baseURL)
	reqBody := map[string]interface{}{
		"model":      t.model,
		"prompt":     "",
		"stream":     false,
		"keep_alive": 0, // Unload immediately
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal unload request: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create unload request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Don't fail if model is already unloaded or server is down
		return nil
	}
	defer resp.Body.Close()

	return nil
}
