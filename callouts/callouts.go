// Package callouts holds Counter-Strike 2 specific knowledge that makes live
// voice translation faster and more accurate: which map is being played (read
// from console.log), the vocabulary Whisper should expect on it, and a cache of
// very common callouts that need no LLM call at all.
package callouts

import (
	"regexp"
	"strings"
)

// mapRe finds a map load in a console.log line, e.g.
//
//	Loading map "de_mirage"
//	Spawn Server: de_inferno
//	Map: de_dust2
var mapRe = regexp.MustCompile(`(?i)(?:loading map|spawn server:?|changelevel|map:?)\s*"?((?:de|cs|ar|dz)_[a-z0-9_]+)`)

// MapFromLine returns the map named by a console.log line ("" if none).
func MapFromLine(line string) string {
	m := mapRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

const universal = "AWP, eco, force buy, clutch, lit, rotate, plant, defuse, rush B, A site, B site, mid, flash, smoke, molly"

var mapTerms = map[string]string{
	"de_mirage":   "palace, ramp, apps, apartments, connector, jungle, ticket, window, short, catwalk, top mid, underpass, CT, sniper's nest",
	"de_inferno":  "banana, apartments, arch, library, pit, graveyard, second mid, coffins, boiler, construction, short",
	"de_dust2":    "long, doors, cat, short, tunnels, upper tunnels, lower tunnels, pit, goose, car, platform, xbox",
	"de_anubis":   "canal, connector, water, bridge, palace, mid, alley, walkway, heaven, temple",
	"de_nuke":     "outside, ramp, secret, heaven, hell, vents, silo, lobby, squeaky, main",
	"de_overpass": "monster, short, bathrooms, connector, fountain, heaven, party, playground, barrels, truck",
	"de_ancient":  "donut, elbow, main, cave, ramp, temple, red room",
	"de_vertigo":  "ramp, mid, A ramp, B stairs, scaffolding, generator, elevator",
	"de_train":    "ivy, pop dog, connector, Z, main, ladder room",
}

// Prompt returns the Whisper initial prompt for a map: universal terms plus
// the map's own callouts. Unknown maps get the universal terms only.
func Prompt(mapName string) string {
	if extra, ok := mapTerms[strings.ToLower(mapName)]; ok {
		return universal + ", " + extra + "."
	}
	return universal + "."
}

// phrases maps normalized high-frequency callouts to their English form per
// language code. Only unambiguous callouts belong here.
var phrases = map[string]map[string]string{
	"ru": {
		"раш б": "Rush B", "раш а": "Rush A", "один мид": "One mid", "дроп": "Drop",
		"эко": "Eco", "форс": "Force", "бомба": "Bomb", "бомба на а": "Bomb on A", "бомба на б": "Bomb on B",
		"смок": "Smoke", "флеш": "Flash", "хелп": "Help", "ротация": "Rotate", "ротейт": "Rotate",
	},
	"de": {
		"rush b": "Rush B", "rush a": "Rush A", "eco": "Eco", "drop": "Drop", "bombe": "Bomb",
	},
	"pl": {"rush b": "Rush B", "rush a": "Rush A", "dropuj": "Drop", "eko": "Eco"},
	"tr": {"b'ye rush": "Rush B", "a'ya rush": "Rush A", "drop": "Drop", "eko": "Eco"},
}

var punctRe = regexp.MustCompile(`[\p{P}\p{S}]+`)

func normalize(s string) string {
	s = punctRe.ReplaceAllString(strings.ToLower(s), " ")
	return strings.Join(strings.Fields(s), " ")
}

// Lookup returns the English translation of a very common callout spoken in
// language lang, if known. The second result is false when the LLM is needed.
func Lookup(lang, text string) (string, bool) {
	t, ok := phrases[lang][normalize(text)]
	return t, ok
}
