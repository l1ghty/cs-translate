package translator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Translator defines the interface for translating text
type Translator interface {
	Translate(ctx context.Context, text string) (string, error)
	Close() error
}

// Options tune how the translation model runs.
type Options struct {
	// CPUOnly keeps the model off the GPU so the game keeps all VRAM and GPU time.
	CPUOnly bool
	// Threads limits CPU threads when running on CPU (0 = Ollama default).
	Threads int
}

// OllamaTranslator implements Translator using local Ollama LLM
type OllamaTranslator struct {
	httpClient *http.Client
	baseURL    string
	model      string
	targetLang string
	opts       Options
}

type ollamaRequest struct {
	Model     string         `json:"model"`
	Prompt    string         `json:"prompt"`
	Stream    bool           `json:"stream"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}

// VoiceContext represents recent transcription context for voice translation
type VoiceContext struct {
	ContextText string // Recent transcriptions from last 10 seconds
}

// keepAlive keeps the model loaded for a whole match: reloading it from disk
// after Ollama's default 5 idle minutes causes a CPU/disk spike mid-game.
const keepAlive = "30m"

// NewOllamaTranslator creates a new Ollama translator
func NewOllamaTranslator(ctx context.Context, model, targetLang string, opts Options) (*OllamaTranslator, error) {
	if model == "" {
		model = DefaultOllamaModel
	}
	if targetLang == "" {
		targetLang = "English"
	}
	return &OllamaTranslator{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		baseURL:    OllamaHost,
		model:      model,
		targetLang: targetLang,
		opts:       opts,
	}, nil
}

// TargetLanguage returns the language translations are made into.
func (t *OllamaTranslator) TargetLanguage() string { return t.targetLang }

// Translate translates the text to the target language using Ollama
func (t *OllamaTranslator) Translate(ctx context.Context, text string) (string, error) {
	return t.TranslateWithContext(ctx, text, VoiceContext{})
}

// TranslateWithContext translates text with additional context from recent transcriptions
func (t *OllamaTranslator) TranslateWithContext(ctx context.Context, text string, vc VoiceContext) (string, error) {
	text = strings.TrimSpace(text)
	if len(text) < 2 {
		return text, nil
	}

	var prompt string
	if vc.ContextText != "" {
		prompt = fmt.Sprintf(`Context from recent speech (last 10 seconds):
%s

Translate the following text to %s. Use the context above to understand the conversation topic and provide a more accurate translation. Output ONLY the translation, nothing else:

%s`, vc.ContextText, t.targetLang, text)
	} else {
		prompt = fmt.Sprintf("Translate the following text to %s. Output ONLY the translation, nothing else:\n\n%s", t.targetLang, text)
	}

	translation, err := t.generate(ctx, prompt)
	if err != nil {
		return "", err
	}
	if translation == "" {
		return text, nil
	}
	return translation, nil
}

func (t *OllamaTranslator) generate(ctx context.Context, prompt string) (string, error) {
	options := map[string]any{"temperature": 0.3}
	if t.opts.CPUOnly {
		options["num_gpu"] = 0
	}
	if t.opts.Threads > 0 {
		options["num_thread"] = t.opts.Threads
	}
	body, err := json.Marshal(ollamaRequest{
		Model:     t.model,
		Prompt:    prompt,
		KeepAlive: keepAlive,
		Options:   options,
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}
	var out ollamaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("ollama error: %s", out.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama API returned status %d", resp.StatusCode)
	}
	return strings.TrimSpace(out.Response), nil
}

// Close unloads the model so its memory is freed when the tool exits.
func (t *OllamaTranslator) Close() error {
	body, _ := json.Marshal(map[string]any{"model": t.model, "keep_alive": 0})
	req, err := http.NewRequest(http.MethodPost, t.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
	return nil
}
