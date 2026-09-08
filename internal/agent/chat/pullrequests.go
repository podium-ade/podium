package chat

import (
	"context"
	"errors"
	"fmt"

	"github.com/alvaroibarguen/podium/internal/agent/conductor"
	"github.com/alvaroibarguen/podium/internal/agent/store"
)

// ErrNotPullRequestURL is what the manual attach returns for something that is not a
// GitHub pull request. The handler maps it to InvalidArgument.
var ErrNotPullRequestURL = errors.New("not a GitHub pull request URL")

// LinkPullRequests implements the conductor's optional pull-request half of Source: the
// turn that just finished named these, so the conversation carries them.
//
// It is quiet when nothing changed. A follow-up turn that mentions the pull request the
// previous one opened links nothing new, and neither does one whose link a human has
// already detached — the store's insert is what decides, and the frame goes out only when
// it actually took a row.
func (s *Source) LinkPullRequests(ctx context.Context, ref string, prs []conductor.PullRequest) error {
	added := false
	var failed error
	for _, pr := range prs {
		ok, err := s.store.LinkChatPullRequest(ctx, store.ChatPullRequest{
			ChatID: ref, URL: pr.URL, Owner: pr.Owner, Repo: pr.Repo, Number: pr.Number,
		})
		if err != nil {
			// One link that would not store must not lose the rest of them.
			failed = errors.Join(failed, err)
			continue
		}
		added = added || ok
	}
	if added {
		if _, err := s.pullRequests(ctx, ref); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	return failed
}

// AttachPullRequest is a person linking one by hand: the turn missed it, or it is a
// related pull request they want on the conversation. It returns the chat's links as they
// now stand.
func (s *Source) AttachPullRequest(
	ctx context.Context, chatID, login, rawURL string,
) ([]store.ChatPullRequest, error) {
	if err := s.ownChat(ctx, chatID, login); err != nil {
		return nil, err
	}
	pr, ok := conductor.ParsePullRequestURL(rawURL)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotPullRequestURL, rawURL)
	}
	if _, err := s.store.AttachChatPullRequest(ctx, store.ChatPullRequest{
		ChatID: chatID, URL: pr.URL, Owner: pr.Owner, Repo: pr.Repo, Number: pr.Number,
	}); err != nil {
		return nil, err
	}
	return s.pullRequests(ctx, chatID)
}

// DetachPullRequest takes one link off a chat. The URL is canonicalised first, so
// detaching the link by the address the browser is showing — which may carry /files or a
// comment fragment — removes the row that was actually stored.
func (s *Source) DetachPullRequest(
	ctx context.Context, chatID, login, rawURL string,
) ([]store.ChatPullRequest, error) {
	if err := s.ownChat(ctx, chatID, login); err != nil {
		return nil, err
	}
	pr, ok := conductor.ParsePullRequestURL(rawURL)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotPullRequestURL, rawURL)
	}
	if err := s.store.DetachChatPullRequest(ctx, chatID, pr.URL); err != nil {
		return nil, err
	}
	return s.pullRequests(ctx, chatID)
}

// ownChat is the same ownership answer Send gives: another login's chat is reported as no
// such chat, because its existence is not this caller's to learn.
func (s *Source) ownChat(ctx context.Context, chatID, login string) error {
	if chatID == "" {
		return errors.New("a chat id is required")
	}
	c, err := s.store.GetChat(ctx, chatID)
	if err != nil {
		return err
	}
	if c.Login != login {
		return fmt.Errorf("%w: chat %s", store.ErrNotFound, chatID)
	}
	return nil
}

// pullRequests reads the chat's links back and publishes them to whoever is watching. The
// read is what the caller returns, so the browser that made the change and the browsers
// that did not are looking at the same list.
func (s *Source) pullRequests(ctx context.Context, chatID string) ([]store.ChatPullRequest, error) {
	prs, err := s.store.ListChatPullRequests(ctx, chatID)
	if err != nil {
		return nil, err
	}
	s.bcast.Publish(chatID, Frame{Kind: FramePullRequests, PullRequests: prs})
	return prs, nil
}
