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
	Raw       bool           `json:"raw,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
	Done     bool   `json:"done,omitempty"`
}

// VoiceContext represents recent transcription context for voice translation
type VoiceContext struct {
	ContextText string // Recent transcriptions from last 10 seconds
	Voice       bool   // the text is speech: use the short answer limit
}

// keepAlive keeps the model loaded between translations: reloading it from
// disk after Ollama's default 5 idle minutes causes a CPU/disk spike mid-game
// and a slow translation. KeepWarm renews it while the tool runs; if the tool
// dies without unloading the model, Ollama frees it after this time.
const keepAlive = "30m"

// Hy-MT chat format (llama.cpp's "hunyuan-dense" template). Ollama doesn't
// apply it to these GGUFs by itself, and without it the model never sees the
// end of its turn: answers run on until a limit, and translations take ages.
const (
	hyUser      = "<｜hy_User｜>"
	hyAssistant = "<｜hy_Assistant｜>"
	hyEndOfTurn = "<｜hy_place▁holder▁no▁2｜>"
	hyEnd       = "<｜hy_place▁holder▁no▁8｜>"
)

// maxAnswerTokens caps every answer. A translated chat line or voice clip
// is far shorter; the cap only stops a model that runs on.
const maxAnswerTokens = 256

// maxVoiceTokens caps voice answers harder: spoken callouts are a few words,
// so a model that rambles must not delay the next callout.
const maxVoiceTokens = 96

// retiredModels are former defaults. If one is still loaded when the tool
// starts (e.g. after an unclean exit), it is unloaded to free its memory.
var retiredModels = []string{"hf.co/blackcloud1199/qwen-translation-vi"}

// keepWarmEvery renews keepAlive well before it runs out.
const keepWarmEvery = 10 * time.Minute

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

	prompt := t.prompt(text, vc.ContextText)
	limit := maxAnswerTokens
	if vc.ContextText != "" || vc.Voice {
		limit = maxVoiceTokens
	}
	translation, err := t.generate(ctx, prompt, limit)
	if err != nil {
		return "", err
	}
	if translation == "" {
		return text, nil
	}
	return translation, nil
}

// isHyMT reports whether the model is one of Tencent's Hy-MT translation
// models, which are trained on fixed prompts and sampling settings.
func (t *OllamaTranslator) isHyMT() bool {
	return strings.Contains(strings.ToLower(t.model), "hy-mt")
}

// prompt builds the translation prompt. Hy-MT models get the exact prompts
// from their model card; other models get a generic instruction.
func (t *OllamaTranslator) prompt(text, contextText string) string {
	if t.isHyMT() {
		switch {
		case contextText != "":
			return fmt.Sprintf("%s\n参考上面的信息，把下面的文本翻译成%s，注意不需要翻译上文，也不要额外解释：\n%s",
				contextText, t.targetLang, text)
		case LanguageCode(t.targetLang) == "zh":
			return fmt.Sprintf("将以下文本翻译为%s，注意只需要输出翻译后的结果，不要额外解释：\n\n%s", t.targetLang, text)
		default:
			return fmt.Sprintf("Translate the following segment into %s, without additional explanation.\n\n%s", t.targetLang, text)
		}
	}
	if contextText != "" {
		return fmt.Sprintf(`Context from recent speech (last 10 seconds):
%s

Translate the following text to %s. Use the context above to understand the conversation topic and provide a more accurate translation. Output ONLY the translation, nothing else:

%s`, contextText, t.targetLang, text)
	}
	return fmt.Sprintf("Translate the following text to %s. Output ONLY the translation, nothing else:\n\n%s", t.targetLang, text)
}

// Preload loads the model into memory and runs one tiny translation, so the
// first real translation is answered immediately instead of waiting for the
// model to load. Loading can take from a second to a few minutes (large
// model, slow disk), so this should be called at startup.
func (t *OllamaTranslator) Preload(ctx context.Context) error {
	t.unloadRetired(ctx)
	if err := t.load(ctx); err != nil {
		return err
	}
	_, err := t.Translate(ctx, "Good luck, have fun!") // warms up prompt processing
	return err
}

// unloadRetired frees the memory of a former default model that is still
// loaded in Ollama, so it doesn't compete with the current one.
func (t *OllamaTranslator) unloadRetired(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/api/ps", nil)
	if err != nil {
		return
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var ps struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&ps) != nil {
		return
	}
	for _, m := range ps.Models {
		for _, old := range retiredModels {
			name := strings.ToLower(m.Name)
			if !strings.EqualFold(old, t.model) && (name == old || strings.HasPrefix(name, old+":")) {
				unload(t.baseURL, m.Name)
			}
		}
	}
}

// KeepWarm keeps the model loaded until ctx is done, so translations stay
// instant even after long quiet periods.
func (t *OllamaTranslator) KeepWarm(ctx context.Context) {
	tick := time.NewTicker(keepWarmEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.load(ctx)
		}
	}
}

// load asks Ollama to load the model (a request without a prompt only loads
// it), with the same options as translations so Ollama doesn't reload it.
func (t *OllamaTranslator) load(ctx context.Context) error {
	client := &http.Client{Timeout: 10 * time.Minute} // first load from a slow disk can take long
	_, err := t.request(ctx, client, "", maxAnswerTokens)
	return err
}

func (t *OllamaTranslator) options(numPredict int) map[string]any {
	options := map[string]any{"temperature": 0.3, "num_predict": numPredict}
	if t.isHyMT() {
		// sampling settings recommended on the Hy-MT2 1.8B/7B model cards
		options = map[string]any{"temperature": 0.7, "top_p": 0.6, "top_k": 20, "repeat_penalty": 1.05,
			"num_predict": numPredict, "stop": []string{hyEndOfTurn, hyEnd, hyUser}}
	}
	if t.opts.CPUOnly {
		options["num_gpu"] = 0
	}
	if t.opts.Threads > 0 {
		options["num_thread"] = t.opts.Threads
	}
	return options
}

func (t *OllamaTranslator) generate(ctx context.Context, prompt string, numPredict int) (string, error) {
	return t.request(ctx, t.httpClient, prompt, numPredict)
}

func (t *OllamaTranslator) request(ctx context.Context, client *http.Client, prompt string, numPredict int) (string, error) {
	// Translations are streamed (tokens are read as they are generated and the
	// connection ends the moment the model is done). A load request, which has
	// no prompt, is a single reply.
	r := ollamaRequest{Model: t.model, Prompt: prompt, Stream: prompt != "", KeepAlive: keepAlive, Options: t.options(numPredict)}
	if prompt != "" && t.isHyMT() {
		// send the prompt already in the model's chat format
		r.Raw = true
		r.Prompt = hyUser + prompt + hyAssistant
	}
	body, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	var sb strings.Builder
	dec := json.NewDecoder(resp.Body)
	for {
		var out ollamaResponse
		if err := dec.Decode(&out); err != nil {
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("ollama API returned status %d", resp.StatusCode)
			}
			if err == io.EOF {
				break // stream ended without a final "done" line
			}
			return "", fmt.Errorf("failed to read response: %w", err)
		}
		if out.Error != "" {
			return "", fmt.Errorf("ollama error: %s", out.Error)
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("ollama API returned status %d", resp.StatusCode)
		}
		sb.WriteString(out.Response)
		if out.Done {
			break
		}
	}
	return cleanOutput(sb.String()), nil
}

// cleanOutput drops a reasoning block (<think>...</think>) that some models
// emit before the answer, and surrounding whitespace.
func cleanOutput(s string) string {
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

// Close unloads the model so its memory is freed when the tool exits.
func (t *OllamaTranslator) Close() error {
	unload(t.baseURL, t.model)
	return nil
}

// unload asks Ollama to free a model's memory right away.
func unload(baseURL, model string) {
	body, _ := json.Marshal(map[string]any{"model": model, "keep_alive": 0})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}
