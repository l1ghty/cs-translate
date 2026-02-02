package parser

import (
	"regexp"
	"strings"
)

// ChatMessage represents a parsed chat message
type ChatMessage struct {
	OriginalText   string
	PlayerName     string
	MessageContent string
	IsDead         bool
	Team           string // "CT", "T", or empty for all
}

var (
	// Regex for standard chat: "PlayerName : Message"
	// Taking into account potential unicode characters in names
	// And *DEAD* prefix
	// And team chat: PlayerName @ Team : Message (Check actual CS2 format)
	// CS2 console output format example needed.
	// Usually:
	// " PlayerName : Message"
	// " *DEAD* PlayerName : Message"

	// We will use a flexible regex
	// ^\s*(\*DEAD\*)?\s*([^:]+?)\s*:\s*(.+)$

	// Updated text from user:
	// 02/02 00:35:34  [ALL] l1ght: testing
	// 02/02 00:35:34  [T] l1ght: testing hello
	chatRegex = regexp.MustCompile(`^\d{2}/\d{2}\s+\d{2}:\d{2}:\d{2}\s+\[(?P<Team>[^\]]+)\]\s+(?P<Name>[^:]+):\s+(?P<Message>.+)$`)
)

// ParseLine parses a line from the loop
// Returns nil if the line is not a chat message
func ParseLine(line string) *ChatMessage {
	// Clean up empty chars
	line = strings.TrimSpace(line)

	// Optimization: Chat lines usually contain ": "
	// This filters out many system messages like "Map:de_dust2" (no space after colon)
	if !strings.Contains(line, ": ") {
		return nil
	}

	// Optimization: Chat lines must contain brackets for Team info (e.g. [ALL])
	// User requested to skip lines not containing something with square brackets
	if !strings.Contains(line, "[") {
		return nil
	}
	if !strings.Contains(line, "[ALL") && !strings.Contains(line, "[T") && !strings.Contains(line, "[CT") {
		return nil
	}

	if !chatRegex.MatchString(line) {
		return nil
	}

	matches := chatRegex.FindStringSubmatch(line)
	result := make(map[string]string)
	names := chatRegex.SubexpNames()

	// Safe extraction
	for i, match := range matches {
		if i < len(names) && names[i] != "" {
			result[names[i]] = match
		}
	}

	name := strings.TrimSpace(result["Name"])
	message := result["Message"]
	team := result["Team"]

	// skip if missing name or message or team
	if name == "" || message == "" || team == "" {
		return nil
	}

	return &ChatMessage{
		OriginalText:   line,
		PlayerName:     name,
		MessageContent: message,
		Team:           team,
		IsDead:         false, // Not explicitly captured in this format yet
	}
}
