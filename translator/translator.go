package translator

import (
	"context"
	"fmt"

	"html"

	"cloud.google.com/go/translate"
	"golang.org/x/text/language"
	"google.golang.org/api/option"
)

// Translator defines the interface for translating text
type Translator interface {
	Translate(ctx context.Context, text string) (string, error)
	Close() error
}

// GTranslator implements Translator using Google Cloud Translation API
type GTranslator struct {
	client     *translate.Client
	targetLang language.Tag
}

// NewGTranslator creates a new translator
func NewGTranslator(ctx context.Context, apiKey string) (*GTranslator, error) {
	var opts []option.ClientOption
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}

	client, err := translate.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create translate client: %v", err)
	}

	return &GTranslator{
		client:     client,
		targetLang: language.English,
	}, nil
}

// Translate translates the text to English
func (t *GTranslator) Translate(ctx context.Context, text string) (string, error) {
	resp, err := t.client.Translate(ctx, []string{text}, t.targetLang, nil)
	if err != nil {
		return "", err
	}
	if len(resp) == 0 {
		return "", fmt.Errorf("translation returned no results")
	}
	return html.UnescapeString(resp[0].Text), nil
}

// Close closes the underlying client
func (t *GTranslator) Close() error {
	return t.client.Close()
}
