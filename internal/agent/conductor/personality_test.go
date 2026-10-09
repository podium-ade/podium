package conductor

import "testing"

func TestSessionProfileName(t *testing.T) {
	if got := sessionProfileName("podium", ""); got != "podium" {
		t.Fatalf("a Podium chat keeps the house name, got %q", got)
	}
	if got := sessionProfileName("podium", "night-owl"); got != "night-owl" {
		t.Fatalf("a personal chat labels the session with that voice, got %q", got)
	}
}

func TestSystemPromptWithPersonality(t *testing.T) {
	const base = "You are Podium."
	if got := systemPromptWithPersonality(base, ""); got != base {
		t.Fatalf("a Podium chat leaves the prompt unchanged, got %q", got)
	}
	if got := systemPromptWithPersonality(base, "   "); got != base {
		t.Fatalf("blank instructions leave the prompt unchanged, got %q", got)
	}
	got := systemPromptWithPersonality(base, "  Speak briefly. ")
	want := "You are Podium.\n\nSpeak briefly."
	if got != want {
		t.Fatalf("instructions are appended after the house prompt\n got %q\nwant %q", got, want)
	}
	if got := systemPromptWithPersonality("", "Speak briefly."); got != "Speak briefly." {
		t.Fatalf("instructions stand alone when the house prompt is empty, got %q", got)
	}
}
