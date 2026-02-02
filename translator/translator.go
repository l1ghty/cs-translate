package translator

import (
	"github.com/bregydoc/gtranslate"
)

// Translator defines the interface for translating text
type Translator interface {
	Translate(text string) (string, error)
}

// GTranslator implements Translator using Google Translate (scraper)
type GTranslator struct {
	TargetLang string
}

// NewGTranslator creates a new translator
func NewGTranslator() *GTranslator {
	return &GTranslator{
		TargetLang: "en",
	}
}

// Translate translates the text to English
func (t *GTranslator) Translate(text string) (string, error) {
	return gtranslate.TranslateWithParams(
		text,
		gtranslate.TranslationParams{
			From: "auto",
			To:   t.TargetLang,
		},
	)
}
