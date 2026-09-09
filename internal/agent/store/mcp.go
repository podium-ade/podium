package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/alvaroibarguen/podium/internal/agent/mcp"
	db "github.com/alvaroibarguen/podium/internal/agent/store/db"
)

// The MCP server registry. There is no file half to merge with — see internal/agent/mcp —
// so these five methods are the whole of where a turn's MCP servers come from.
//
// No method here reads or writes a token. The credential is a Podium secret in the control
// plane; what this table holds is the registration and the four characters of hint an
// operator recognises it by.

// ListMcpServers returns every registered server, sorted by name.
func (s *Store) ListMcpServers(ctx context.Context) ([]mcp.Server, error) {
	rows, err := s.q.ListMcpServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list mcp servers: %w", err)
	}
	out := make([]mcp.Server, 0, len(rows))
	for _, r := range rows {
		srv, err := mcpServerFromRow(mcpRow(r))
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, nil
}

// McpServer reads one. ErrNotFound means it is not registered.
func (s *Store) McpServer(ctx context.Context, name string) (mcp.Server, error) {
	row, err := s.q.GetMcpServer(ctx, name)
	if noRows(err) {
		return mcp.Server{}, fmt.Errorf("%w: mcp server %s", ErrNotFound, name)
	}
	if err != nil {
		return mcp.Server{}, fmt.Errorf("get mcp server %s: %w", name, err)
	}
	return mcpServerFromRow(mcpRow(row))
}

// InsertMcpServer registers a new server. ErrConflict means the name is already taken.
func (s *Store) InsertMcpServer(ctx context.Context, srv mcp.Server, login string) error {
	n, err := s.q.InsertMcpServer(ctx, db.InsertMcpServerParams{
		Name:        srv.Name,
		Url:         srv.URL,
		Description: srv.Description,
		Enabled:     srv.Enabled,
		CreatedBy:   login,
		UpdatedBy:   login,
		UpdatedAt:   time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("insert mcp server %s: %w", srv.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: mcp server %s", ErrConflict, srv.Name)
	}
	return nil
}

// UpdateMcpServer replaces a registration, leaving the token metadata alone. ErrNotFound
// means there was none to replace.
func (s *Store) UpdateMcpServer(ctx context.Context, srv mcp.Server, login string) error {
	n, err := s.q.UpdateMcpServer(ctx, db.UpdateMcpServerParams{
		Name:        srv.Name,
		Url:         srv.URL,
		Description: srv.Description,
		Enabled:     srv.Enabled,
		UpdatedBy:   login,
		UpdatedAt:   time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("update mcp server %s: %w", srv.Name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: mcp server %s", ErrNotFound, srv.Name)
	}
	return nil
}

// SetMcpServerTokenMeta records that a token was stored, and which version of the secret the
// hint describes. The token itself is already in the control plane by the time this is
// called — the order is deliberate, and api/mcp.go carries the reasoning.
func (s *Store) SetMcpServerTokenMeta(
	ctx context.Context, name, hint, login string, version int32,
) error {
	now := time.Now().UTC()
	n, err := s.q.SetMcpServerTokenMeta(ctx, db.SetMcpServerTokenMetaParams{
		Name:               name,
		TokenHint:          hint,
		TokenSetBy:         login,
		TokenSetAt:         &now,
		TokenSecretVersion: version,
		AuthKind:           mcp.AuthToken,
	})
	if err != nil {
		return fmt.Errorf("set mcp server %s token metadata: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: mcp server %s", ErrNotFound, name)
	}
	return nil
}

// SetMcpServerOAuth records a completed sign-in: who did it, which secret version holds the
// access token, and the bag a refresh needs. It is the OAuth twin of SetMcpServerTokenMeta
// and is exclusive with it — either column set clears the other, so a server has one
// credential and one story about where it came from.
func (s *Store) SetMcpServerOAuth(
	ctx context.Context, name, login string, version int32, o mcp.OAuth,
) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return fmt.Errorf("encode mcp server %s oauth: %w", name, err)
	}
	now := time.Now().UTC()
	n, err := s.q.SetMcpServerOAuth(ctx, db.SetMcpServerOAuthParams{
		Name:               name,
		TokenSetBy:         login,
		TokenSetAt:         &now,
		TokenSecretVersion: version,
		AuthKind:           mcp.AuthOAuth,
		Oauth:              raw,
	})
	if err != nil {
		return fmt.Errorf("set mcp server %s oauth: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: mcp server %s", ErrNotFound, name)
	}
	return nil
}

// RefreshMcpServerOAuth moves a sign-in onto a new access token. It deliberately does not
// touch the provenance: the human who signed in is still the human who signed in, and a
// background pass writing its own name over theirs would lose the only record of who did.
func (s *Store) RefreshMcpServerOAuth(
	ctx context.Context, name string, version int32, o mcp.OAuth,
) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return fmt.Errorf("encode mcp server %s oauth: %w", name, err)
	}
	n, err := s.q.RefreshMcpServerOAuth(ctx, db.RefreshMcpServerOAuthParams{
		Name:               name,
		TokenSecretVersion: version,
		Oauth:              raw,
	})
	if err != nil {
		return fmt.Errorf("refresh mcp server %s oauth: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: mcp server %s", ErrNotFound, name)
	}
	return nil
}

// ClearMcpServerTokenMeta forgets a token that has been removed from the control plane.
func (s *Store) ClearMcpServerTokenMeta(ctx context.Context, name, login string) error {
	n, err := s.q.ClearMcpServerTokenMeta(ctx, db.ClearMcpServerTokenMetaParams{
		Name:      name,
		UpdatedBy: login,
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("clear mcp server %s token metadata: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: mcp server %s", ErrNotFound, name)
	}
	return nil
}

// DeleteMcpServer removes a registration. ErrNotFound means it was not registered; the
// caller is what deletes the secret, because a row that is gone must not leave a credential
// behind it.
func (s *Store) DeleteMcpServer(ctx context.Context, name string) error {
	n, err := s.q.DeleteMcpServer(ctx, name)
	if err != nil {
		return fmt.Errorf("delete mcp server %s: %w", name, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: mcp server %s", ErrNotFound, name)
	}
	return nil
}

// mcpRow is the shape the read queries share. sqlc emits one struct per query, so this is
// what lets one conversion serve all of them — which only works while the field names and
// their order match the generated rows exactly. That is also why `Url` is spelled the way
// the generator spells it and not the way Go would.
type mcpRow struct {
	Name string
	//nolint:revive // the name has to match sqlc's generated row field for the conversion above
	Url                string
	Description        string
	Enabled            bool
	TokenHint          string
	TokenSetBy         string
	TokenSetAt         *time.Time
	TokenSecretVersion int32
	AuthKind           string
	Oauth              []byte
	CreatedBy          string
	UpdatedBy          string
	UpdatedAt          time.Time
}

func mcpServerFromRow(r mcpRow) (mcp.Server, error) {
	out := mcp.Server{
		Name:               r.Name,
		URL:                r.Url,
		Description:        r.Description,
		Enabled:            r.Enabled,
		TokenHint:          r.TokenHint,
		TokenSetBy:         r.TokenSetBy,
		TokenSecretVersion: r.TokenSecretVersion,
		AuthKind:           r.AuthKind,
		CreatedBy:          r.CreatedBy,
		UpdatedBy:          r.UpdatedBy,
		UpdatedAt:          r.UpdatedAt.UTC(),
	}
	if r.TokenSetAt != nil {
		out.TokenSetAt = r.TokenSetAt.UTC()
	}
	// A row whose oauth column no longer decodes is an error naming it rather than a server
	// that silently lost its sign-in: the alternative is a turn authenticating with a token
	// nothing can refresh, and nobody being told why it stopped working.
	if len(r.Oauth) > 0 {
		var o mcp.OAuth
		if err := json.Unmarshal(r.Oauth, &o); err != nil {
			return mcp.Server{}, fmt.Errorf("decode mcp server %s oauth: %w", r.Name, err)
		}
		out.OAuth = &o
	}
	return out, nil
}
