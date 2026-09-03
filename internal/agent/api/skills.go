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
// Nothing here is a secret: a name, an image, and the first line of a prompt the operator
// wrote. A skill's secrets, tools and resources are deliberately NOT reported — they are
// what the skill file decides and nothing a browser needs to see.
func (s *AgentService) ListSkills(
	ctx context.Context, _ *connect.Request[agentv1.ListSkillsRequest],
) (*connect.Response[agentv1.ListSkillsResponse], error) {
	if s.profile == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	chatDefault := s.profile.ChatSkill()
	out := make([]*agentv1.Skill, 0, len(s.profile.Skills))
	for _, name := range s.profile.SkillNames() {
		skill := s.profile.Skills[name]
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
		ProfileDisplayName: s.profile.DisplayName,
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
