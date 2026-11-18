package translator

import "testing"

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"":                     "http://localhost:11434",
		"0.0.0.0":              "http://localhost:11434",
		"0.0.0.0:11500":        "http://localhost:11500",
		"localhost:11434":      "http://localhost:11434",
		"http://10.0.0.5:8080": "http://10.0.0.5:8080",
		"https://ollama.lan":   "https://ollama.lan:11434",
		"http://[::]:11434":    "http://localhost:11434",
		"  127.0.0.1:11434  ":  "http://127.0.0.1:11434",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLanguageCode(t *testing.T) {
	cases := map[string]string{"English": "en", "russian": "ru", " German ": "de", "pt": "pt", "Klingon": ""}
	for in, want := range cases {
		if got := LanguageCode(in); got != want {
			t.Errorf("LanguageCode(%q) = %q, want %q", in, got, want)
		}
	}
}
