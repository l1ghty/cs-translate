package main

import (
	"strings"
	"testing"

	"github.com/micha/cs-ingame-translate/audio"
)

func TestHearingText(t *testing.T) {
	for _, tc := range []struct{ confirmed, tentative, want string }{
		{"rush b", "we go", "rush b ▸ we go"},
		{"", "rush", "… rush"},
		{"rush b we go", "", "rush b we go"},
	} {
		if got := hearingText(audio.Transcript{Text: tc.confirmed, Tentative: tc.tentative}); got != tc.want {
			t.Errorf("hearingText(%q,%q) = %q, want %q", tc.confirmed, tc.tentative, got, tc.want)
		}
	}
}

func TestBusyLabelShowsLiveText(t *testing.T) {
	b := &busy{}
	if b.label() != "" {
		t.Errorf("idle label = %q", b.label())
	}
	b.hearing = "rush b ▸ we"
	b.translating = 2
	got := b.label()
	if !strings.HasPrefix(got, "Hearing: rush b ▸ we") || !strings.Contains(got, "Translating (1 queued)") {
		t.Errorf("label = %q", got)
	}
}

func TestClearHearingOnlyForItsUtterance(t *testing.T) {
	s := &session{busy: &busy{hearing: "next sentence", hearingUtt: 5}}
	s.clearHearing(audio.Transcript{Source: "live", Utterance: 4}) // final of an earlier utterance
	if s.busy.hearing == "" {
		t.Error("an earlier utterance's final cleared the current live text")
	}
	s.clearHearing(audio.Transcript{Source: "capture", Utterance: 9}) // F9 captures never show live text
	if s.busy.hearing == "" {
		t.Error("a capture cleared the live text")
	}
	s.clearHearing(audio.Transcript{Source: "live", Utterance: 5})
	if s.busy.hearing != "" {
		t.Errorf("hearing = %q", s.busy.hearing)
	}
}
