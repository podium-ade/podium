package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/alvaroibarguen/podium/internal/agent/chat"
	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// maxChatMessageBytes caps one human message. The brief cap is 96 KiB of base64 for the
// whole conversation — 72 KiB of JSON — so a single message larger than this could never be
// answered anyway, and refusing it here says so instead of failing the turn.
const maxChatMessageBytes = 32 << 10

// ChatSource is the part of the chat source the handlers use: the write path that starts a
// turn, and the live fan-out. Reads go to the store directly.
type ChatSource interface {
	Send(ctx context.Context, req chat.SendRequest) (store.ChatMessage, error)
	Subscribe(ctx context.Context, chatID string) *chat.Subscriber
	Running(ctx context.Context, chatID string) (bool, error)
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
	row, err := s.store.CreateChat(ctx, login, req.Msg.GetTitle())
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
	row, err := s.store.RenameChat(ctx, req.Msg.GetChatId(), login, req.Msg.GetTitle())
	switch {
	case errors.Is(err, store.ErrInvalidChatTitle):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case err != nil:
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a chat was renamed", "chat_id", row.ID, "login", login)
	return connect.NewResponse(&agentv1.RenameChatResponse{Chat: chatToProto(row)}), nil
}

// ListChats returns the caller's own chats, newest first.
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
	rows, next, err := s.store.ListChats(ctx, login, int(page.GetLimit()), page.GetCursor())
	if err != nil {
		return nil, storeError(err)
	}
	out := make([]*agentv1.Chat, 0, len(rows))
	for _, r := range rows {
		out = append(out, chatToProto(r))
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
	if row.Login != login {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, chatID))
	}
	if err := s.stopRunningChatTask(ctx, chatID); err != nil {
		return nil, err
	}
	if err := s.store.DeleteChat(ctx, chatID, login); err != nil {
		return nil, storeError(err)
	}
	s.logger.InfoContext(ctx, "a chat was deleted", "chat_id", chatID, "login", login)
	return connect.NewResponse(&agentv1.DeleteChatResponse{}), nil
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
	if err := s.checkOverride(req.Msg.GetPlaybook(), override); err != nil {
		return nil, err
	}
	msg, err := s.chat.Send(ctx, chat.SendRequest{
		ChatID:   req.Msg.GetChatId(),
		Login:    login,
		Text:     req.Msg.GetText(),
		Playbook: req.Msg.GetPlaybook(),
		Override: override,
	})
	switch {
	case errors.Is(err, chat.ErrTurnRunning):
		// The UI disables the composer, so a human never sees this. It is the server-side
		// guarantee behind that: turn-based, one in flight per conversation.
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, storeError(err)
	}
	return connect.NewResponse(&agentv1.SendChatMessageResponse{Message: chatMessageToProto(msg)}), nil
}

// checkOverride refuses a per-turn choice the catalogue does not allow, before the message
// is stored.
//
// It resolves first and validates the RESULT, because the parts are not independent: a
// caller that names only an effort is asking about the model it will inherit, and a caller
// that names only a model has also chosen that model's backend. Validating the fields
// separately would accept combinations that cannot run.
//
// A playbook this conductor does not have is not this function's problem — the routing rules
// deal with an unknown name — so an unresolvable playbook validates the override against the
// profile alone rather than refusing.
func (s *AgentService) checkOverride(playbookName string, o profiles.Override) error {
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
	playbook := p.Playbooks[playbookName]
	if err := profiles.ValidateOverride(p.Resolve(playbook, o)); err != nil {
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
	if row.Login != login {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%w: chat %s", store.ErrNotFound, chatID))
	}

	sub := s.chat.Subscribe(ctx, chatID)
	defer sub.Close()

	running, err := s.chat.Running(ctx, chatID)
	if err != nil {
		return storeError(err)
	}
	state := chat.StatusFinished
	if running {
		state = chat.StatusStarted
	}
	if err := stream.Send(&agentv1.ChatFrame{
		Frame: &agentv1.ChatFrame_Status{Status: &agentv1.ChatStatus{State: state}},
	}); err != nil {
		return err
	}
	// The chat row itself — title and playbook — so a deep link does not wait for ListChats
	// to learn which playbook this conversation already chose.
	if err := stream.Send(&agentv1.ChatFrame{
		Frame: &agentv1.ChatFrame_Chat{Chat: chatToProto(row)},
	}); err != nil {
		return err
	}

	lastSeq, err := s.replay(ctx, stream, chatID, req.Msg.GetFromSeq())
	if err != nil {
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
			if f.Kind == chat.FrameMessage && f.Message.Seq > lastSeq {
				lastSeq = f.Message.Seq
			}
			if err := stream.Send(frameToProto(f)); err != nil {
				return err
			}
		}
	}
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
	default:
		// An unknown kind is a programming error, and an empty frame is what a client can
		// safely ignore.
		return &agentv1.ChatFrame{}
	}
}

func chatToProto(c store.Chat) *agentv1.Chat {
	out := &agentv1.Chat{
		Id:          c.ID,
		Title:       c.Title,
		CreatedAt:   timestamppb.New(c.CreatedAt),
		Preview:     c.Preview,
		TurnRunning: c.TurnRunning,
		Playbook:    c.Playbook,
	}
	if c.LastMessageAt != nil {
		out.LastMessageAt = timestamppb.New(*c.LastMessageAt)
	}
	return out
}

func chatMessageToProto(m store.ChatMessage) *agentv1.ChatMessage {
	out := &agentv1.ChatMessage{
		ChatId: m.ChatID,
		Seq:    m.Seq,
		Role:   m.Role,
		Text:   m.Text,
		Ts:     timestamppb.New(m.TS),
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
