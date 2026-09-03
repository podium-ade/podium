package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alvaroibarguen/podium/internal/agent/memory"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// notConfigured is what all three memory RPCs answer on an install with no Hindsight. It is
// FailedPrecondition rather than Unimplemented: the RPC exists and the deployment does not
// have the thing it needs, which is something an operator fixes.
func notConfigured() error {
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("memory is not configured on this host"))
}

// ListMemories pages through the shared memory, newest first.
func (s *AgentService) ListMemories(
	ctx context.Context, req *connect.Request[agentv1.ListMemoriesRequest],
) (*connect.Response[agentv1.ListMemoriesResponse], error) {
	if s.memory == nil {
		return nil, notConfigured()
	}
	items, next, err := s.memory.List(ctx, req.Msg.GetCursor(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, s.memoryError(ctx, "list memories", err)
	}
	return connect.NewResponse(&agentv1.ListMemoriesResponse{
		Items:      memoriesToProto(items),
		NextCursor: next,
	}), nil
}

// SearchMemories is the semantic search behind the Memory tab's search box.
func (s *AgentService) SearchMemories(
	ctx context.Context, req *connect.Request[agentv1.SearchMemoriesRequest],
) (*connect.Response[agentv1.SearchMemoriesResponse], error) {
	if s.memory == nil {
		return nil, notConfigured()
	}
	query := strings.TrimSpace(req.Msg.GetQuery())
	if query == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("search memories: query is required"))
	}
	items, err := s.memory.Recall(ctx, query, int(req.Msg.GetLimit()))
	if err != nil {
		return nil, s.memoryError(ctx, "search memories", err)
	}
	return connect.NewResponse(&agentv1.SearchMemoriesResponse{Items: memoriesToProto(items)}), nil
}

// DeleteMemory takes one memory out of every future recall.
//
// The memory engine has no single-memory delete, so this is its curation tombstone: the
// memory is excluded from recall, from consolidation and from the graph, the observations
// derived from it are pruned, and the row is kept in an archive. What the operator asked
// for holds — no future turn sees it, and neither does this UI — but the record of it
// having existed is not destroyed.
func (s *AgentService) DeleteMemory(
	ctx context.Context, req *connect.Request[agentv1.DeleteMemoryRequest],
) (*connect.Response[agentv1.DeleteMemoryResponse], error) {
	if s.memory == nil {
		return nil, notConfigured()
	}
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("delete memory: id is required"))
	}
	if err := s.memory.Forget(ctx, id); err != nil {
		return nil, s.memoryError(ctx, "delete memory", err)
	}
	s.logger.InfoContext(ctx, "a memory was forgotten", "memory_id", id, "login", Login(ctx))
	return connect.NewResponse(&agentv1.DeleteMemoryResponse{}), nil
}

// memoryError maps the client's sentinels onto Connect codes. A wrong API key is
// Unavailable rather than PermissionDenied on purpose: the caller's own credentials are
// fine, and telling an operator "you may not do that" when the truth is "this host's
// memory key is wrong" sends them to the wrong place.
func (s *AgentService) memoryError(ctx context.Context, what string, err error) error {
	switch {
	case errors.Is(err, memory.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s: %w", what, err))
	case errors.Is(err, memory.ErrUnauthorized):
		s.logger.ErrorContext(ctx, "the memory API key is refused; check PODIUM_AGENT_MEMORY_API_KEY "+
			"against the memory service's own key", "error", err)
		return connect.NewError(connect.CodeUnavailable,
			errors.New(what+": this host's memory API key is refused. An operator should check "+
				"PODIUM_AGENT_MEMORY_API_KEY against the memory service's key."))
	}
	s.logger.WarnContext(ctx, "a memory call failed", "what", what, "error", err)
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s: %w", what, err))
}

func memoriesToProto(items []memory.Memory) []*agentv1.Memory {
	out := make([]*agentv1.Memory, 0, len(items))
	for _, m := range items {
		p := &agentv1.Memory{
			Id:         m.ID,
			Text:       m.Text,
			FactType:   m.FactType,
			Tags:       m.Tags,
			Metadata:   m.Metadata,
			Entities:   m.Entities,
			Context:    m.Context,
			DocumentId: m.DocumentID,
		}
		if !m.LearnedAt.IsZero() {
			p.CreatedAt = timestamppb.New(m.LearnedAt)
		}
		out = append(out, p)
	}
	return out
}
