package translator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPreloadLoadsThenWarmsUpWithSameOptions(t *testing.T) {
	var mu sync.Mutex
	var reqs []ollamaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
			return
		}
		var req ollamaRequest
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		resp := ollamaResponse{}
		if req.Prompt != "" {
			resp.Response = "translated"
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	tr, _ := NewOllamaTranslator(context.Background(), "m", "English", Options{CPUOnly: true, Threads: 2})
	tr.baseURL = srv.URL
	if err := tr.Preload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want load + warm-up", len(reqs))
	}
	if reqs[0].Prompt != "" || reqs[1].Prompt == "" {
		t.Errorf("first request should only load the model: %+v", reqs)
	}
	for i, r := range reqs {
		// Different options would make Ollama reload the model.
		if r.KeepAlive != keepAlive || r.Options["num_gpu"] != float64(0) || r.Options["num_thread"] != float64(2) {
			t.Errorf("request %d: %+v", i, r)
		}
	}
}

func TestHyMTPromptsAndOptions(t *testing.T) {
	tr, _ := NewOllamaTranslator(context.Background(), DefaultOllamaModel, "English", Options{})
	if !tr.isHyMT() {
		t.Fatal("default model should be detected as Hy-MT")
	}
	if got, want := tr.prompt("привет", ""), "Translate the following segment into English, without additional explanation.\n\nпривет"; got != want {
		t.Errorf("prompt = %q, want %q", got, want)
	}
	if got := tr.prompt("rush b", "we go mid"); !strings.HasPrefix(got, "we go mid\n参考上面的信息") || !strings.HasSuffix(got, "English，注意不需要翻译上文，也不要额外解释：\nrush b") {
		t.Errorf("context prompt = %q", got)
	}
	if o := tr.options(maxAnswerTokens); o["top_k"] != 20 || o["temperature"] != 0.7 {
		t.Errorf("options = %v", o)
	}

	zh, _ := NewOllamaTranslator(context.Background(), DefaultOllamaModel, "Chinese", Options{})
	if got := zh.prompt("gg", ""); !strings.HasPrefix(got, "将以下文本翻译为Chinese") {
		t.Errorf("Chinese prompt = %q", got)
	}

	other, _ := NewOllamaTranslator(context.Background(), "translategemma:4b", "English", Options{})
	if other.isHyMT() || !strings.HasPrefix(other.prompt("x", ""), "Translate the following text to English.") {
		t.Errorf("generic prompt = %q", other.prompt("x", ""))
	}
	if o := other.options(maxAnswerTokens); o["temperature"] != 0.3 || o["top_k"] != nil {
		t.Errorf("generic options = %v", o)
	}
}

func TestCleanOutputDropsThinking(t *testing.T) {
	for in, want := range map[string]string{
		"  hello \n":                      "hello",
		"<think>\nhmm\n</think>\n\nhello": "hello",
		"<think></think>hello":            "hello",
	} {
		if got := cleanOutput(in); got != want {
			t.Errorf("cleanOutput(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeOllama records /api/generate requests and reports loaded models on /api/ps.
func fakeOllama(t *testing.T, loaded ...string) (*httptest.Server, func() []ollamaRequest) {
	var mu sync.Mutex
	var reqs []ollamaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			var models []map[string]string
			for _, m := range loaded {
				models = append(models, map[string]string{"name": m})
			}
			json.NewEncoder(w).Encode(map[string]any{"models": models})
			return
		}
		var req ollamaRequest
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		json.NewEncoder(w).Encode(ollamaResponse{Response: "ok"})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []ollamaRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]ollamaRequest(nil), reqs...)
	}
}

func TestHyMTUsesRawChatFormatAndStops(t *testing.T) {
	srv, reqs := fakeOllama(t)
	tr, _ := NewOllamaTranslator(context.Background(), DefaultOllamaModel, "English", Options{})
	tr.baseURL = srv.URL
	if _, err := tr.Translate(context.Background(), "привет"); err != nil {
		t.Fatal(err)
	}
	r := reqs()[0]
	want := "<｜hy_User｜>Translate the following segment into English, without additional explanation.\n\nпривет<｜hy_Assistant｜>"
	if !r.Raw || r.Prompt != want {
		t.Errorf("raw=%v prompt=%q", r.Raw, r.Prompt)
	}
	stops, _ := r.Options["stop"].([]any)
	if len(stops) == 0 || stops[0] != "<｜hy_place▁holder▁no▁2｜>" || r.Options["num_predict"] != float64(maxAnswerTokens) {
		t.Errorf("options = %v", r.Options)
	}

	other, _ := NewOllamaTranslator(context.Background(), "translategemma:4b", "English", Options{})
	other.baseURL = srv.URL
	other.Translate(context.Background(), "hola")
	if r := reqs()[1]; r.Raw || strings.Contains(r.Prompt, "hy_User") {
		t.Errorf("other models must use Ollama's template: %+v", r)
	}
}

func TestPreloadUnloadsRetiredDefault(t *testing.T) {
	srv, reqs := fakeOllama(t, "hf.co/blackcloud1199/qwen-translation-vi:latest", "llama3:8b")
	tr, _ := NewOllamaTranslator(context.Background(), DefaultOllamaModel, "English", Options{})
	tr.baseURL = srv.URL
	if err := tr.Preload(context.Background()); err != nil {
		t.Fatal(err)
	}
	var unloaded []string
	for _, r := range reqs() {
		if r.Model != DefaultOllamaModel {
			unloaded = append(unloaded, r.Model)
		}
	}
	if len(unloaded) != 1 || unloaded[0] != "hf.co/blackcloud1199/qwen-translation-vi:latest" {
		t.Errorf("unloaded %v, want only the retired default", unloaded)
	}
}

func TestTranslationIsStreamedAndVoiceIsCapped(t *testing.T) {
	var got ollamaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		enc := json.NewEncoder(w)
		enc.Encode(ollamaResponse{Response: "Rush "})
		enc.Encode(ollamaResponse{Response: "B", Done: true})
	}))
	defer srv.Close()
	tr, _ := NewOllamaTranslator(context.Background(), "m", "English", Options{})
	tr.baseURL = srv.URL
	out, err := tr.TranslateWithContext(context.Background(), "раш б", VoiceContext{Voice: true})
	if err != nil || out != "Rush B" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if !got.Stream || got.Options["num_predict"] != float64(maxVoiceTokens) {
		t.Errorf("request = %+v", got)
	}
	if _, err := tr.Translate(context.Background(), "привет"); err != nil || got.Options["num_predict"] != float64(maxAnswerTokens) {
		t.Errorf("chat request = %+v, err = %v", got, err)
	}
}
