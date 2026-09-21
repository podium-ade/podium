package store

import "testing"

func TestSlackChannelID(t *testing.T) {
	id, ok := SlackChannelID("slack:C0123:1725000000.000100")
	if !ok || id != "C0123" {
		t.Fatalf("got %q %v", id, ok)
	}
	if _, ok := SlackChannelID("linear:ENG-1"); ok {
		t.Fatal("a Linear key is not a Slack channel")
	}
	if _, ok := SlackChannelID("odd"); ok {
		t.Fatal("an unshaped key is not a Slack channel")
	}
}
