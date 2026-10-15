package sandbox

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// Swappable is a Provider whose implementation can be replaced while it is in use: a sandbox
// node configured from the control plane builds one when the configuration arrives and again
// whenever an admin changes it. Until the first one is set every call fails, retryably.
//
// Disabling it stops new sandboxes and nothing else: the last provider stays for the ones
// that already exist, which still have to be reached, and deleted, when their tasks end.
type Swappable struct {
	name     string
	mu       sync.RWMutex
	p        Provider
	disabled bool
}

// NewSwappable returns an empty Swappable for the named provider.
func NewSwappable(name string) *Swappable { return &Swappable{name: name} }

// Set replaces the provider and enables it.
func (s *Swappable) Set(p Provider) {
	s.mu.Lock()
	s.p, s.disabled = p, false
	s.mu.Unlock()
}

// Disable stops Create, keeping the provider for the sandboxes that already exist.
func (s *Swappable) Disable() {
	s.mu.Lock()
	s.disabled = true
	s.mu.Unlock()
}

// Ready reports whether new sandboxes can be made.
func (s *Swappable) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.p != nil && !s.disabled
}

func (s *Swappable) get() Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.p
}

func (s *Swappable) current(op string) (Provider, error) {
	if p := s.get(); p != nil {
		return p, nil
	}
	return nil, &Error{Op: op, Code: "not_configured", Retryable: true,
		Err: errors.New("no admin has configured sandbox provider " + s.name)}
}

// Name implements Provider.
func (s *Swappable) Name() string { return s.name }

// Capabilities implements Provider. With no provider set it has none.
func (s *Swappable) Capabilities() Capabilities {
	if p := s.get(); p != nil {
		return p.Capabilities()
	}
	return Capabilities{}
}

// Create implements Provider. A disabled provider makes no new sandboxes.
func (s *Swappable) Create(ctx context.Context, req CreateRequest) (string, error) {
	if !s.Ready() {
		return "", &Error{Op: "create", Code: "not_configured", Retryable: true,
			Err: errors.New("sandbox provider " + s.name + " is not configured on the control plane")}
	}
	p, err := s.current("create")
	if err != nil {
		return "", err
	}
	return p.Create(ctx, req)
}

// WaitReady implements Provider.
func (s *Swappable) WaitReady(ctx context.Context, id string) error {
	p, err := s.current("wait")
	if err != nil {
		return err
	}
	return p.WaitReady(ctx, id)
}

// PutFile implements Provider.
func (s *Swappable) PutFile(ctx context.Context, id, path string, r io.Reader, size int64) error {
	p, err := s.current("put file")
	if err != nil {
		return err
	}
	return p.PutFile(ctx, id, path, r, size)
}

// Exec implements Provider.
func (s *Swappable) Exec(ctx context.Context, id, cmd string, detached bool) (ExecResult, error) {
	p, err := s.current("exec")
	if err != nil {
		return ExecResult{}, err
	}
	return p.Exec(ctx, id, cmd, detached)
}

// Expose implements Provider.
func (s *Swappable) Expose(ctx context.Context, id string, port int) (Endpoint, error) {
	p, err := s.current("expose")
	if err != nil {
		return Endpoint{}, err
	}
	return p.Expose(ctx, id, port)
}

// ExtendTTL implements Provider.
func (s *Swappable) ExtendTTL(ctx context.Context, id string, d time.Duration) error {
	p, err := s.current("extend")
	if err != nil {
		return err
	}
	return p.ExtendTTL(ctx, id, d)
}

// Delete implements Provider.
func (s *Swappable) Delete(ctx context.Context, id string) error {
	p, err := s.current("delete")
	if err != nil {
		return err
	}
	return p.Delete(ctx, id)
}

// Limits implements Provider.
func (s *Swappable) Limits(ctx context.Context) (Limits, error) {
	p, err := s.current("limits")
	if err != nil {
		return Limits{}, err
	}
	return p.Limits(ctx)
}

var _ Provider = (*Swappable)(nil)
