package callouts

import (
	"strings"
	"testing"
)

func TestMapFromLine(t *testing.T) {
	for line, want := range map[string]string{
		`Loading map "de_mirage"`:                "de_mirage",
		"[Client] Spawn Server: de_Inferno":      "de_inferno",
		"Map:de_dust2":                           "de_dust2",
		"02/02 00:35:34  [ALL] l1ght: de_mirage": "",
		"nothing here":                           "",
	} {
		if got := MapFromLine(line); got != want {
			t.Errorf("MapFromLine(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestPrompt(t *testing.T) {
	if p := Prompt("de_mirage"); !strings.Contains(p, "palace") || !strings.Contains(p, "AWP") {
		t.Errorf("mirage prompt = %q", p)
	}
	if p := Prompt("de_unknown"); !strings.Contains(p, "AWP") || strings.Contains(p, "palace") {
		t.Errorf("unknown map prompt = %q", p)
	}
}

func TestLookup(t *testing.T) {
	if got, ok := Lookup("ru", " Раш Б! "); !ok || got != "Rush B" {
		t.Errorf("got %q %v", got, ok)
	}
	if _, ok := Lookup("ru", "привет как дела"); ok {
		t.Error("unexpected hit")
	}
	if _, ok := Lookup("xx", "drop"); ok {
		t.Error("unknown language hit")
	}
}
