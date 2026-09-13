package api

import (
	"context"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/agent/skills"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// ListSkills reports every skill this conductor can hand a turn: the directories under
// PODIUM_AGENT_SKILLS_DIR. A directory that is there and will not load is reported with the
// reason attached rather than hidden.
func (s *AgentService) ListSkills(
	_ context.Context, _ *connect.Request[agentv1.ListSkillsRequest],
) (*connect.Response[agentv1.ListSkillsResponse], error) {
	dir, err := skills.ListDir(s.skillsDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	users := s.playbooksBySkill()
	out := make([]*agentv1.AgentSkill, 0, len(dir))
	for _, d := range dir {
		out = append(out, dirSkillToProto(d, users))
	}
	return connect.NewResponse(&agentv1.ListSkillsResponse{
		Skills:         out,
		SkillsDir:      s.skillsDir,
		MaxBytes:       int64(skills.MaxBytes),
		MaxFiles:       int32(skills.MaxFiles),
		MaxPerPlaybook: int32(skills.MaxSkills),
	}), nil
}

// playbooksBySkill is which playbooks name each skill, out of the profile in force.
func (s *AgentService) playbooksBySkill() map[string][]string {
	out := map[string][]string{}
	if s.profiles == nil {
		return out
	}
	profile := s.profiles.Current()
	if profile == nil {
		return out
	}
	for _, name := range profile.PlaybookNames() {
		for _, skill := range profile.Playbooks[name].Skills {
			out[skill] = append(out[skill], name)
		}
	}
	return out
}

func dirSkillToProto(d skills.DirSkill, users map[string][]string) *agentv1.AgentSkill {
	return &agentv1.AgentSkill{
		Name:        d.Name,
		Description: d.Description,
		Sha256:      d.SHA256,
		SizeBytes:   d.SizeBytes,
		FileCount:   int32(d.FileCount),
		Problem:     d.Problem,
		Playbooks:   users[d.Name],
	}
}
