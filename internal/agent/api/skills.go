package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/skills"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// The Agent Skill library, as an API.
//
// There are two sources and they are not equal. A directory under PODIUM_AGENT_SKILLS_DIR is
// a file on the conductor's host, and this API neither writes nor deletes one — it is the
// escape hatch for the case where the database or the browser is not available, and it is how
// this repository loads its own skills. Everything else here is the stored half: a bundle
// somebody uploaded, held in the conductor's database, which the write RPCs own.
//
// The rules a bundle is held to are not restated here. skills.Build is the one validator, and
// a skill uploaded through a browser is refused for exactly the reasons a skill on disk is.

// ListSkills reports every skill this conductor can hand a turn.
func (s *AgentService) ListSkills(
	ctx context.Context, _ *connect.Request[agentv1.ListSkillsRequest],
) (*connect.Response[agentv1.ListSkillsResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no database"))
	}
	dir, err := skills.ListDir(s.skillsDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	stored, err := s.store.ListStoredSkills(ctx)
	if err != nil {
		return nil, storeError(err)
	}

	fromDir := make(map[string]bool, len(dir))
	for _, d := range dir {
		fromDir[d.Name] = true
	}
	users := s.playbooksBySkill()

	// Running first, in name order, exactly as GetProfile reports playbooks: the directory's
	// and the stored ones it does not shadow are one sorted list, because they are one
	// namespace as far as a playbook's skills: is concerned.
	out := make([]*agentv1.AgentSkill, 0, len(dir)+len(stored))
	for _, d := range dir {
		out = append(out, dirSkillToProto(d, users))
	}
	shadowed := make([]*agentv1.AgentSkill, 0)
	for _, row := range stored {
		msg := storedSkillToProto(row, users)
		if fromDir[row.Name] {
			msg.Shadowed = true
			shadowed = append(shadowed, msg)
			continue
		}
		out = append(out, msg)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	out = append(out, shadowed...)

	return connect.NewResponse(&agentv1.ListSkillsResponse{
		Skills:         out,
		SkillsDir:      s.skillsDir,
		MaxBytes:       int64(skills.MaxBytes),
		MaxFiles:       int32(skills.MaxFiles),
		MaxPerPlaybook: int32(skills.MaxSkills),
	}), nil
}

// UploadSkill validates a bundle and stores it.
//
// The name is the SKILL.md's own frontmatter name and is not taken from the request: the
// harness only loads a skill whose frontmatter name matches the directory it is installed
// into, so any other answer would produce a skill that is present and never loadable.
func (s *AgentService) UploadSkill(
	ctx context.Context, req *connect.Request[agentv1.UploadSkillRequest],
) (*connect.Response[agentv1.UploadSkillResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no database, so there is nowhere to put a skill"))
	}
	name, files, err := skills.Parse(req.Msg.GetContent(), req.Msg.GetFilename())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	bundle, err := skills.Build(name, files)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.refuseDirSkill(name); err != nil {
		return nil, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	stored, err := s.store.ListStoredSkills(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	existing, found := findStored(stored, name)
	login := Login(ctx)
	switch {
	case found && !req.Msg.GetReplace():
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf(
			"a skill named %q is already stored — uploaded by %s, %s. Replacing it changes every "+
				"playbook that names it, so it takes an explicit replace", name, existing.UploadedBy,
			existing.UploadedAt.Format(time.RFC3339)))
	case found:
		if err := s.store.ReplaceStoredSkill(ctx, bundle, login); err != nil {
			return nil, storeError(err)
		}
	default:
		if err := s.store.InsertStoredSkill(ctx, bundle, login); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, connect.NewError(connect.CodeAlreadyExists, err)
			}
			return nil, storeError(err)
		}
	}

	// The digest is logged because it is the only thing that identifies which bytes were
	// admitted. It is transport integrity and not provenance — see docs/security.md — but it
	// is what an operator has to compare against later.
	s.logger.InfoContext(ctx, "an agent skill was uploaded", "skill", name,
		"sha256", bundle.SHA256, "files", bundle.Files, "bytes", len(bundle.Document),
		"replaced", found, "filename", req.Msg.GetFilename(), "login", login)

	row := skills.Stored{
		Name:        bundle.Name,
		Description: bundle.Description,
		SHA256:      bundle.SHA256,
		SizeBytes:   int64(len(bundle.Document)),
		FileCount:   bundle.Files,
		Enabled:     !found || existing.Enabled,
		UploadedBy:  login,
		UploadedAt:  time.Now().UTC(),
	}
	return connect.NewResponse(&agentv1.UploadSkillResponse{
		Skill:    storedSkillToProto(row, s.playbooksBySkill()),
		Replaced: found,
	}), nil
}

// SetSkillEnabled takes a stored skill out of service, or puts it back.
//
// Disabling does not quietly narrow a playbook: a playbook that names a disabled skill fails
// its turns saying so. That is the same rule every other check here follows, and it is the
// only one under which "which skills did that turn actually have" has an answer.
func (s *AgentService) SetSkillEnabled(
	ctx context.Context, req *connect.Request[agentv1.SetSkillEnabledRequest],
) (*connect.Response[agentv1.SetSkillEnabledResponse], error) {
	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("set skill enabled: name is required"))
	}
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no database"))
	}
	if err := s.refuseDirSkill(name); err != nil {
		return nil, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	enabled := req.Msg.GetEnabled()
	if err := s.store.SetStoredSkillEnabled(ctx, name, enabled); err != nil {
		return nil, s.skillNotFound(err, name)
	}
	row, err := s.store.StoredSkill(ctx, name)
	if err != nil {
		return nil, storeError(err)
	}
	row.Document = nil
	users := s.playbooksBySkill()
	s.logger.InfoContext(ctx, "an agent skill was enabled or disabled", "skill", name,
		"enabled", enabled, "playbooks", users[name], "login", Login(ctx))
	return connect.NewResponse(&agentv1.SetSkillEnabledResponse{
		Skill: storedSkillToProto(row, users),
	}), nil
}

// DeleteSkill removes a stored skill and its bundle.
//
// A skill a playbook still names is deletable. profiles.Load deliberately does not check that
// a named skill exists — a playbook file has to load on a machine with no skills at all — so
// refusing here would be the only place in Podium where the two disagreed. The playbook's
// turns then fail naming the skill, and ListSkills reports which playbooks name each one so a
// human can see that before pressing the button.
func (s *AgentService) DeleteSkill(
	ctx context.Context, req *connect.Request[agentv1.DeleteSkillRequest],
) (*connect.Response[agentv1.DeleteSkillResponse], error) {
	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("delete skill: name is required"))
	}
	if s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no database"))
	}
	if err := s.refuseDirSkill(name); err != nil {
		return nil, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	users := s.playbooksBySkill()
	if err := s.store.DeleteStoredSkill(ctx, name); err != nil {
		return nil, s.skillNotFound(err, name)
	}
	s.logger.InfoContext(ctx, "an agent skill was deleted", "skill", name,
		"playbooks", users[name], "login", Login(ctx))
	return connect.NewResponse(&agentv1.DeleteSkillResponse{}), nil
}

// refuseDirSkill is the read-only rule, and it is the playbook rule word for word: a file on
// the conductor's host is where that skill is defined, and this API does not write over one.
func (s *AgentService) refuseDirSkill(name string) error {
	if s.skillsDir == "" {
		return nil
	}
	dir, err := skills.ListDir(s.skillsDir)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for _, d := range dir {
		if d.Name != name {
			continue
		}
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%q is a directory in %s=%q on this conductor's host, and the host wins: it is "+
				"read-only here. Edit or remove that directory. If a stored skill of this name "+
				"exists it is shadowed and never runs; delete it", name, skills.DirEnv, s.skillsDir))
	}
	return nil
}

// skillNotFound turns a store miss into the answer the operator needs, which is not simply
// "no": a directory of that name is a different situation from nothing at all.
func (s *AgentService) skillNotFound(err error, name string) error {
	if !errors.Is(err, store.ErrNotFound) {
		return storeError(err)
	}
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("no stored skill named %q", name))
}

// playbooksBySkill is which playbooks name each skill, out of the profile in force. It is
// what makes a disable or a delete a decision rather than a surprise.
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

func findStored(rows []skills.Stored, name string) (skills.Stored, bool) {
	for _, row := range rows {
		if row.Name == name {
			return row, true
		}
	}
	return skills.Stored{}, false
}

func dirSkillToProto(d skills.DirSkill, users map[string][]string) *agentv1.AgentSkill {
	return &agentv1.AgentSkill{
		Name:        d.Name,
		Description: d.Description,
		SizeBytes:   d.SizeBytes,
		FileCount:   int32(d.FileCount),
		// A directory has nothing to turn off, and nothing here can turn it off. Reporting it
		// as enabled is the truth: a playbook that names it gets it.
		Enabled:   true,
		Origin:    skills.OriginDir,
		Editable:  false,
		Problem:   d.Problem,
		Playbooks: users[d.Name],
	}
}

func storedSkillToProto(row skills.Stored, users map[string][]string) *agentv1.AgentSkill {
	out := &agentv1.AgentSkill{
		Name:        row.Name,
		Description: row.Description,
		Sha256:      row.SHA256,
		SizeBytes:   row.SizeBytes,
		FileCount:   int32(row.FileCount),
		Enabled:     row.Enabled,
		Origin:      skills.OriginStored,
		Editable:    true,
		UploadedBy:  row.UploadedBy,
		Playbooks:   users[row.Name],
	}
	if !row.UploadedAt.IsZero() {
		out.UploadedAt = timestamppb.New(row.UploadedAt)
	}
	return out
}

// compile-time proof that the store is a skill library's reader, so a change to one of the
// two signatures is a build failure here rather than a nil at run time.
var _ skills.Reader = (*store.Store)(nil)
