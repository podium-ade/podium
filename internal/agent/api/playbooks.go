package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// maxPlaybookHintChars caps the description a playbook's prompt contributes to the UI. It is one
// line of an operator's own file, not a contract, and a chip's tooltip is all it is for.
const maxPlaybookHintChars = 160

// ListPlaybooks reports who answers a conversation and what that turn may delegate to.
//
// It is the chat's surface and stays minimal: per playbook a name, an image and the first
// line of a prompt the operator wrote. The whole definition — tools, secrets, resources — is
// what GetProfile is for.
func (s *AgentService) ListPlaybooks(
	ctx context.Context, _ *connect.Request[agentv1.ListPlaybooksRequest],
) (*connect.Response[agentv1.ListPlaybooksResponse], error) {
	profile := s.profiles.Current()
	if profile == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	out := make([]*agentv1.Playbook, 0, len(profile.Playbooks))
	for _, name := range profile.PlaybookNames() {
		playbook := profile.Playbooks[name]
		// Resolved, not the playbook's own fields: a playbook that inherits everything would
		// otherwise show a blank where a reader expects a model name.
		runs := profile.Resolve(playbook, profiles.Override{})
		out = append(out, &agentv1.Playbook{
			Name:   name,
			Image:  playbook.Image,
			Hint:   promptHint(playbook.SystemPrompt),
			Agent:  runs.Agent,
			Model:  runs.Model,
			Effort: runs.Effort,
		})
	}
	answers := profile.ResolveAssistant(profiles.Override{})
	_ = ctx
	return connect.NewResponse(&agentv1.ListPlaybooksResponse{
		Playbooks: out,
		Assistant: &agentv1.Assistant{
			DisplayName: profile.DisplayName,
			Agent:       answers.Agent,
			Model:       answers.Model,
			Effort:      answers.Effort,
		},
	}), nil
}

// promptHint is the first non-empty, non-heading line of a prompt, capped. A markdown
// heading is skipped because "# The analyst" says less than the sentence under it.
func promptHint(prompt string) string {
	for line := range strings.Lines(prompt) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		runes := []rune(line)
		if len(runes) > maxPlaybookHintChars {
			return string(runes[:maxPlaybookHintChars]) + "…"
		}
		return line
	}
	return ""
}
