package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/agent/skills"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

func fileOnly(kind string) error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New(
		kind+" are files on this conductor's host; this API does not write them"))
}

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
		Enabled:     true,
		Origin:      "dir",
		Editable:    true,
		Problem:     d.Problem,
		Playbooks:   users[d.Name],
		Markdown:    d.Markdown,
	}
}

// UploadSkill unpacks a zip or a bare SKILL.md into PODIUM_AGENT_SKILLS_DIR.
func (s *AgentService) UploadSkill(
	ctx context.Context, req *connect.Request[agentv1.UploadSkillRequest],
) (*connect.Response[agentv1.UploadSkillResponse], error) {
	if s.skillsDir == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no skills directory"))
	}
	name, files, err := skills.ParseUpload(req.Msg.GetContent(), req.Msg.GetFilename())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	replaced := false
	if _, err := os.Lstat(filepath.Join(s.skillsDir, name)); err == nil {
		replaced = true
	}
	if err := skills.Install(s.skillsDir, name, files, req.Msg.GetReplace()); err != nil {
		if !req.Msg.GetReplace() && replaced {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	dir, err := skills.ListDir(s.skillsDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var out *agentv1.AgentSkill
	users := s.playbooksBySkill()
	for _, d := range dir {
		if d.Name == name {
			out = dirSkillToProto(d, users)
			break
		}
	}
	s.logger.InfoContext(ctx, "skill uploaded", "login", Login(ctx), "skill", name, "replaced", replaced)
	return connect.NewResponse(&agentv1.UploadSkillResponse{Skill: out, Replaced: replaced && req.Msg.GetReplace()}), nil
}

// SetSkillEnabled has no file to flip: a directory skill is on if it is on disk.
func (s *AgentService) SetSkillEnabled(
	context.Context, *connect.Request[agentv1.SetSkillEnabledRequest],
) (*connect.Response[agentv1.SetSkillEnabledResponse], error) {
	return nil, fileOnly("skill enablement")
}

// DeleteSkill removes the skill directory.
func (s *AgentService) DeleteSkill(
	ctx context.Context, req *connect.Request[agentv1.DeleteSkillRequest],
) (*connect.Response[agentv1.DeleteSkillResponse], error) {
	if s.skillsDir == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no skills directory"))
	}
	if err := skills.Remove(s.skillsDir, req.Msg.GetName()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.logger.InfoContext(ctx, "skill deleted", "login", Login(ctx), "skill", req.Msg.GetName())
	return connect.NewResponse(&agentv1.DeleteSkillResponse{}), nil
}
