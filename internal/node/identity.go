package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"connectrpc.com/connect"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/proto/podium/v1/podiumv1connect"
)

// identityFile is the node's durable credential inside the data dir.
const identityFile = "identity.json"

// Identity is what Enroll returned, persisted 0600. NodeKey is handed out exactly once by
// the server, so losing this file means enrolling again with a fresh token.
type Identity struct {
	NodeID string `json:"node_id"`
	// SENSITIVE: never log. The server stores only its SHA-256.
	NodeKey string `json:"node_key"`
}

// LoadIdentity reads the identity from dataDir. The second result is false when there is
// none yet, which is the signal to enroll.
func LoadIdentity(dataDir string) (Identity, bool, error) {
	path := filepath.Join(dataDir, identityFile)
	raw, err := os.ReadFile(path) //nolint:gosec // path is derived from the operator's data_dir
	if errors.Is(err, os.ErrNotExist) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, fmt.Errorf("read node identity %s: %w", path, err)
	}
	var id Identity
	if err := json.Unmarshal(raw, &id); err != nil {
		return Identity{}, false, fmt.Errorf("parse node identity %s: %w", path, err)
	}
	if id.NodeID == "" || id.NodeKey == "" {
		return Identity{}, false, fmt.Errorf("node identity %s is incomplete; delete it and enroll again", path)
	}
	return id, true, nil
}

// SaveIdentity writes the identity 0600. It is written before the first stream so a crash
// never strands a node key that only the server-side hash remembers.
func SaveIdentity(dataDir string, id Identity) error {
	path := filepath.Join(dataDir, identityFile)
	raw, err := json.Marshal(id)
	if err != nil {
		return fmt.Errorf("encode node identity: %w", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write node identity %s: %w", path, err)
	}
	return nil
}

// enroll exchanges the configured enrollment token for a durable identity and persists it.
func enroll(ctx context.Context, client podiumv1connect.NodeServiceClient, cfg Config, facts HostFacts, logger *slog.Logger) (Identity, error) {
	if cfg.EnrollToken == "" {
		return Identity{}, fmt.Errorf("no identity in %s and no enrollment token: run "+
			"`podium node enroll-token` and pass it as PODIUM_NODE_ENROLL_TOKEN",
			filepath.Join(cfg.DataDir, identityFile))
	}

	res, err := client.Enroll(ctx, connect.NewRequest(&podiumv1.EnrollRequest{
		Token:         cfg.EnrollToken,
		Hostname:      facts.Hostname,
		Arch:          runtime.GOARCH,
		Os:            runtime.GOOS,
		CpuCores:      facts.CPUCores,
		MemoryMb:      facts.MemoryMB,
		DockerVersion: facts.DockerVersion,
		Labels:        cfg.Labels,
	}))
	if err != nil {
		return Identity{}, fmt.Errorf("enroll with the control plane: %w", err)
	}

	id := Identity{NodeID: res.Msg.GetNodeId(), NodeKey: res.Msg.GetNodeKey()}
	if err := SaveIdentity(cfg.DataDir, id); err != nil {
		return Identity{}, err
	}
	logger.InfoContext(ctx, "node enrolled", "node_id", id.NodeID, "labels", cfg.Labels)
	return id, nil
}
