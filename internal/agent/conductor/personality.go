package conductor

import "strings"

// sessionProfileName is the label recorded on a new session. A personal assistant
// uses its own name. A Podium chat, and every non-web source, keeps the house name.
// An existing session is not rewritten: UpsertSession leaves profile alone on conflict.
func sessionProfileName(house, personality string) string {
	if personality != "" {
		return personality
	}
	return house
}

// systemPromptWithPersonality appends a personal assistant's instructions to a copy
// of Podium's system prompt. An empty extra leaves the prompt unchanged. The stored
// prompt is not modified; this string exists only for the brief.
func systemPromptWithPersonality(base, extra string) string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return base
	}
	if strings.TrimSpace(base) == "" {
		return extra
	}
	return base + "\n\n" + extra
}
