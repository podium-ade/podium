package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/podium-ade/podium/internal/agent/chat"
	"github.com/podium-ade/podium/internal/agent/conductor"
	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// maxChatMessageBytes caps one human message. The brief cap is 96 KiB of base64 for the
// whole conversation — 72 KiB of JSON — so a single message larger than this could never be
// answered anyway, and refusing it here says so instead of failing the turn.
const maxChatMessageBytes = 32 << 10

// ChatSource is the part of the chat source the handlers use: the write paths, and the
// live fan-out. Reads go to the store directly.
//
// The pull-request writes are here rather than on the store because the point of them is
// the frame: every browser watching the conversation has to see the list change, not only
// the one that pressed the button.
type ChatSource interface {
	SendMessage(ctx context.Context, req chat.SendRequest) (chat.SendResult, error)
	// QueueChanged redraws a chat's queue on every screen showing it.
	QueueChanged(chatID string)
	Subscribe(ctx context.Context, chatID string) *chat.Subscriber
	Running(ctx context.Context, chatID string) (bool, error)
	Awaiting(chatID string) bool
	AttachPullRequest(ctx context.Context, chatID, login, url string) ([]store.ChatPullRequest, error)
	DetachPullRequest(ctx context.Context, chatID, login, url string) ([]store.ChatPullRequest, error)
	// Multiplayer marks a chat multiplayer, force for a share; ChatChanged shows a chat's row
	// to every screen watching it after its sharing changed (phase 5).
	Multiplayer(ctx context.Context, chatID string, force bool, who string)
	ChatChanged(ctx context.Context, chatID string)
}

// TurnStopper is the conductor, as deleting a chat needs it. Neither of these can be
// reached with a task id: a host turn is a child process of this conductor and has no task,
// and the tasks it delegated are owned by the CONVERSATION rather than by the turn that
// asked for them — which is precisely why deleting the conversation has to end them.
type TurnStopper interface {
	// CancelHostTurn stops the host turn answering ref, reporting whether there was one.
	CancelHostTurn(ref string) bool
	// CancelDelegationsForRef stops every delegated task of one conversation.
	CancelDelegationsForRef(ctx context.Context, ref, reason string) (int, error)
}

// ChatTurnCanceller stops the turn answering a chat right now, on whichever conductor runs
// it (phase 4). The conductor implements it.
type ChatTurnCanceller interface {
	CancelChatTurn(ctx context.Context, chatID string) (bool, error)
}

// DeliveryRetrier puts a bot answer's failed deliveries back in the outbox and attempts them.
// The conductor implements it; ErrNothingToRetry-style refusals come back as errors the
// handler maps to NotFound.
type DeliveryRetrier interface {
	RetryDelivery(ctx context.Context, chatID string, seq uint64) error
}

// TaskCanceller stops a Podium task. It is the same CancelTask `podium task cancel` calls:
// the node gets SIGTERM and up to 30s, and nothing here waits.
type TaskCanceller interface {
	CancelTask(ctx context.Context, taskID, reason string) error
}

// requireLogin is the whole of the chat's ownership story. There is no RBAC in this track,
// but a login is a natural partition and it is free, so a request that arrived without one
// gets nothing rather than somebody else's chats.
//
// "unknown" is what RequireBearer records for a request that carried the bearer and no
// login header — a direct call to the conductor rather than one through podium-server's
// proxy. It is refused here: a chat has an owner or it does not exist.
func requireLogin(ctx context.Context) (string, error) {
	login := Login(ctx)
	if login == "" || login == "unknown" {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New(
			"the chat needs to know who is calling; this request carried no X-Podium-Login"))
	}
	return login, nil
}

// chatEnabled refuses the chat RPCs when no chat source was wired in. Nothing external is
// needed for a chat to work, so in production this never fires; a service built for a test
// that does not care about chat is the case it exists for.
func (s *AgentService) chatEnabled() error {
	if s.chat == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(
			"the web chat is not available on this conductor"))
	}
	return nil
}

// CreateChat opens a conversation owned by the caller.
func (s *AgentService) CreateChat(
	ctx context.Context, req *connect.Request[agentv1.CreateChatRequest],
) (*connect.Response[agentv1.CreateChatResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	var row store.Chat
	if id := strings.TrimSpace(req.Msg.GetPersonalityId()); id == "" {
		row, err = s.store.CreateChat(ctx, login, req.Msg.GetTitle())
	} else {
		voice, voiceErr := s.personalityForCaller(ctx, id, login)
		if voiceErr != nil {
			return nil, personalityError(voiceErr)
		}
		row, err = s.store.CreatePersonalityChat(ctx, login, req.Msg.GetTitle(), voice.ID, voice.DisplayName)
	}
	if err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a chat was created", "chat_id", row.ID, "login", login)
	return connect.NewResponse(&agentv1.CreateChatResponse{Chat: chatToProto(row)}), nil
}

// RenameChat changes the title of one of the caller's chats.
func (s *AgentService) RenameChat(
	ctx context.Context, req *connect.Request[agentv1.RenameChatRequest],
) (*connect.Response[agentv1.RenameChatResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetChatId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("rename chat: chat_id is required"))
	}
	// A viewer is told so; somebody who cannot see the chat is told nothing (the store's
	// filter answers them NotFound, as it always has).
	if !SeesAll(ctx) {
		if cur, err := s.store.GetChat(ctx, req.Msg.GetChatId()); err == nil {
			role, err := s.chatRole(ctx, cur, login)
			if err != nil {
				return nil, storeError(err)
			}
			if store.MayRead(role) && !store.MayWrite(role) {
				return nil, connect.NewError(connect.CodePermissionDenied,
					fmt.Errorf("chat %s: you have view access", cur.ID))
			}
		}
	}
	row, err := s.store.RenameChat(ctx, req.Msg.GetChatId(), login, req.Msg.GetTitle(), SeesAll(ctx))
	switch {
	case errors.Is(err, store.ErrInvalidChatTitle):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case err != nil:
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a chat was renamed", "chat_id", row.ID, "login", login)
	return connect.NewResponse(&agentv1.RenameChatResponse{Chat: chatToProto(row)}), nil
}

// ListChats returns chats newest first. A signed-in user gets their own web chats. The
// dev token, which the proxy marks with X-Podium-Scope: all, gets every chat.
func (s *AgentService) ListChats(
	ctx context.Context, req *connect.Request[agentv1.ListChatsRequest],
) (*connect.Response[agentv1.ListChatsResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	page := req.Msg.GetPage()
	rows, next, err := s.store.ListChats(ctx, login, SeesAll(ctx), int(page.GetLimit()), page.GetCursor(), store.ListChatsScope{
		FilterPersonality: req.Msg.GetFilterPersonality(),
		PersonalityID:     req.Msg.GetPersonalityId(),
		Section:           req.Msg.GetSection(),
	})
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.Chat, 0, len(rows))
	for _, r := range rows {
		c := chatToProto(r)
		c.SharedWithMe = sharedWithMe(r, login, SeesAll(ctx))
		withSharing(ctx, c, r.Sharing)
		out = append(out, c)
	}
	return connect.NewResponse(&agentv1.ListChatsResponse{Chats: out, NextCursor: next}), nil
}

// DeleteChat removes one of the caller's chats and every message in it.
//
// A task still answering the chat is cancelled first. The transcript is about to
// disappear, and a container nobody is listening to would otherwise keep the node slot
// and can still finish work the person who asked for it will never see. Cancel does not
// wait: the chat is gone immediately, the node has up to 30s. Sessions and turns stay.
func (s *AgentService) DeleteChat(
	ctx context.Context, req *connect.Request[agentv1.DeleteChatRequest],
) (*connect.Response[agentv1.DeleteChatResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	chatID := req.Msg.GetChatId()
	if chatID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("delete chat: chat_id is required"))
	}
	row, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return nil, storeError(err)
	}
	// A web chat is its owner's. A mirrored thread has no login until a Podium person speaks
	// in it; only the dev token, which is who can see it then, may remove the copy. Phase 5:
	// delete is for everyone, so it stays the owner's — somebody it was shared with, or who
	// took part, is told to leave instead, and a chat they cannot see is not found. Deleting a
	// mirrored chat removes Podium's record only; the Slack thread or pull request is untouched.
	// A mirrored chat already deleted is gone for its owner too, and deleting it again is
	// the dev token's alone (a no-op that keeps the tombstone).
	if row.DeletedAt != nil && !SeesAll(ctx) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, chatID))
	}
	if row.Login != login && (!SeesAll(ctx) || row.Login != "") {
		role, err := s.chatRole(ctx, row, login)
		if err != nil {
			return nil, storeError(err)
		}
		if store.MayRead(role) {
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("chat %s: only its owner can delete it; leave it instead", chatID))
		}
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, chatID))
	}
	if err := s.stopRunningChatTask(ctx, chatID); err != nil {
		return nil, err
	}
	s.stopChatTurnWork(ctx, chatID)
	if err := s.store.DeleteChat(ctx, chatID, login, SeesAll(ctx) && row.Login == ""); err != nil {
		return nil, storeError(err)
	}
	// Every screen still showing it is told, so its stream ends: a mirrored chat's tombstone,
	// or for a web chat, the tombstone ChatChanged publishes for a row that is gone.
	s.chat.ChatChanged(ctx, chatID)
	s.auditChat(ctx, login, AuditChatDelete, chatID, map[string]any{"origin": row.Origin, "title": row.Title})
	s.logger.InfoContext(ctx, "a chat was deleted", "chat_id", chatID, "login", login)
	return connect.NewResponse(&agentv1.DeleteChatResponse{}), nil
}

// stopChatTurnWork ends what this conversation owns beyond a single task: the host turn
// answering it, and every task that turn delegated.
//
// Neither failure stops the delete. The chat is being removed either way, and a delegated
// task that survives is visible in `podium task ls` and in the log line below — where a
// refusal to delete the chat would leave the person unable to do the thing they asked for.
func (s *AgentService) stopChatTurnWork(ctx context.Context, chatID string) {
	if s.turns == nil {
		return
	}
	if s.turns.CancelHostTurn(chatID) {
		s.logger.InfoContext(ctx, "stopped the host turn of a chat that is being deleted",
			"chat_id", chatID)
	}
	stopped, err := s.turns.CancelDelegationsForRef(ctx, chatID,
		"the chat this task was delegated for was deleted")
	if err != nil {
		s.logger.WarnContext(ctx, "cancelling the delegated tasks of a deleted chat failed; "+
			"they may still be running", "chat_id", chatID, "error", err)
		return
	}
	if stopped > 0 {
		s.logger.InfoContext(ctx, "stopped the delegated tasks of a chat that is being deleted",
			"chat_id", chatID, "tasks", stopped)
	}
}

// stopRunningChatTask asks the node to stop the task currently answering this chat.
//
// FailedPrecondition and NotFound are the control plane saying there is nothing to
// stop, which is what a turn that ended between the modal and this call looks like:
// delete can proceed. Anything else left a task running, and deleting the chat then
// would hide it.
func (s *AgentService) stopRunningChatTask(ctx context.Context, chatID string) error {
	taskID, err := s.store.RunningChatTask(ctx, chatID)
	if err != nil {
		return storeError(err)
	}
	if taskID == "" {
		return nil
	}
	if s.tasks == nil {
		s.logger.WarnContext(ctx, "deleting a chat with a running task, but this conductor cannot cancel it",
			"chat_id", chatID, "task_id", taskID)
		return nil
	}
	err = s.tasks.CancelTask(ctx, taskID, "the chat this task was answering was deleted")
	if err == nil {
		s.logger.InfoContext(ctx, "stopped the task of a chat that is being deleted",
			"chat_id", chatID, "task_id", taskID)
		return nil
	}
	switch connect.CodeOf(err) {
	case connect.CodeFailedPrecondition, connect.CodeNotFound:
		return nil
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf(
			"delete chat %s: stop its task %s: %w", chatID, taskID, err))
	}
}

// SendChatMessage stores one human message and starts a turn on it.
func (s *AgentService) SendChatMessage(
	ctx context.Context, req *connect.Request[agentv1.SendChatMessageRequest],
) (*connect.Response[agentv1.SendChatMessageResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg.GetChatId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("send chat message: chat_id is required"))
	}
	if len(req.Msg.GetText()) > maxChatMessageBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"send chat message: %d bytes is more than the %d-byte limit for one message",
			len(req.Msg.GetText()), maxChatMessageBytes))
	}
	override := profiles.Override{
		Agent:  strings.TrimSpace(req.Msg.GetAgent()),
		Model:  strings.TrimSpace(req.Msg.GetModel()),
		Effort: strings.TrimSpace(req.Msg.GetEffort()),
	}
	if err := s.checkOverride(override); err != nil {
		return nil, err
	}
	res, err := s.chat.SendMessage(ctx, chat.SendRequest{
		ChatID:   req.Msg.GetChatId(),
		Login:    login,
		Text:     req.Msg.GetText(),
		Override: override,
	})
	switch {
	case errors.Is(err, chat.ErrPersonalityGone):
		// The UI says so when the assistant this chat was bound to has been deleted. The
		// message was not stored.
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.SendChatMessageResponse{
		Message:        chatMessageToProto(res.Message),
		Addressed:      res.Addressed,
		QueuedPosition: int32(res.Ahead), //nolint:gosec // a queue of a few turns
	}), nil
}

// CancelQueuedTurn takes back the caller's own waiting turn.
func (s *AgentService) CancelQueuedTurn(
	ctx context.Context, req *connect.Request[agentv1.CancelQueuedTurnRequest],
) (*connect.Response[agentv1.CancelQueuedTurnResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	chatID := req.Msg.GetChatId()
	if chatID == "" || (req.Msg.GetSeq() == 0 && req.Msg.GetId() == 0) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("cancel queued turn: chat_id and an id or seq are required"))
	}
	// By its row when the client names one: a message queued from the web waits unrecorded
	// and has no seq. Taking it back removes it entirely, which is what the person asked.
	if id := req.Msg.GetId(); id != 0 {
		err = s.store.RemoveQueuedTurnByID(ctx, chatID, id, login)
	} else {
		err = s.store.RemoveQueuedTurn(ctx, chatID, req.Msg.GetSeq(), login)
	}
	if err != nil {
		return nil, storeError(err)
	}
	s.chat.QueueChanged(chatID)
	s.logger.InfoContext(ctx, "a queued turn was taken back",
		"chat_id", chatID, "seq", req.Msg.GetSeq(), "queue_id", req.Msg.GetId())
	return connect.NewResponse(&agentv1.CancelQueuedTurnResponse{}), nil
}

// CancelChatTurn stops the turn answering a chat, for anybody who may write it. Who started
// the turn does not matter: in a shared conversation, stopping an answer that is going wrong
// is everybody's to do, and the person whose turn it was can ask again.
func (s *AgentService) CancelChatTurn(
	ctx context.Context, req *connect.Request[agentv1.CancelChatTurnRequest],
) (*connect.Response[agentv1.CancelChatTurnResponse], error) {
	if err := s.chatEnabled(); err != nil {
		return nil, err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	chatID := req.Msg.GetChatId()
	if chatID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("cancel chat turn: chat_id is required"))
	}
	row, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return nil, storeError(err)
	}
	// Phase 5: stopping a turn is an editor's, like writing the chat; a viewer only watches.
	if err := s.mayWriteChat(ctx, row, login); err != nil {
		return nil, err
	}
	if s.canceller == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("cancel chat turn: no conductor is wired in"))
	}
	stopped, err := s.canceller.CancelChatTurn(ctx, chatID)
	if err != nil {
		return nil, storeError(err)
	}
	if stopped {
		s.logger.InfoContext(ctx, "a chat's turn was stopped", "chat_id", chatID, "login", login)
	}
	return connect.NewResponse(&agentv1.CancelChatTurnResponse{Stopped: stopped}), nil
}

// SearchMembers lists Podium people for the composer's @-mention list.
func (s *AgentService) SearchMembers(
	ctx context.Context, req *connect.Request[agentv1.SearchMembersRequest],
) (*connect.Response[agentv1.SearchMembersResponse], error) {
	if _, err := requireLogin(ctx); err != nil {
		return nil, err
	}
	rows, err := s.store.SearchMembers(ctx, req.Msg.GetQuery(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.Member, 0, len(rows))
	for _, m := range rows {
		out = append(out, &agentv1.Member{Login: m.Login, Name: m.Name, Avatar: m.Avatar})
	}
	return connect.NewResponse(&agentv1.SearchMembersResponse{Members: out}), nil
}

// maxPullRequestURLBytes is the longest URL the attach and detach RPCs will look at. A
// GitHub pull-request URL is around sixty characters; this is only what stops an
// arbitrarily long string being handed to a regular expression.
const maxPullRequestURLBytes = 2 << 10

// AttachChatPullRequest links a pull request to one of the caller's chats by hand.
func (s *AgentService) AttachChatPullRequest(
	ctx context.Context, req *connect.Request[agentv1.AttachChatPullRequestRequest],
) (*connect.Response[agentv1.AttachChatPullRequestResponse], error) {
	login, err := s.checkPullRequestRequest(ctx, req.Msg.GetChatId(), req.Msg.GetUrl())
	if err != nil {
		return nil, err
	}
	prs, err := s.chat.AttachPullRequest(ctx, req.Msg.GetChatId(), login, req.Msg.GetUrl())
	if err != nil {
		return nil, pullRequestError(err)
	}
	s.logger.InfoContext(ctx, "a pull request was attached to a chat",
		"chat_id", req.Msg.GetChatId(), "login", login, "url", req.Msg.GetUrl())
	return connect.NewResponse(&agentv1.AttachChatPullRequestResponse{
		PullRequests: pullRequestsToProto(prs),
	}), nil
}

// DetachChatPullRequest takes one link off one of the caller's chats.
func (s *AgentService) DetachChatPullRequest(
	ctx context.Context, req *connect.Request[agentv1.DetachChatPullRequestRequest],
) (*connect.Response[agentv1.DetachChatPullRequestResponse], error) {
	login, err := s.checkPullRequestRequest(ctx, req.Msg.GetChatId(), req.Msg.GetUrl())
	if err != nil {
		return nil, err
	}
	prs, err := s.chat.DetachPullRequest(ctx, req.Msg.GetChatId(), login, req.Msg.GetUrl())
	if err != nil {
		return nil, pullRequestError(err)
	}
	s.logger.InfoContext(ctx, "a pull request was detached from a chat",
		"chat_id", req.Msg.GetChatId(), "login", login, "url", req.Msg.GetUrl())
	return connect.NewResponse(&agentv1.DetachChatPullRequestResponse{
		PullRequests: pullRequestsToProto(prs),
	}), nil
}

// checkPullRequestRequest is the preamble both pull-request RPCs share.
func (s *AgentService) checkPullRequestRequest(ctx context.Context, chatID, url string) (string, error) {
	if err := s.chatEnabled(); err != nil {
		return "", err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return "", err
	}
	if chatID == "" {
		return "", connect.NewError(connect.CodeInvalidArgument,
			errors.New("chat_id is required"))
	}
	if len(url) > maxPullRequestURLBytes {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"%d bytes is more than the %d-byte limit for a pull request URL",
			len(url), maxPullRequestURLBytes))
	}
	return login, nil
}

// pullRequestError maps a URL that is not a pull request onto InvalidArgument and leaves
// everything else to the store's own mapping.
func pullRequestError(err error) error {
	if errors.Is(err, chat.ErrNotPullRequestURL) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return storeError(err)
}

// checkOverride refuses a per-turn choice the catalogue does not allow, before the message
// is stored.
//
// It resolves first and validates the RESULT, because the parts are not independent: a
// caller that names only an effort is asking about the model it will inherit, and a caller
// that names only a model has also chosen that model's backend. Validating the fields
// separately would accept combinations that cannot run.
//
// It is resolved against the ASSISTANT, because that is what the override moves: a chat
// message is answered here, and the tasks the turn delegates keep their own playbooks'
// models whatever this says.
func (s *AgentService) checkOverride(o profiles.Override) error {
	if o.Empty() {
		return nil
	}
	if s.profiles == nil {
		return nil
	}
	p := s.profiles.Current()
	if p == nil {
		return nil
	}
	if err := profiles.ValidateOverride(p.ResolveAssistant(o)); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

// StreamChat replays a chat and then follows it live.
//
// The order is: subscribe, send the turn state, replay, then forward. Subscribing first is
// what closes the gap — a message stored between the replay and the subscription would
// otherwise be seen by nobody — and the cost is that a frame may arrive twice, which is
// harmless because a client keys on seq.
//
// The status frame goes FIRST and unconditionally, for two reasons. It is what tells a
// browser joining mid-turn that the composer is disabled without waiting for the next
// progress line. And it is what puts the response headers on the wire immediately: a
// Connect server stream writes no headers until its first message, and podium-server's
// proxy gives the conductor 30 seconds to produce them — so a subscriber on a quiet chat
// would otherwise be disconnected every 30 seconds and reconnect for ever.
//
// It never returns on its own. The client cancels, and the request context is what unwinds
// the subscription.
func (s *AgentService) StreamChat(
	ctx context.Context, req *connect.Request[agentv1.StreamChatRequest],
	stream *connect.ServerStream[agentv1.ChatFrame],
) error {
	if err := s.chatEnabled(); err != nil {
		return err
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return err
	}
	chatID := req.Msg.GetChatId()
	if chatID == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("stream chat: chat_id is required"))
	}
	row, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return storeError(err)
	}
	// Opening is where a link, or members access, becomes the opener's own row (phase 5).
	role, err := s.openChat(ctx, row, login, req.Msg.GetLinkToken())
	if err != nil {
		return storeError(err)
	}
	if !store.MayRead(role) {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, chatID))
	}
	// Who has spoken, for a conversation with more than one person in it. Loaded here and
	// not in ListChats, which would need a query per row and only shows started_by.
	if row.Origin != store.OriginWeb || row.Multiplayer {
		if row.Participants, err = s.store.ChatParticipants(ctx, chatID); err != nil {
			return storeError(err)
		}
	}

	sub := s.chat.Subscribe(ctx, chatID)
	defer sub.Close()

	running, err := s.chat.Running(ctx, chatID)
	if err != nil {
		return storeError(err)
	}
	state := chat.StatusFinished
	switch {
	case s.chat.Awaiting(chatID):
		state = chat.StatusAwaiting
	case running:
		state = chat.StatusStarted
	}
	if err := stream.Send(&agentv1.ChatFrame{
		Frame: &agentv1.ChatFrame_Status{Status: &agentv1.ChatStatus{State: state}},
	}); err != nil {
		return err
	}
	// The chat row itself — title and playbook — so a deep link does not wait for ListChats
	// to learn which playbook this conversation already chose.
	head := chatToProto(row)
	head.SharedWithMe = sharedWithMe(row, login, SeesAll(ctx))
	head.MyRole = role
	head.TaskRunning = s.chatTaskRunning(ctx, chatID)
	if err := stream.Send(&agentv1.ChatFrame{
		Frame: &agentv1.ChatFrame_Chat{Chat: head},
	}); err != nil {
		return err
	}

	// The pull requests, before the transcript. They are the answer to "what did this
	// conversation produce", and a reader who has that in front of them before the replay
	// arrives does not have to read the transcript to find out.
	prs, err := s.store.ListChatPullRequests(ctx, chatID)
	if err != nil {
		return storeError(err)
	}
	if err := stream.Send(&agentv1.ChatFrame{
		Frame: &agentv1.ChatFrame_PullRequests{PullRequests: &agentv1.ChatPullRequests{
			PullRequests: pullRequestsToProto(prs),
		}},
	}); err != nil {
		return err
	}

	// The turns waiting behind the one in flight, so a screen that opens mid-queue shows it.
	// Nothing is sent for an empty queue, which is what a client starts from.
	queued, err := s.store.ListQueuedTurnsForChat(ctx, chatID)
	if err != nil {
		return storeError(err)
	}
	if len(queued) > 0 {
		if err := stream.Send(frameToProto(chat.Frame{Kind: chat.FrameQueue, Queue: queued})); err != nil {
			return err
		}
	}

	lastSeq, err := s.replay(ctx, stream, chatID, req.Msg.GetFromSeq())
	if err != nil {
		return err
	}
	// The transcript so far is on the client. Without this an empty chat and one still
	// replaying look the same to a screen with no messages yet, and it shows a new chat's
	// welcome before the history lands.
	if err := stream.Send(&agentv1.ChatFrame{Frame: &agentv1.ChatFrame_Replayed{Replayed: true}}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.Resync():
			// This subscriber fell behind and something durable was dropped. Saying so is
			// better than sending a gap: the client re-reads from its last seq.
			s.logger.WarnContext(ctx, "a chat subscriber fell behind; asked it to resync",
				"chat_id", chatID, "last_seq", lastSeq)
			if err := stream.Send(&agentv1.ChatFrame{Frame: &agentv1.ChatFrame_Resync{Resync: true}}); err != nil {
				return err
			}
		case f := <-sub.Frames():
			if f.Kind == chat.FrameMessage {
				// Access is checked again before every message, not only when a chat frame
				// says it changed: a path that changes who may read a chat and forgets to
				// announce it must not leave a stream showing what comes next. It is one
				// read of the chat and one of the viewer's role, per message.
				if err := s.stillReadable(ctx, chatID, login); err != nil {
					return err
				}
				if f.Message.Seq > lastSeq {
					lastSeq = f.Message.Seq
				}
			}
			out := frameToProto(f)
			if f.Kind == chat.FrameChat {
				// The chat's row changed, which is how a share, an unshare or a change of
				// general access reaches a screen (phase 5). The role is this viewer's, so it
				// is worked out again here; a viewer who has lost access is shown nothing more.
				role, err := s.chatRole(ctx, f.Chat, login)
				if err != nil {
					return storeError(err)
				}
				if !store.MayRead(role) {
					return connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, chatID))
				}
				c := out.GetChat()
				c.MyRole = role
				c.SharedWithMe = sharedWithMe(f.Chat, login, SeesAll(ctx))
				c.TaskRunning = s.chatTaskRunning(ctx, chatID)
			}
			if err := stream.Send(out); err != nil {
				return err
			}
		}
	}
}

// chatTaskRunning is whether a task this chat delegated is still running, for a chat row read
// on its own: GetChat leaves Chat.TaskRunning false, which drew a delegated task as finished in
// the open chat while it was still working (the list computes it inline and was right). A read
// that fails says false, as the row did before: a stale "done" beats a stream that ends.
func (s *AgentService) chatTaskRunning(ctx context.Context, chatID string) bool {
	running, err := s.store.ChatTaskRunning(ctx, chatID)
	if err != nil {
		s.logger.WarnContext(ctx, "reading whether a chat's task is running failed", "chat_id", chatID, "error", err)
		return false
	}
	return running
}

// stillReadable is nil while login may still read the chat, and the not-found error a stream
// ends with once they may not: the chat is gone, tombstoned, or no longer shared with them.
func (s *AgentService) stillReadable(ctx context.Context, chatID, login string) error {
	row, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return storeError(err)
	}
	role, err := s.chatRole(ctx, row, login)
	if err != nil {
		return storeError(err)
	}
	if !store.MayRead(role) {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, chatID))
	}
	return nil
}

// replay sends everything stored after fromSeq and returns the highest seq it sent.
func (s *AgentService) replay(
	ctx context.Context, stream *connect.ServerStream[agentv1.ChatFrame], chatID string, fromSeq uint64,
) (uint64, error) {
	msgs, err := s.store.ListChatMessages(ctx, chatID, fromSeq)
	if err != nil {
		return 0, storeError(err)
	}
	last := fromSeq
	for _, m := range msgs {
		if err := stream.Send(frameToProto(chat.Frame{Kind: chat.FrameMessage, Message: m})); err != nil {
			return 0, err
		}
		last = m.Seq
	}
	return last, nil
}

func frameToProto(f chat.Frame) *agentv1.ChatFrame {
	switch f.Kind {
	case chat.FrameMessage:
		return &agentv1.ChatFrame{
			Frame: &agentv1.ChatFrame_Message{Message: chatMessageToProto(f.Message)},
		}
	case chat.FrameProgress:
		return &agentv1.ChatFrame{Frame: &agentv1.ChatFrame_Progress{Progress: f.Progress}}
	case chat.FrameStatus:
		return &agentv1.ChatFrame{Frame: &agentv1.ChatFrame_Status{Status: &agentv1.ChatStatus{
			State: f.State, TaskId: f.TaskID,
		}}}
	case chat.FrameChat:
		return &agentv1.ChatFrame{Frame: &agentv1.ChatFrame_Chat{Chat: chatToProto(f.Chat)}}
	case chat.FramePullRequests:
		return &agentv1.ChatFrame{Frame: &agentv1.ChatFrame_PullRequests{
			PullRequests: &agentv1.ChatPullRequests{PullRequests: pullRequestsToProto(f.PullRequests)},
		}}
	case chat.FrameQueue:
		return &agentv1.ChatFrame{Frame: &agentv1.ChatFrame_Queue{Queue: queueToProto(f.Queue)}}
	default:
		// An unknown kind is a programming error, and an empty frame is what a client can
		// safely ignore.
		return &agentv1.ChatFrame{}
	}
}

// mayReadChat reports whether login may READ this conversation: the dev token reads every
// chat; anybody else reads what store.ChatVisibleTo allows — a chat they own, or one they
// have taken part in from any surface (0031_identities.sql's chat_visible_to). The owner
// check is answered from the row without a query, because it is every web chat's case.
//
// Sending still requires the owner, and a mirrored thread is answered where it lives.
func (s *AgentService) mayReadChat(ctx context.Context, c store.Chat, login string) (bool, error) {
	role, err := s.chatRole(ctx, c, login)
	return store.MayRead(role), err
}

// withSharing puts the caller's relationship to a chat on its proto. The dev token is the
// operator, an owner of everything it sees, and has no read state of its own.
func withSharing(ctx context.Context, c *agentv1.Chat, sh store.ChatSharing) {
	c.MyRole, c.AccessReason, c.SharedBy, c.UnreadCount = sh.MyRole, sh.AccessReason, sh.SharedBy, sh.UnreadCount
	if SeesAll(ctx) {
		c.MyRole, c.UnreadCount = store.RoleOwner, 0
	}
}

// chatToProto never carries the link token: that is the owner's alone, and only the share
// dialog (sharingToProto) hands it out.
func chatToProto(c store.Chat) *agentv1.Chat {
	out := &agentv1.Chat{
		GeneralAccess:      c.GeneralAccess,
		GeneralRole:        c.GeneralRole,
		Id:                 c.ID,
		Title:              c.Title,
		CreatedAt:          timestamppb.New(c.CreatedAt),
		Preview:            c.Preview,
		TurnRunning:        c.TurnRunning,
		TaskRunning:        c.TaskRunning,
		Agent:              c.Agent,
		Model:              c.Model,
		Effort:             c.Effort,
		Origin:             c.Origin,
		StartedBy:          c.StartedBy,
		Login:              c.Login,
		Participants:       participantNames(c.Participants),
		People:             peopleToProto(c.Participants),
		Surfaces:           surfacesOf(c),
		Channel:            c.Channel,
		PersonalityId:      c.PersonalityID,
		PersonalityDisplay: c.PersonalityDisplay,
		Multiplayer:        c.Multiplayer,
		RoutineId:          c.RoutineID,
	}
	if c.LastMessageAt != nil {
		out.LastMessageAt = timestamppb.New(*c.LastMessageAt)
	}
	return out
}

func pullRequestsToProto(prs []store.ChatPullRequest) []*agentv1.ChatPullRequest {
	out := make([]*agentv1.ChatPullRequest, 0, len(prs))
	for _, pr := range prs {
		out = append(out, &agentv1.ChatPullRequest{
			Url:       pr.URL,
			Owner:     pr.Owner,
			Repo:      pr.Repo,
			Number:    int32(pr.Number),
			Source:    pr.Source,
			CreatedAt: timestamppb.New(pr.CreatedAt),
		})
	}
	return out
}

func chatMessageToProto(m store.ChatMessage) *agentv1.ChatMessage {
	out := &agentv1.ChatMessage{
		ChatId:       m.ChatID,
		Seq:          m.Seq,
		Role:         m.Role,
		Text:         m.Text,
		Ts:           timestamppb.New(m.TS),
		TaskId:       m.TaskID,
		Author:       m.Author,
		AuthorLogin:  m.AuthorLogin,
		AuthorHandle: m.AuthorHandle,
		AuthorAvatar: m.AuthorAvatar,
		Via:          m.Via,
		ExternalUrl:  m.ExternalURL,
		Addressed:    m.Addressed,
		Mentions:     m.Mentions,
	}
	for _, d := range m.Deliveries {
		out.Deliveries = append(out.Deliveries, &agentv1.ChatDelivery{
			Surface:  d.SurfaceKind,
			Status:   d.Status,
			Attempts: int32(d.Attempts), //nolint:gosec // a handful of attempts
			Error:    cutDeliveryError(d.LastError),
		})
	}
	if m.EditedAt != nil {
		out.EditedAt = timestamppb.New(*m.EditedAt)
	}
	if m.DeletedAt != nil {
		// Deleted on its own surface: the row stays in the store with its text (decision
		// 12), and none of it leaves the API. The UI draws a placeholder.
		out.Deleted = true
		out.Text = ""
		return out
	}
	for _, a := range m.Attachments {
		out.Attachments = append(out.Attachments, &agentv1.ChatAttachment{
			ArtifactId:  a.ArtifactID,
			Name:        a.Name,
			ContentType: a.ContentType,
			SizeBytes:   a.SizeBytes,
		})
	}
	return out
}

// sharedWithMe is whether a chat the caller can see is somebody else's.
func sharedWithMe(c store.Chat, login string, all bool) bool {
	return !all && c.Login != "" && c.Login != login
}

// queueToProto is the turns waiting, oldest first.
func queueToProto(q []store.QueuedTurn) *agentv1.ChatQueue {
	out := &agentv1.ChatQueue{}
	for _, t := range q {
		out.Turns = append(out.Turns, &agentv1.QueuedTurn{
			Login:    t.Login,
			Author:   t.Author,
			Via:      queuedVia(t.SourceKind),
			Seq:      t.MessageSeq,
			QueuedAt: timestamppb.New(t.CreatedAt),
			Id:       t.ID,
			Text:     queuedText(t.Event),
		})
	}
	return out
}

// queuedText is what a queued turn asked, in the asker's own words, from the event the
// conductor queued. Empty when the event does not say.
func queuedText(raw []byte) string {
	var ev struct{ Text, RecordText string }
	if len(raw) == 0 || json.Unmarshal(raw, &ev) != nil {
		return ""
	}
	if ev.RecordText != "" {
		return ev.RecordText
	}
	return ev.Text
}

// queuedVia is a queued turn's surface in the words the UI uses.
func queuedVia(sourceKind string) string {
	switch sourceKind {
	case conductor.SourceChat, "":
		return store.ViaWeb
	default:
		return sourceKind
	}
}

// peopleToProto is the participants as the header draws them.
func peopleToProto(ps []store.ChatParticipant) []*agentv1.ChatPerson {
	if len(ps) == 0 {
		return nil
	}
	out := make([]*agentv1.ChatPerson, 0, len(ps))
	for _, p := range ps {
		out = append(out, &agentv1.ChatPerson{Login: p.Login, Name: p.Name, Avatar: p.Avatar, Via: p.Via})
	}
	return out
}

// surfacesOf is where a mirrored conversation lives, read from the session it records. The
// key is the conductor's, and its two mirrored shapes are fixed: slack:<channel>:<thread>
// and github:<owner>/<repo>#<n>. A web chat lives nowhere else.
func surfacesOf(c store.Chat) []*agentv1.ChatSurface {
	switch {
	case strings.HasPrefix(c.SourceKey, "slack:"):
		label := "Slack thread"
		if c.Channel != "" {
			label = "#" + c.Channel
		}
		return []*agentv1.ChatSurface{{Kind: store.ViaSlack, Label: label, Url: c.SourceURL}}
	case strings.HasPrefix(c.SourceKey, "github:"):
		ref := strings.TrimPrefix(c.SourceKey, "github:")
		url := ""
		if slash, hash := strings.IndexByte(ref, '/'), strings.LastIndexByte(ref, '#'); slash > 0 && hash > slash {
			url = "https://github.com/" + ref[:hash] + "/pull/" + ref[hash+1:]
		}
		return []*agentv1.ChatSurface{{Kind: store.ViaGitHub, Label: ref, Url: url}}
	}
	return nil
}

// participantNames is the participants as the existing proto field carries them: one name
// each, Podium people first.
func participantNames(ps []store.ChatParticipant) []string {
	if len(ps) == 0 {
		return nil
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// deliveryErrorRunes bounds the failure text a delivery carries to the UI. A Slack or GitHub
// error is a line; anything longer is a body nobody reading "not delivered" needs.
const deliveryErrorRunes = 300

func cutDeliveryError(s string) string {
	r := []rune(s)
	if len(r) <= deliveryErrorRunes {
		return s
	}
	return string(r[:deliveryErrorRunes]) + "…"
}

// RetryChatDelivery puts one bot message's failed deliveries back in the queue. Anybody who
// can read the chat may press it: they are the people who can see that it did not arrive,
// and retrying only re-sends what the bot already said where it was meant to say it. Phase 5
// narrows this to editors.
func (s *AgentService) RetryChatDelivery(
	ctx context.Context, req *connect.Request[agentv1.RetryChatDeliveryRequest],
) (*connect.Response[agentv1.RetryChatDeliveryResponse], error) {
	if s.deliveries == nil {
		return nil, connect.NewError(connect.CodeUnimplemented,
			errors.New("retry chat delivery: this conductor has no delivery outbox"))
	}
	login, err := requireLogin(ctx)
	if err != nil {
		return nil, err
	}
	chatID, seq := req.Msg.GetChatId(), req.Msg.GetSeq()
	if chatID == "" || seq == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("retry chat delivery: chat_id and seq are required"))
	}
	row, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return nil, storeError(err)
	}
	// Phase 5: retrying a delivery is writing to the conversation, so it is an editor's.
	if err := s.mayWriteChat(ctx, row, login); err != nil {
		return nil, err
	}
	if err := s.deliveries.RetryDelivery(ctx, chatID, seq); err != nil {
		if errors.Is(err, conductor.ErrNothingToRetry) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.RetryChatDeliveryResponse{}), nil
}
