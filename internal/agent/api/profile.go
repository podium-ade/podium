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
		// A shadowed row never runs and writing to it would write to the wrong half, so it
		// stays the one thing this screen shows and will not edit. Delete is what it is for.
		def.Editable = false
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

// CreateSkill stores a new skill in the database. A name a skills/*.yaml already defines is
// refused, because a name is one skill: the answer to "I want to change that one" is now to
// edit it, which UpdateSkill does in place whichever half it lives in.
//
// A new skill goes to the database rather than to a file. That is the one place the two
// halves still differ, and it is a choice about which is the surprising default: writing a
// file into somebody's profile directory the first time they press Create is a bigger
// assumption than storing a row.
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
			"%q is already defined by skills/%s.yaml on this conductor's host: edit that "+
				"skill rather than creating a second one of the same name, or pick another name",
			skill.Name, skill.Name))
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

// UpdateSkill replaces a skill, where it lives.
//
// A skill defined by a skills/<name>.yaml is written back to THAT FILE, and one from the
// database is written to the database. There is deliberately no difference at the UI: a
// human editing a skill should not have to know which half of the profile it came from, and
// "this one is read-only because of where it happens to be stored" is not a rule anybody
// asked for.
//
// The cost is real and is documented on profiles.WriteSkillFile: a save from the browser
// does not preserve the file's comments, and a GitOps deployment will overwrite it.
func (s *AgentService) UpdateSkill(
	ctx context.Context, req *connect.Request[agentv1.UpdateSkillRequest],
) (*connect.Response[agentv1.UpdateSkillResponse], error) {
	skill, err := s.prepareSkill(req.Msg.GetSkill())
	if err != nil {
		return nil, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// The two halves are checked differently because they ARE different: a file skill is
	// substituted into the directory and the whole thing re-merged, while a stored one is
	// looked up in the database. checkMerges does the second and would answer NotFound for
	// the first, which has no row anywhere.
	if s.isFileSkill(skill.Name) {
		return s.updateFileSkill(ctx, skill)
	}
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

// updateFileSkill writes the skill back to the profile directory and reloads from disk.
func (s *AgentService) updateFileSkill(
	ctx context.Context, skill profiles.Skill,
) (*connect.Response[agentv1.UpdateSkillResponse], error) {
	dir := s.profiles.Files().Dir
	// Held to the same rules the directory is held to at start-up, with the edit in place:
	// a change that claims another skill's Slack channel, or a second `linear: true`, is
	// refused before the file is written rather than after it stops the conductor loading.
	if err := s.checkFileMerges(ctx, skill); err != nil {
		return nil, err
	}
	if err := profiles.WriteSkillFile(dir, skill); err != nil {
		if errors.Is(err, profiles.ErrProfileDirReadOnly) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := s.ReloadProfile(ctx); err != nil {
		// The file is written, so reporting a failure would be a lie about what happened.
		// The next reconcile picks it up.
		s.logger.ErrorContext(ctx, "a skill file was written but the running profile could "+
			"not be rebuilt; the conductor is still on the previous one",
			"skill", skill.Name, "error", err)
	}
	s.logger.InfoContext(ctx, "a skill file was changed", "skill", skill.Name,
		"path", profiles.SkillPath(dir, skill.Name), "login", Login(ctx))

	out := skillToProto(skill)
	out.Origin = profiles.OriginFile
	out.Editable = true
	return connect.NewResponse(&agentv1.UpdateSkillResponse{Skill: out}), nil
}

// checkFileMerges re-merges the directory with one file skill replaced, so an edit that
// would stop the profile loading is refused while the file on disk is still the old one.
func (s *AgentService) checkFileMerges(ctx context.Context, skill profiles.Skill) error {
	files := s.profiles.Files()
	next := *files
	next.Skills = make(map[string]profiles.Skill, len(files.Skills))
	for n, sk := range files.Skills {
		next.Skills[n] = sk
	}
	edited := skill
	edited.Origin = profiles.OriginFile
	next.Skills[skill.Name] = edited

	ov, err := readOverrides(ctx, s.store)
	if err != nil {
		return storeError(err)
	}
	stored, err := s.store.ListStoredSkills(ctx)
	if err != nil {
		return storeError(err)
	}
	if _, _, err := profiles.Merge(&next, ov, skillsOf(stored)); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

// isFileSkill reports whether this name is defined by a file in the profile directory.
func (s *AgentService) isFileSkill(name string) bool {
	files := s.profiles.Files()
	if files == nil {
		return false
	}
	_, ok := files.Skills[name]
	return ok
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
		// Nothing stored under that name, so this is either a file skill — which is deleted
		// by removing its file — or nothing at all.
		if s.isFileSkill(name) {
			return s.deleteFileSkill(ctx, name)
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no skill named %q", name))
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

// deleteFileSkill removes the skill's file from the profile directory and reloads.
//
// The rule that a file skill could not be deleted here went with the rule that it could not
// be edited here: both amounted to "the UI shows you a definition it will not let you touch",
// which is not a boundary worth having. The file is the definition, so deleting the skill
// means deleting the file.
func (s *AgentService) deleteFileSkill(
	ctx context.Context, name string,
) (*connect.Response[agentv1.DeleteSkillResponse], error) {
	dir := s.profiles.Files().Dir
	// The same load-time rules a directory is held to: a profile whose default_skill names
	// the skill being deleted would not load, so it is refused before the file is removed
	// rather than after.
	next := *s.profiles.Files()
	next.Skills = make(map[string]profiles.Skill, len(next.Skills))
	for n, sk := range s.profiles.Files().Skills {
		if n != name {
			next.Skills[n] = sk
		}
	}
	ov, err := readOverrides(ctx, s.store)
	if err != nil {
		return nil, storeError(err)
	}
	stored, err := s.store.ListStoredSkills(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	if _, _, err := profiles.Merge(&next, ov, skillsOf(stored)); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	if err := profiles.DeleteSkillFile(dir, name); err != nil {
		if errors.Is(err, profiles.ErrProfileDirReadOnly) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := s.ReloadProfile(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "a skill file was deleted", "skill", name,
		"path", profiles.SkillPath(dir, name), "login", Login(ctx))
	return connect.NewResponse(&agentv1.DeleteSkillResponse{}), nil
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
		// Both halves are editable: a skill from a file is written back to that file. The
		// field stays on the wire because a shadowed stored skill is still not editable.
		Editable: true,
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
