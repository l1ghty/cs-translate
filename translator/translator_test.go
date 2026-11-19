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
	if o := tr.options(); o["top_k"] != 20 || o["temperature"] != 0.7 {
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
	if o := other.options(); o["temperature"] != 0.3 || o["top_k"] != nil {
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
