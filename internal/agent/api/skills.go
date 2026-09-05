package api

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// maxSkillHintChars caps the description a skill's prompt contributes to the UI. It is one
// line of an operator's own file, not a contract, and a chip's tooltip is all it is for.
const maxSkillHintChars = 160

// ListSkills reports the profile's skills so the chat can offer the choice.
//
// It is the chat's surface and stays minimal: a name, an image, and the first line of a
// prompt the operator wrote. The whole definition — tools, secrets, resources — is what
// GetProfile is for, and a chip does not need it.
func (s *AgentService) ListSkills(
	ctx context.Context, _ *connect.Request[agentv1.ListSkillsRequest],
) (*connect.Response[agentv1.ListSkillsResponse], error) {
	profile := s.profiles.Current()
	if profile == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	chatDefault := profile.ChatSkill()
	out := make([]*agentv1.Skill, 0, len(profile.Skills))
	for _, name := range profile.SkillNames() {
		skill := profile.Skills[name]
		out = append(out, &agentv1.Skill{
			Name:        name,
			Image:       skill.Image,
			Hint:        promptHint(skill.SystemPrompt),
			ChatDefault: name == chatDefault,
		})
	}
	_ = ctx
	return connect.NewResponse(&agentv1.ListSkillsResponse{
		Skills:             out,
		ProfileDisplayName: profile.DisplayName,
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
		if len(runes) > maxSkillHintChars {
			return string(runes[:maxSkillHintChars]) + "…"
		}
		return line
	}
	return ""
}
