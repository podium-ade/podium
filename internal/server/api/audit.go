package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"

	podiumv1 "github.com/podium-ade/podium/internal/proto/podium/v1"
	"github.com/podium-ade/podium/internal/transport"
)

// auditWriter is the store's half of AuditService.
type auditWriter interface {
	Audit(ctx context.Context, actor, action, subject string, details map[string]any) error
}

// maxAuditDetailsBytes bounds what one row may carry. A sharing decision is a few fields.
const maxAuditDetailsBytes = 8 << 10

// AuditService is the conductor's way into audit_log. See proto/podium/v1/audit.proto.
type AuditService struct {
	store  auditWriter
	logger *slog.Logger
}

// NewAuditService returns the handlers.
func NewAuditService(st auditWriter, logger *slog.Logger) *AuditService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AuditService{store: st, logger: logger}
}

// conductorActions is every action the conductor may record. A closed list rather than a
// prefix check alone, so a typo in the conductor is an error here instead of a new row kind.
var conductorActions = map[string]bool{
	"chat.share":          true,
	"chat.unshare":        true,
	"chat.general_access": true,
	"chat.link_reset":     true,
	"chat.join":           true,
	"chat.leave":          true,
	"chat.delete":         true,
	"chat.start":          true,
	// Routines (docs/agent.md, "Routines"): who set one up, changed what it is told, paused,
	// resumed, ran or stopped it, and the per-person limits.
	"routine.create": true,
	"routine.update": true,
	"routine.delete": true,
	"routine.pause":  true,
	"routine.resume": true,
	"routine.run":    true,
	"routine.cancel": true,
	"routine.limits": true,
}

// RecordAudit writes one row, for the conductor only.
func (s *AuditService) RecordAudit(
	ctx context.Context, req *connect.Request[podiumv1.RecordAuditRequest],
) (*connect.Response[podiumv1.RecordAuditResponse], error) {
	id, ok := transport.From(ctx)
	if !ok || id.Kind != transport.KindAgent {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("record audit: only the conductor records audit entries"))
	}
	action := req.Msg.GetAction()
	if !conductorActions[action] {
		return nil, connect.NewError(connect.CodePermissionDenied,
			errors.New("record audit: the conductor may not record "+action))
	}
	actor, subject := strings.TrimSpace(req.Msg.GetActor()), strings.TrimSpace(req.Msg.GetSubject())
	if actor == "" || subject == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("record audit: actor and subject are required"))
	}
	var details map[string]any
	if raw := req.Msg.GetDetailsJson(); raw != "" {
		if len(raw) > maxAuditDetailsBytes {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("record audit: details are too large"))
		}
		if err := json.Unmarshal([]byte(raw), &details); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("record audit: details_json is not a JSON object"))
		}
	}
	if err := s.store.Audit(ctx, actor, action, subject, details); err != nil {
		s.logger.ErrorContext(ctx, "recording the conductor's audit entry failed", "action", action, "error", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("record audit: the entry could not be written"))
	}
	return connect.NewResponse(&podiumv1.RecordAuditResponse{}), nil
}
