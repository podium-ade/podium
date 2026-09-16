package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/agent/profiles"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
	"github.com/podium-ade/podium/pkg/spec"
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

// CreatePlaybook writes playbooks/<name>.yaml and re-reads the profile directory.
func (s *AgentService) CreatePlaybook(
	ctx context.Context, req *connect.Request[agentv1.CreatePlaybookRequest],
) (*connect.Response[agentv1.CreatePlaybookResponse], error) {
	pb, err := protoToPlaybook(req.Msg.GetPlaybook())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.requireProfileDir(); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	path := filepath.Join(s.profileDir, "playbooks", pb.Name+".yaml")
	if _, err := os.Stat(path); err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists,
			fmt.Errorf("a playbook named %q already exists", pb.Name))
	}
	if err := profiles.WritePlaybook(s.profileDir, pb); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if _, err := ReloadProfileDir(ctx, s.store, s.profiles, s.profileDir); err != nil {
		_ = profiles.RemovePlaybook(s.profileDir, pb.Name)
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.setStaleReason(nil)
	out := s.profiles.Current().Playbooks[pb.Name]
	s.logger.InfoContext(ctx, "playbook created", "login", Login(ctx), "playbook", pb.Name)
	return connect.NewResponse(&agentv1.CreatePlaybookResponse{Playbook: playbookToProto(out)}), nil
}

// UpdatePlaybook replaces playbooks/<name>.yaml.
func (s *AgentService) UpdatePlaybook(
	ctx context.Context, req *connect.Request[agentv1.UpdatePlaybookRequest],
) (*connect.Response[agentv1.UpdatePlaybookResponse], error) {
	pb, err := protoToPlaybook(req.Msg.GetPlaybook())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.requireProfileDir(); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	path := filepath.Join(s.profileDir, "playbooks", pb.Name+".yaml")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, connect.NewError(connect.CodeNotFound,
				fmt.Errorf("no playbook named %q", pb.Name))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := profiles.WritePlaybook(s.profileDir, pb); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if _, err := ReloadProfileDir(ctx, s.store, s.profiles, s.profileDir); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.setStaleReason(nil)
	out := s.profiles.Current().Playbooks[pb.Name]
	s.logger.InfoContext(ctx, "playbook updated", "login", Login(ctx), "playbook", pb.Name)
	return connect.NewResponse(&agentv1.UpdatePlaybookResponse{Playbook: playbookToProto(out)}), nil
}

// DeletePlaybook removes playbooks/<name>.yaml.
func (s *AgentService) DeletePlaybook(
	ctx context.Context, req *connect.Request[agentv1.DeletePlaybookRequest],
) (*connect.Response[agentv1.DeletePlaybookResponse], error) {
	name := strings.TrimSpace(req.Msg.GetName())
	if err := s.requireProfileDir(); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := profiles.RemovePlaybook(s.profileDir, name); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if _, err := ReloadProfileDir(ctx, s.store, s.profiles, s.profileDir); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.setStaleReason(nil)
	s.logger.InfoContext(ctx, "playbook deleted", "login", Login(ctx), "playbook", name)
	return connect.NewResponse(&agentv1.DeletePlaybookResponse{}), nil
}

func (s *AgentService) requireProfileDir() error {
	if s.profiles == nil || s.profileDir == "" {
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile directory"))
	}
	return nil
}

func protoToPlaybook(in *agentv1.PlaybookDefinition) (profiles.Playbook, error) {
	if in == nil {
		return profiles.Playbook{}, errors.New("playbook is required")
	}
	out := profiles.Playbook{
		Name:          strings.TrimSpace(in.Name),
		Image:         strings.TrimSpace(in.Image),
		SystemPrompt:  in.SystemPrompt,
		AllowedTools:  append([]string(nil), in.AllowedTools...),
		MaxTurns:      int(in.MaxTurns),
		Model:         strings.TrimSpace(in.Model),
		Agent:         strings.TrimSpace(in.Agent),
		Effort:        strings.TrimSpace(in.Effort),
		Labels:        append([]string(nil), in.Labels...),
		Priority:      int(in.Priority),
		SlackChannels: append([]string(nil), in.SlackChannels...),
		Linear:        in.Linear,
		Interactive:   in.Interactive,
		Docker:        in.Docker,
		Browser:       in.Browser,
		Skills:        append([]string(nil), in.Skills...),
		MCPServers:    append([]string(nil), in.McpServers...),
		Env:           in.Env,
	}
	if in.Timeout != "" {
		d, err := time.ParseDuration(in.Timeout)
		if err != nil {
			return profiles.Playbook{}, fmt.Errorf("timeout: %w", err)
		}
		out.Timeout = spec.Duration(d)
	}
	if in.Resources != nil {
		out.Resources = spec.Resources{
			CPU:      in.Resources.Cpu,
			MemoryMB: int(in.Resources.MemoryMb),
			PIDs:     int(in.Resources.Pids),
		}
	}
	for _, ref := range in.Secrets {
		out.Secrets = append(out.Secrets, spec.SecretRef{
			Name: ref.Name, Target: ref.Target, Key: ref.Key,
		})
	}
	for _, r := range in.Repos {
		out.Repos = append(out.Repos, profiles.Repo{
			Name: r.Name, URL: r.Url, DefaultBranch: r.DefaultBranch,
		})
	}
	if in.Git != nil && (in.Git.Name != "" || in.Git.Email != "") {
		out.Git = profiles.GitPersona{Name: in.Git.Name, Email: in.Git.Email}
	}
	return out, nil
}
