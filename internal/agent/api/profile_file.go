package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"connectrpc.com/connect"

	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// maxProfileFileBytes caps what a browser may write as profile.yaml.
const maxProfileFileBytes = 64 << 10

// GetProfileFile returns profile.yaml as it is on disk.
func (s *AgentService) GetProfileFile(
	_ context.Context, _ *connect.Request[agentv1.GetProfileFileRequest],
) (*connect.Response[agentv1.GetProfileFileResponse], error) {
	if err := s.requireProfileDir(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.profileDir, "profile.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // operator-owned profile dir
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("read %s: %w", path, err))
	}
	return connect.NewResponse(&agentv1.GetProfileFileResponse{Content: string(raw), Path: path}), nil
}

// UpdateProfileFile replaces profile.yaml and re-reads the profile directory. The old file is
// put back when the new one does not load, so a bad save leaves the conductor as it was.
func (s *AgentService) UpdateProfileFile(
	ctx context.Context, req *connect.Request[agentv1.UpdateProfileFileRequest],
) (*connect.Response[agentv1.UpdateProfileFileResponse], error) {
	content := req.Msg.GetContent()
	if len(content) > maxProfileFileBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("profile.yaml is %d bytes; the limit is %d", len(content), maxProfileFileBytes))
	}
	if err := s.requireProfileDir(); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	path := filepath.Join(s.profileDir, "profile.yaml")
	info, err := os.Stat(path)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	old, err := os.ReadFile(path) //nolint:gosec // operator-owned profile dir
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := writeFileAtomic(path, []byte(content), info.Mode().Perm()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if _, err := ReloadProfileDir(ctx, s.store, s.profiles, s.profileDir); err != nil {
		if rerr := writeFileAtomic(path, old, info.Mode().Perm()); rerr != nil {
			return nil, connect.NewError(connect.CodeInternal,
				fmt.Errorf("%w; and putting the previous profile.yaml back failed: %w", err, rerr))
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.setStaleReason(nil)
	s.logger.InfoContext(ctx, "profile.yaml updated", "login", Login(ctx), "path", path)
	return connect.NewResponse(&agentv1.UpdateProfileFileResponse{}), nil
}

func writeFileAtomic(path string, raw []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
