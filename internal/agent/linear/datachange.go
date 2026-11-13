package linear

// Data change webhooks (docs/agent.md, "On a Linear event"). With resource types ticked on the
// app's webhook, Linear also sends a delivery to the same URL whenever an issue, a comment, a
// project … is created, updated or removed. Nothing in the agent app acts on one; it is handed
// to an EventSink, which stores it for the routines that run on that event, and the routine
// scheduler runs them.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

// Data change actions, as Linear names them.
const (
	ActionCreate = "create"
	ActionUpdate = "update"
	ActionRemove = "remove"
)

// deliveryTypeData is a data change as the webhook_deliveries_total type label says it: one
// value for every entity, so the label's values stay few.
const deliveryTypeData = "data"

// dataChangeTimeout bounds the sink's write for one delivery. The write is off the request, but
// one stuck write would hold every delivery queued behind it.
const dataChangeTimeout = 10 * time.Second

// dataChangeQueue is the queue data changes are handed over on: one, so they reach the sink in
// the order Linear sent them, and never behind a session's work.
const dataChangeQueue = "\x00data-change"

// DataChange is one data change delivery, as an EventSink is handed it.
type DataChange struct {
	// DeliveryID is Linear's id for the delivery, the same on a retry. A delivery that came
	// without one is named by a hash of its body, which a retry also repeats.
	DeliveryID string
	// Type is the entity ("Issue", "Comment", …) and Action create, update or remove.
	Type   string
	Action string
	// TeamKey is the key of the team the entity is in, as Linear sent it or as Linear answered
	// for the team's id; "" when the entity names no team or the key could not be learned.
	TeamKey string
	// Payload is the delivery without anything secret: action, type, data, actor, url,
	// updatedFrom and createdAt.
	Payload    json.RawMessage
	ReceivedAt time.Time
}

// Event is the change as a routine names it: "Issue.create".
func (d DataChange) Event() string { return d.Type + "." + d.Action }

// EventSink takes the data changes the app receives. The conductor's store implements it,
// keeping one for each routine that runs on it, and reports how many it kept.
type EventSink interface {
	LinearDataChange(ctx context.Context, d DataChange) (int, error)
}

// payloadActor is who made a data change: a person ("user"), or an OAuth client or
// integration. Linear may send none.
// The email Linear may add is not read, so it is never stored: a run needs who, not how to
// reach them.
type payloadActor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// dataTeam is as much of a data change's entity as says which team it is in. An issue names
// its team; a comment names its issue, which names the team.
type dataTeam struct {
	TeamID string `json:"teamId"`
	Team   *struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	} `json:"team"`
	Issue *struct {
		TeamID string `json:"teamId"`
		Team   *struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"team"`
	} `json:"issue"`
}

// team is the entity's team id and key, either of them "" when the payload does not say.
func (t dataTeam) team() (id, key string) {
	id = t.TeamID
	if t.Team != nil {
		id, key = cmpOr(id, t.Team.ID), t.Team.Key
	}
	if t.Issue != nil {
		id = cmpOr(id, t.Issue.TeamID)
		if t.Issue.Team != nil {
			id, key = cmpOr(id, t.Issue.Team.ID), cmpOr(key, t.Issue.Team.Key)
		}
	}
	return id, key
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// dataChange reports whether a delivery is a data change: a type none of the agent app's own,
// an action of create, update or remove, and an entity.
func dataChange(p *webhookPayload) bool {
	switch p.Type {
	case "", TypeAgentSession, TypeAppNotification, TypePermissionChange, TypeOAuthApp:
		return false
	}
	switch p.Action {
	case ActionCreate, ActionUpdate, ActionRemove:
	default:
		return false
	}
	data := strings.TrimSpace(string(p.Data))
	return strings.HasPrefix(data, "{")
}

// handleDataChange hands a data change to the sink, off the request: the write is a database
// round trip, and the team's key can take a call to Linear, while Linear waits five seconds
// for an answer. A change the app made itself is not handed over, so a routine that acts
// through the app cannot start itself.
func (s *AppSource) handleDataChange(p *webhookPayload, deliveryID string, body []byte) (outcome, reason string, level slog.Level) {
	if s.changes == nil {
		return DeliveryIgnored, "no routine runs on Linear events here", slog.LevelInfo
	}
	if s.Revoked() {
		return DeliveryIgnored, "revoked", slog.LevelInfo
	}
	if p.Actor != nil && p.Actor.ID != "" && p.Actor.ID == s.appUserID {
		return DeliveryIgnored, "the app's own change", slog.LevelInfo
	}
	if deliveryID == "" {
		sum := sha256.Sum256(body)
		deliveryID = "sha256:" + hex.EncodeToString(sum[:])
	}
	envelope := map[string]any{
		"action": p.Action, "type": p.Type, "data": p.Data, "actor": p.Actor, "url": p.URL,
		"updatedFrom": rawOrNil(p.UpdatedFrom),
	}
	if !p.CreatedAt.IsZero() {
		envelope["createdAt"] = p.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return DeliveryIgnored, "payload could not be encoded", slog.LevelWarn
	}
	var t dataTeam
	_ = json.Unmarshal(p.Data, &t)
	teamID, teamKey := t.team()
	change := DataChange{
		DeliveryID: deliveryID, Type: p.Type, Action: p.Action, Payload: payload, ReceivedAt: s.now().UTC(),
	}
	sink := s.changes
	s.enqueue(dataChangeQueue, func(work context.Context) {
		ctx, cancel := context.WithTimeout(work, dataChangeTimeout)
		defer cancel()
		change.TeamKey = teamKey
		if change.TeamKey == "" {
			change.TeamKey = s.teamKey(ctx, teamID)
		}
		n, err := sink.LinearDataChange(ctx, change)
		if err != nil {
			// Linear was answered already, and a retry would be dropped as a duplicate: the
			// change is lost to the routines, and said so.
			s.logger.WarnContext(ctx, "linear: storing a data change for routines failed",
				"delivery", change.DeliveryID, "event", change.Event(), "error", err)
			return
		}
		if n > 0 {
			s.logger.InfoContext(ctx, "linear: a data change is waiting for routines",
				"delivery", change.DeliveryID, "event", change.Event(), "routines", n)
		}
	})
	return DeliveryAccepted, "data change", slog.LevelInfo
}

// rawOrNil is raw, or nil when it is empty or null, so an absent updatedFrom stays absent.
func rawOrNil(raw json.RawMessage) any {
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		return nil
	}
	return raw
}

// teamKey is a team's key, asked of Linear once per team for accountTTL, or "" when it cannot
// be learned. A data change names its team by id, and a routine names teams by key.
func (s *AppSource) teamKey(ctx context.Context, teamID string) string {
	if teamID == "" {
		return ""
	}
	now := s.now()
	s.mu.Lock()
	known, ok := s.teamKeys[teamID]
	s.mu.Unlock()
	if ok && now.Sub(known.at) < accountTTL {
		return known.key
	}
	key, err := s.client.TeamKey(ctx, teamID)
	if err != nil {
		s.logger.DebugContext(ctx, "linear: reading a team's key failed", "team", teamID, "error", err)
		return ""
	}
	s.mu.Lock()
	for id, t := range s.teamKeys {
		if now.Sub(t.at) >= accountTTL {
			delete(s.teamKeys, id)
		}
	}
	s.teamKeys[teamID] = teamKeyAt{key: key, at: now}
	s.mu.Unlock()
	return key
}

// teamKeyAt is a team's key, and when Linear was asked.
type teamKeyAt struct {
	key string
	at  time.Time
}
