package translator

import "strings"

// languageCodes maps language names (as accepted by -lang) to the ISO 639-1
// codes Whisper reports.
var languageCodes = map[string]string{
	"arabic": "ar", "bulgarian": "bg", "chinese": "zh", "croatian": "hr",
	"czech": "cs", "danish": "da", "dutch": "nl", "english": "en",
	"estonian": "et", "finnish": "fi", "french": "fr", "german": "de",
	"greek": "el", "hebrew": "he", "hindi": "hi", "hungarian": "hu",
	"indonesian": "id", "italian": "it", "japanese": "ja", "kazakh": "kk",
	"korean": "ko", "latvian": "lv", "lithuanian": "lt", "norwegian": "no",
	"persian": "fa", "polish": "pl", "portuguese": "pt", "romanian": "ro",
	"russian": "ru", "serbian": "sr", "slovak": "sk", "slovenian": "sl",
	"spanish": "es", "swedish": "sv", "thai": "th", "turkish": "tr",
	"ukrainian": "uk", "vietnamese": "vi",
}

// LanguageCode returns the ISO 639-1 code for a language name or code, or "".
func LanguageCode(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if code, ok := languageCodes[l]; ok {
		return code
	}
	for _, code := range languageCodes {
		if code == l {
			return code
		}
	}
	return ""
}
