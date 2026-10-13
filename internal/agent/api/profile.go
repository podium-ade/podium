package api

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/pkg/spec"
)

// GetProfile reports the profile in force and every playbook in full.
//
// A playbook's secrets are reported by NAME, which is all a playbook file holds either. No value
// reaches this response: Podium has no endpoint that reads a secret's value back, and a
// playbook naming one is not a privilege — a task spec names secrets exactly the same way.
func (s *AgentService) GetProfile(
	ctx context.Context, _ *connect.Request[agentv1.GetProfileRequest],
) (*connect.Response[agentv1.GetProfileResponse], error) {
	cur, files, err := s.profilePair()
	if err != nil {
		return nil, err
	}
	saved, err := s.profileSave(ctx)
	if err != nil {
		return nil, storeError(err)
	}

	out := make([]*agentv1.PlaybookDefinition, 0, len(cur.Playbooks))
	for _, name := range cur.PlaybookNames() {
		out = append(out, playbookToProto(cur.Playbooks[name]))
	}

	return connect.NewResponse(&agentv1.GetProfileResponse{
		Profile:     profileToProto(cur, files, saved),
		Playbooks:   out,
		StaleReason: s.staleReason(),
	}), nil
}

// UpdateProfile stores the active assistant definition and swaps the rebuilt profile in.
// The request is the whole definition. Playbooks stay files. A legacy override row is
// removed on the first save so the two cannot both apply.
func (s *AgentService) UpdateProfile(
	ctx context.Context, req *connect.Request[agentv1.UpdateProfileRequest],
) (*connect.Response[agentv1.UpdateProfileResponse], error) {
	files := s.profiles.Files()
	if files == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	def, err := definitionFromRequest(req.Msg, Login(ctx), time.Now().UTC())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cat, found, err := readCatalog(ctx, s.store)
	if err != nil {
		return nil, storeError(err)
	}
	if found && def.ID != "" && def.ID != cat.ActiveID {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("only the active assistant can be saved here"))
	}
	var next profiles.AssistantCatalog
	if found {
		next, err = cat.Upsert(def)
	} else {
		next, err = cat.Add(def)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	active, ok := next.Active()
	if !ok {
		return nil, connect.NewError(connect.CodeInternal, errors.New("the assistant catalog has no active definition"))
	}
	if _, err := profiles.ApplyDefinition(files, active); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.store.PutSetting(ctx, assistantsSettingKey, next); err != nil {
		return nil, storeError(err)
	}
	if err := s.store.DeleteSetting(ctx, overridesSettingKey); err != nil {
		return nil, storeError(err)
	}
	if err := s.reloadProfile(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.logger.InfoContext(ctx, "the assistant was saved", "login", active.UpdatedBy, "id", active.ID, "name", active.Name)

	cur, _, err := s.profilePair()
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentv1.UpdateProfileResponse{
		Profile: profileToProto(cur, files, profileSave{
			ID: active.ID, UpdatedBy: active.UpdatedBy, UpdatedAt: active.UpdatedAt,
		}),
	}), nil
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

// profileSave is who last wrote the assistant the screen is showing. ID is empty until
// a definition has been saved. Overridden is the legacy overlay's fields, and only while
// that overlay is still what a turn runs from.
type profileSave struct {
	ID         string
	UpdatedBy  string
	UpdatedAt  time.Time
	Overridden []string
}

func (s *AgentService) profileSave(ctx context.Context) (profileSave, error) {
	cat, found, err := readCatalog(ctx, s.store)
	if err != nil {
		return profileSave{}, err
	}
	if found {
		def, ok := cat.Active()
		if !ok {
			return profileSave{}, nil
		}
		return profileSave{ID: def.ID, UpdatedBy: def.UpdatedBy, UpdatedAt: def.UpdatedAt}, nil
	}
	ov, err := readOverrides(ctx, s.store)
	if err != nil {
		return profileSave{}, err
	}
	return profileSave{UpdatedBy: ov.UpdatedBy, UpdatedAt: ov.UpdatedAt, Overridden: ov.Fields()}, nil
}

func profileToProto(cur, files *profiles.Profile, saved profileSave) *agentv1.AgentProfile {
	assistant := cur.Assistant()
	out := &agentv1.AgentProfile{
		Id:                  saved.ID,
		Name:                cur.Name,
		DisplayName:         cur.DisplayName,
		Model:               cur.Model,
		Agent:               cur.AgentFor(profiles.Playbook{}),
		Effort:              cur.Effort,
		DefaultPlaybook:     cur.DefaultPlaybook,
		ProfileDir:          files.Dir,
		FileDisplayName:     files.DisplayName,
		FileModel:           files.Model,
		FileAgent:           files.Agent,
		FileEffort:          files.Effort,
		FileDefaultPlaybook: files.DefaultPlaybook,
		Overridden:          saved.Overridden,
		UpdatedBy:           saved.UpdatedBy,
		Skills:              assistant.Skills,
		MaxTurns:            int32(assistant.MaxTurns),
		SystemPrompt:        cur.SystemPrompt,
		FileSystemPrompt:    files.SystemPrompt,
		McpServers:          assistant.MCPServers,
		FileMcpServers:      files.MCPServers,
		FileSkills:          files.Skills,
		FileMaxTurns:        int32(files.MaxTurns),
		Timeout:             assistant.Timeout.String(),
		GitName:             cur.Git.Name,
		GitEmail:            cur.Git.Email,
		FileGitName:         files.Git.Name,
		FileGitEmail:        files.Git.Email,
	}
	if files.Timeout > 0 {
		out.FileTimeout = files.Timeout.String()
	}
	if !saved.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(saved.UpdatedAt)
	}
	return out
}

// definitionFromRequest is the assistant a save asked to store. Every field is the
// value, including an empty skill list and a cap of zero.
func definitionFromRequest(msg *agentv1.UpdateProfileRequest, login string, now time.Time) (profiles.AssistantDefinition, error) {
	if msg == nil {
		msg = &agentv1.UpdateProfileRequest{}
	}
	return profiles.AssistantDefinition{
		ID:           msg.GetId(),
		Name:         msg.GetName(),
		DisplayName:  msg.GetDisplayName(),
		SystemPrompt: msg.GetSystemPrompt(),
		Model:        msg.GetModel(),
		Agent:        msg.GetAgent(),
		Effort:       msg.GetEffort(),
		Git:          profiles.GitPersona{Name: msg.GetGitName(), Email: msg.GetGitEmail()},
		Skills:       append([]string(nil), msg.GetSkills()...),
		MCPServers:   append([]string(nil), msg.GetMcpServers()...),
		MaxTurns:     int(msg.GetMaxTurns()),
		Timeout:      msg.GetTimeout(),
		UpdatedBy:    login,
		UpdatedAt:    now,
	}.Prepare()
}

func playbookToProto(s profiles.Playbook) *agentv1.PlaybookDefinition {
	out := &agentv1.PlaybookDefinition{
		Name:          s.Name,
		Image:         s.Image,
		SystemPrompt:  s.SystemPrompt,
		AllowedTools:  s.AllowedTools,
		MaxTurns:      int32(s.MaxTurns),
		Model:         s.Model,
		Agent:         s.Agent,
		Effort:        s.Effort,
		Labels:        s.Labels,
		Priority:      int32(s.Priority),
		SlackChannels: s.SlackChannels,
		Linear:        s.Linear,
		Skills:        s.Skills,
		McpServers:    s.MCPServers,
		Env:           s.Env,
		Interactive:   s.Interactive,
		Docker:        s.Docker,
		Browser:       s.Browser,
		Origin:        "file",
		Editable:      true,
	}
	if s.Timeout > 0 {
		out.Timeout = s.Timeout.String()
	}
	if s.Resources != (spec.Resources{}) {
		out.Resources = &agentv1.PlaybookResources{
			Cpu:      s.Resources.CPU,
			MemoryMb: int32(s.Resources.MemoryMB),
			Pids:     int32(s.Resources.PIDs),
		}
	}
	for _, ref := range s.Secrets {
		out.Secrets = append(out.Secrets, &agentv1.PlaybookSecretRef{
			Name: ref.Name, Target: ref.Target, Key: ref.Key,
		})
	}
	for _, ref := range s.UserSecrets {
		out.UserSecrets = append(out.UserSecrets, &agentv1.PlaybookSecretRef{
			Name: ref.Name, Target: ref.Target, Key: ref.Key,
		})
	}
	for _, r := range s.Repos {
		out.Repos = append(out.Repos, &agentv1.PlaybookRepo{
			Name: r.Name, Url: r.URL, DefaultBranch: r.DefaultBranch,
		})
	}
	// Absent rather than empty when the playbook names none: the field means "inherits the
	// profile's persona", and a pair of empty strings would read as a persona of its own.
	if s.Git.Set() {
		out.Git = &agentv1.PlaybookGit{Name: s.Git.Name, Email: s.Git.Email}
	}
	return out
}
