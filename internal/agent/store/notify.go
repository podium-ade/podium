package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ChatChannel is the Postgres NOTIFY channel a chat's live frames travel on between
// conductors that share this database. Every frame worth keeping is a row before it is
// announced, so a notification only says "look": a conductor that hears one re-reads the
// row and hands it to its own watchers. See chat.Relay.
const ChatChannel = "podium_chat"

// MaxNotifyPayload is the largest payload Postgres accepts on a NOTIFY, less a margin for
// the envelope. Anything that would not fit is announced as a pointer to re-read instead.
const MaxNotifyPayload = 7900

// listenRetry bounds the wait between attempts to re-establish a LISTEN after its
// connection drops.
const (
	listenRetryMin = 200 * time.Millisecond
	listenRetryMax = 10 * time.Second
)

// Notify sends one notification on channel. It is not part of any write's transaction: the
// row it points at has already committed, which is what makes "re-read on notify" safe —
// the reader can never see the notification before the row.
func (s *Store) Notify(ctx context.Context, channel, payload string) error {
	if len(payload) > MaxNotifyPayload {
		return fmt.Errorf("notify %s: payload of %d bytes is over the %d-byte limit", channel, len(payload), MaxNotifyPayload)
	}
	if _, err := s.pool.Exec(ctx, "select pg_notify($1, $2)", channel, payload); err != nil {
		return fmt.Errorf("notify %s: %w", channel, err)
	}
	return nil
}

// Listen holds a connection of its own LISTENing on channel and calls fn with each payload,
// until ctx ends. A dropped connection is re-established with backoff, and connected is
// called each time the LISTEN is (re)established: anything announced while it was down was
// missed, and the caller must have its watchers re-read rather than trust the stream. failed,
// when set, hears why a connection was lost.
//
// The connection is outside the pool on purpose. A LISTEN belongs to a session, and a pooled
// connection handed back would carry the subscription to whichever query took it next.
func (s *Store) Listen(
	ctx context.Context, channel string, connected func(), fn func(payload string), failed func(error),
) error {
	cfg := s.pool.Config().ConnConfig.Copy()
	wait := listenRetryMin
	onConnect := func() {
		// A connection that came up is a fresh start: the next drop waits the minimum again.
		wait = listenRetryMin
		if connected != nil {
			connected()
		}
	}
	for {
		err := s.listenOnce(ctx, cfg, channel, onConnect, fn)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && failed != nil {
			failed(err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait *= 2
		if wait > listenRetryMax {
			wait = listenRetryMax
		}
	}
}

func (s *Store) listenOnce(
	ctx context.Context, cfg *pgx.ConnConfig, channel string, connected func(), fn func(string),
) error {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("listen %s: connect: %w", channel, err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	if _, err := conn.Exec(ctx, "listen "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return fmt.Errorf("listen %s: %w", channel, err)
	}
	if connected != nil {
		connected()
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("listen %s: %w", channel, err)
		}
		fn(n.Payload)
	}
}
