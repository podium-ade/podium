package spec

import "testing"

func TestAWorkspaceKeyNamesItsConversation(t *testing.T) {
	key := WorkspacePlaybookKey("sess_01abc", "coder")
	if key != "sess_01abc.coder" {
		t.Fatalf("key %q", key)
	}
	if got, ok := WorkspaceConversation(key); !ok || got != "sess_01abc" {
		t.Fatalf("conversation %q %v", got, ok)
	}
	if got := WorkspacePlaybookKey("sess_01abc", ""); got != "sess_01abc" {
		t.Fatalf("no playbook: %q", got)
	}
	for _, k := range []string{"sess_01abc", ".coder", "sess_01abc."} {
		if _, ok := WorkspaceConversation(k); ok {
			t.Fatalf("%q is not a playbook's key", k)
		}
	}
}
