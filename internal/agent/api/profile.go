package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
	"github.com/alvaroibarguen/podium/pkg/spec"
)

// GetProfile reports the profile in force and every skill in full.
//
// A skill's secrets are reported by NAME, which is all a skill file holds either. No value
// reaches this response: Podium has no endpoint that reads a secret's value back, and a
// skill naming one is not a privilege — a task spec names secrets exactly the same way.
func (s *AgentService) GetProfile(
	ctx context.Context, _ *connect.Request[agentv1.GetProfileRequest],
) (*connect.Response[agentv1.GetProfileResponse], error) {
	cur, files, err := s.profilePair()
	if err != nil {
		return nil, err
	}
	ov, err := readOverrides(ctx, s.store)
	if err != nil {
		return nil, storeError(err)
	}
	stored, err := s.store.ListStoredSkills(ctx)
	if err != nil {
		return nil, storeError(err)
	}

	meta := make(map[string]store.StoredSkill, len(stored))
	for _, row := range stored {
		meta[row.Skill.Name] = row
	}

	out := make([]*agentv1.SkillDefinition, 0, len(cur.Skills)+len(stored))
	for _, name := range cur.SkillNames() {
		def := skillToProto(cur.Skills[name])
		if row, ok := meta[name]; ok && def.GetOrigin() == profiles.OriginStored {
			def.UpdatedBy = row.UpdatedBy
			def.UpdatedAt = timestamppb.New(row.UpdatedAt)
		}
		out = append(out, def)
	}
	// A stored skill a file skill of the same name overrides never runs, so it is not in
	// cur.Skills at all. It is reported anyway, flagged, because a skill an operator saved
	// and cannot see is worse than one they can see is shadowed — and deleting it is the
	// only way to make the list honest again.
	for _, row := range stored {
		if _, isFile := files.Skills[row.Skill.Name]; !isFile {
			continue
		}
		def := skillToProto(row.Skill)
		def.Shadowed = true
		def.UpdatedBy = row.UpdatedBy
		def.UpdatedAt = timestamppb.New(row.UpdatedAt)
		out = append(out, def)
	}

	return connect.NewResponse(&agentv1.GetProfileResponse{
		Profile:     profileToProto(cur, files, ov),
		Skills:      out,
		StaleReason: s.staleReason(),
	}), nil
}

// UpdateProfile stores the overrides and swaps the rebuilt profile in. Every field is an
// override of profile.yaml and an empty one clears it, so "use the file's value" needs no
// second RPC.
func (s *AgentService) UpdateProfile(
	ctx context.Context, req *connect.Request[agentv1.UpdateProfileRequest],
) (*connect.Response[agentv1.UpdateProfileResponse], error) {
	files := s.profiles.Files()
	if files == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	ov := profiles.Overrides{
		DisplayName:      req.Msg.GetDisplayName(),
		Model:            req.Msg.GetModel(),
		Agent:            req.Msg.GetAgent(),
		Effort:           req.Msg.GetEffort(),
		DefaultSkill:     req.Msg.GetDefaultSkill(),
		ChatDefaultSkill: req.Msg.GetChatDefaultSkill(),
		UpdatedBy:        Login(ctx),
		UpdatedAt:        time.Now().UTC(),
	}.Trim()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	stored, err := s.store.ListStoredSkills(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	// Validated before it is stored, and by exactly the rules profile.yaml is held to: a
	// default_skill naming a skill that is not loaded is refused here as it would be at
	// start-up, rather than stored and found at the next restart.
	if _, _, err := profiles.Merge(files, ov, skillsOf(stored)); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.store.PutSetting(ctx, overridesSettingKey, ov); err != nil {
		return nil, storeError(err)
	}
	if err := s.ReloadProfile(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "the agent profile was changed", "login", ov.UpdatedBy,
		"overridden", ov.Fields())

	cur, _, err := s.profilePair()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.UpdateProfileResponse{
		Profile: profileToProto(cur, files, ov),
	}), nil
}

// CreateSkill stores a new skill. A name skills/*.yaml already defines is refused: the
// files are authoritative for the names they hold, so a stored skill of that name would
// never run and storing one would only be a way to be confused later.
func (s *AgentService) CreateSkill(
	ctx context.Context, req *connect.Request[agentv1.CreateSkillRequest],
) (*connect.Response[agentv1.CreateSkillResponse], error) {
	skill, err := s.prepareSkill(req.Msg.GetSkill())
	if err != nil {
		return nil, err
	}
	files := s.profiles.Files()
	if _, ok := files.Skills[skill.Name]; ok {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf(
			"a skills/%s.yaml on this conductor's host already defines %q, and the files win: "+
				"edit that file, or pick another name", skill.Name, skill.Name))
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.checkMerges(ctx, skill, false); err != nil {
		return nil, err
	}
	if err := s.store.InsertStoredSkill(ctx, skill, Login(ctx)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, connect.NewError(connect.CodeAlreadyExists, err)
		}
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.CreateSkillResponse{
		Skill: s.storedResponse(ctx, skill, "a skill was created"),
	}), nil
}

// UpdateSkill replaces a stored skill. A file-defined one is refused, because the file is
// where it is defined and a browser writing over it would put two answers in two places.
func (s *AgentService) UpdateSkill(
	ctx context.Context, req *connect.Request[agentv1.UpdateSkillRequest],
) (*connect.Response[agentv1.UpdateSkillResponse], error) {
	skill, err := s.prepareSkill(req.Msg.GetSkill())
	if err != nil {
		return nil, err
	}
	if err := s.refuseFileSkill(skill.Name); err != nil {
		return nil, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.checkMerges(ctx, skill, true); err != nil {
		return nil, err
	}
	if err := s.store.UpdateStoredSkill(ctx, skill, Login(ctx)); err != nil {
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.UpdateSkillResponse{
		Skill: s.storedResponse(ctx, skill, "a skill was changed"),
	}), nil
}

// DeleteSkill removes a stored skill. Deleting the profile's default is refused by the
// same rule that refuses a profile.yaml naming a default that is not there.
//
// A shadowed row — one a skills/<name>.yaml has since claimed — IS deletable, and has to
// be: it is a stored row, it never runs, and deleting it is the only way to stop the Skills
// screen reporting a skill that does nothing.
func (s *AgentService) DeleteSkill(
	ctx context.Context, req *connect.Request[agentv1.DeleteSkillRequest],
) (*connect.Response[agentv1.DeleteSkillResponse], error) {
	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("delete skill: name is required"))
	}
	if s.profiles == nil || s.profiles.Files() == nil || s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	stored, err := s.store.ListStoredSkills(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	kept := make([]profiles.Skill, 0, len(stored))
	found := false
	for _, row := range stored {
		if row.Skill.Name == name {
			found = true
			continue
		}
		kept = append(kept, row.Skill)
	}
	if !found {
		// Nothing stored under that name. A file skill of that name is a different answer
		// than nothing at all, and the operator needs to be told which.
		if err := s.refuseFileSkill(name); err != nil {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no stored skill named %q", name))
	}
	ov, err := readOverrides(ctx, s.store)
	if err != nil {
		return nil, storeError(err)
	}
	if _, _, err := profiles.Merge(s.profiles.Files(), ov, kept); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if err := s.store.DeleteStoredSkill(ctx, name); err != nil {
		return nil, storeError(err)
	}
	if err := s.ReloadProfile(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "a skill was deleted", "skill", name, "login", Login(ctx))
	return connect.NewResponse(&agentv1.DeleteSkillResponse{}), nil
}

// prepareSkill decodes a request and validates it by exactly the rules a skills/<name>.yaml
// is held to. There is no second validator: profiles.ValidateStoredSkill runs the same
// checks the file loader runs, so a skill made in a browser is refused for the same reasons.
func (s *AgentService) prepareSkill(in *agentv1.SkillDefinition) (profiles.Skill, error) {
	if s.profiles == nil || s.profiles.Files() == nil || s.store == nil {
		return profiles.Skill{}, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	if in == nil {
		return profiles.Skill{}, connect.NewError(connect.CodeInvalidArgument,
			errors.New("skill is required"))
	}
	skill, err := skillFromProto(in)
	if err != nil {
		return profiles.Skill{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	skill, err = profiles.ValidateStoredSkill(skill)
	if err != nil {
		return profiles.Skill{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return skill, nil
}

// refuseFileSkill is the read-only rule: a skills/<name>.yaml is the definition of that
// skill and this API does not write over one. It is also why a stored row of that name is
// only ever deletable — changing one would be editing something that cannot run.
func (s *AgentService) refuseFileSkill(name string) error {
	if _, ok := s.profiles.Files().Skills[name]; ok {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"%q is defined by a skills/%s.yaml on this conductor's host, and the files win: it is "+
				"read-only here. Edit that file and restart the conductor. If a stored skill of "+
				"this name exists it is shadowed and never runs; delete it", name, name))
	}
	return nil
}

// checkMerges refuses a skill that would not survive being loaded alongside the others:
// two skills claiming one Slack channel, two claiming Linear. The check runs before the
// write, so the database can never hold a set that will not load.
func (s *AgentService) checkMerges(ctx context.Context, skill profiles.Skill, replacing bool) error {
	stored, err := s.store.ListStoredSkills(ctx)
	if err != nil {
		return storeError(err)
	}
	next := make([]profiles.Skill, 0, len(stored)+1)
	replaced := false
	for _, row := range stored {
		if row.Skill.Name == skill.Name {
			next = append(next, skill)
			replaced = true
			continue
		}
		next = append(next, row.Skill)
	}
	if !replaced {
		if replacing {
			return connect.NewError(connect.CodeNotFound, fmt.Errorf("no stored skill named %q", skill.Name))
		}
		next = append(next, skill)
	}
	ov, err := readOverrides(ctx, s.store)
	if err != nil {
		return storeError(err)
	}
	if _, _, err := profiles.Merge(s.profiles.Files(), ov, next); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

// storedResponse reloads the live profile and renders what was just written. A failure to
// reload is logged rather than returned: the row is stored, so telling the operator the
// write failed would be a lie, and the next reconcile picks it up.
func (s *AgentService) storedResponse(ctx context.Context, skill profiles.Skill, what string) *agentv1.SkillDefinition {
	login := Login(ctx)
	if err := s.ReloadProfile(ctx); err != nil {
		s.logger.ErrorContext(ctx, "a skill was stored but the running profile could not be rebuilt; "+
			"the conductor is still on the previous one", "skill", skill.Name, "error", err)
	} else {
		s.logger.InfoContext(ctx, what, "skill", skill.Name, "image", skill.Image,
			"secrets", secretNames(skill), "login", login)
	}
	out := skillToProto(skill)
	out.Origin = profiles.OriginStored
	out.Editable = true
	out.UpdatedBy = login
	out.UpdatedAt = timestamppb.New(time.Now().UTC())
	return out
}

// profilePair is the profile in force and the one the files describe.
func (s *AgentService) profilePair() (cur, files *profiles.Profile, err error) {
	if s.profiles == nil || s.store == nil {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	cur, files = s.profiles.Current(), s.profiles.Files()
	if cur == nil || files == nil {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	return cur, files, nil
}

// secretNames is what a write logs about a skill's credentials: the names, never a value.
func secretNames(s profiles.Skill) []string {
	out := make([]string, 0, len(s.Secrets))
	for _, ref := range s.Secrets {
		out = append(out, ref.Name)
	}
	sort.Strings(out)
	return out
}

func profileToProto(cur, files *profiles.Profile, ov profiles.Overrides) *agentv1.AgentProfile {
	out := &agentv1.AgentProfile{
		Name:                 files.Name,
		DisplayName:          cur.DisplayName,
		Model:                cur.Model,
		Agent:                cur.AgentFor(profiles.Skill{}),
		Effort:               cur.Effort,
		DefaultSkill:         cur.DefaultSkill,
		ChatDefaultSkill:     cur.ChatDefaultSkill,
		ProfileDir:           files.Dir,
		FileDisplayName:      files.DisplayName,
		FileModel:            files.Model,
		FileAgent:            files.Agent,
		FileEffort:           files.Effort,
		FileDefaultSkill:     files.DefaultSkill,
		FileChatDefaultSkill: files.ChatDefaultSkill,
		Overridden:           ov.Fields(),
		UpdatedBy:            ov.UpdatedBy,
	}
	if !ov.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(ov.UpdatedAt)
	}
	return out
}

func skillToProto(s profiles.Skill) *agentv1.SkillDefinition {
	out := &agentv1.SkillDefinition{
		Name:          s.Name,
		Image:         s.Image,
		SystemPrompt:  s.SystemPrompt,
		AllowedTools:  s.AllowedTools,
		MaxTurns:      int32(s.MaxTurns),
		Model:         s.Model,
		Agent:         s.Agent,
		Effort:        s.Effort,
		Labels:        s.Labels,
		SlackChannels: s.SlackChannels,
		Linear:        s.Linear,
		Env:           s.Env,
		Origin:        s.Origin,
		Editable:      s.Origin == profiles.OriginStored,
	}
	if s.Timeout > 0 {
		out.Timeout = s.Timeout.String()
	}
	if s.Resources != (spec.Resources{}) {
		out.Resources = &agentv1.SkillResources{
			Cpu:      s.Resources.CPU,
			MemoryMb: int32(s.Resources.MemoryMB),
			Pids:     int32(s.Resources.PIDs),
		}
	}
	for _, ref := range s.Secrets {
		out.Secrets = append(out.Secrets, &agentv1.SkillSecretRef{
			Name: ref.Name, Target: ref.Target, Key: ref.Key,
		})
	}
	for _, r := range s.Repos {
		out.Repos = append(out.Repos, &agentv1.SkillRepo{
			Name: r.Name, Url: r.URL, DefaultBranch: r.DefaultBranch,
		})
	}
	return out
}

func skillFromProto(in *agentv1.SkillDefinition) (profiles.Skill, error) {
	out := profiles.Skill{
		Name:          in.GetName(),
		Image:         in.GetImage(),
		SystemPrompt:  in.GetSystemPrompt(),
		AllowedTools:  in.GetAllowedTools(),
		MaxTurns:      int(in.GetMaxTurns()),
		Model:         in.GetModel(),
		Agent:         strings.TrimSpace(in.GetAgent()),
		Effort:        strings.TrimSpace(in.GetEffort()),
		Labels:        in.GetLabels(),
		SlackChannels: in.GetSlackChannels(),
		Linear:        in.GetLinear(),
		Env:           in.GetEnv(),
	}
	if t := strings.TrimSpace(in.GetTimeout()); t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			return profiles.Skill{}, fmt.Errorf("timeout %q must be a duration like 30m: %w", t, err)
		}
		out.Timeout = spec.Duration(d)
	}
	if r := in.GetResources(); r != nil {
		out.Resources = spec.Resources{
			CPU: r.GetCpu(), MemoryMB: int(r.GetMemoryMb()), PIDs: int(r.GetPids()),
		}
	}
	for _, ref := range in.GetSecrets() {
		out.Secrets = append(out.Secrets, spec.SecretRef{
			Name: ref.GetName(), Target: ref.GetTarget(), Key: ref.GetKey(),
		})
	}
	for _, r := range in.GetRepos() {
		out.Repos = append(out.Repos, profiles.Repo{
			Name: r.GetName(), URL: r.GetUrl(), DefaultBranch: r.GetDefaultBranch(),
		})
	}
	if len(out.Env) == 0 {
		out.Env = nil
	}
	return out, nil
}
