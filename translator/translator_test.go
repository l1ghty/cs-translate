package translator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
