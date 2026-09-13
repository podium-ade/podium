package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	ov, err := readOverrides(ctx, s.store)
	if err != nil {
		return nil, storeError(err)
	}

	out := make([]*agentv1.PlaybookDefinition, 0, len(cur.Playbooks))
	for _, name := range cur.PlaybookNames() {
		out = append(out, playbookToProto(cur.Playbooks[name]))
	}

	return connect.NewResponse(&agentv1.GetProfileResponse{
		Profile:     profileToProto(cur, files, ov),
		Playbooks:   out,
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
		DisplayName:     req.Msg.GetDisplayName(),
		Model:           req.Msg.GetModel(),
		Agent:           req.Msg.GetAgent(),
		Effort:          req.Msg.GetEffort(),
		DefaultPlaybook: req.Msg.GetDefaultPlaybook(),
		UpdatedBy:       Login(ctx),
		UpdatedAt:       time.Now().UTC(),
	}.Trim()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	applied, err := profiles.Apply(files, ov)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if ov.DefaultPlaybook != "" {
		if _, ok := applied.Playbooks[ov.DefaultPlaybook]; !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
				"default_playbook %q names no playbook in playbooks/ (have %s)",
				ov.DefaultPlaybook, strings.Join(applied.PlaybookNames(), ", ")))
		}
	}
	if err := s.store.PutSetting(ctx, overridesSettingKey, ov); err != nil {
		return nil, storeError(err)
	}
	if err := s.reloadProfile(ctx); err != nil {
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

func profileToProto(cur, files *profiles.Profile, ov profiles.Overrides) *agentv1.AgentProfile {
	assistant := cur.Assistant()
	out := &agentv1.AgentProfile{
		Name:                files.Name,
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
		Overridden:          ov.Fields(),
		UpdatedBy:           ov.UpdatedBy,
		// The assistant's own two fields. There is no file_* pair for them and no override:
		// they come from profile.yaml and only from there.
		Skills:   assistant.Skills,
		MaxTurns: int32(assistant.MaxTurns),
	}
	if !ov.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(ov.UpdatedAt)
	}
	return out
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
		Origin:        "file",
		Editable:      false,
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
