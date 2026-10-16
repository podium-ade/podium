// Package sandbox is the provider-neutral face of a cloud sandbox: a Linux machine or
// container somebody else runs, that podium-node can create, put files in, start one
// process in, reach one port of, and delete. Everything Podium does inside it is the
// sandbox agent's job; a provider only has to supply these primitives.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Provider is one sandbox service. Implementations must be safe for concurrent use.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	// Create starts a sandbox and returns its ID without waiting for it to be usable.
	// It is idempotent on req.Key: a retry with the same key returns the same sandbox.
	Create(ctx context.Context, req CreateRequest) (string, error)
	// WaitReady blocks until commands and files work in the sandbox.
	WaitReady(ctx context.Context, id string) error
	// PutFile writes r to path in the sandbox, creating parent directories.
	PutFile(ctx context.Context, id, path string, r io.Reader, size int64) error
	// Exec runs a shell command. It is for bootstrapping the agent, never for task output.
	// A detached command returns as soon as it has started and outlives the call.
	Exec(ctx context.Context, id, cmd string, detached bool) (ExecResult, error)
	// Expose makes a port in the sandbox reachable from podium-node over HTTP(S). See
	// Capabilities.ExposeLoopback for the address the service has to listen on.
	Expose(ctx context.Context, id string, port int) (Endpoint, error)
	// ExtendTTL moves the sandbox's automatic stop to d from now. A provider that cannot
	// change a running sandbox's lifetime may emulate it, up to Capabilities.MaxTTL.
	ExtendTTL(ctx context.Context, id string, d time.Duration) error
	// Delete destroys the sandbox and its data. Deleting one that is gone is not an error.
	Delete(ctx context.Context, id string) error
	// Limits reports how much more this account may start right now.
	Limits(ctx context.Context) (Limits, error)
}

// Capabilities is what a provider's sandboxes can do, which decides how the agent runs a
// task in one.
type Capabilities struct {
	// NativeImage: a sandbox can be created from an OCI image.
	NativeImage bool
	// Snapshot: a sandbox can be created from a provider snapshot.
	Snapshot bool
	// Docker: a Docker engine runs inside the sandbox.
	Docker bool
	// Root: the agent can become root, so it can run the task as a user of its own.
	Root bool
	// Tmpfs: the agent can mount a tmpfs, so secrets never touch the sandbox's disk.
	Tmpfs bool
	// ExposeLoopback: Expose reaches a service bound to 127.0.0.1. Without it the service
	// has to listen on every interface.
	ExposeLoopback bool
	// MaxTTL is the longest a sandbox may live. Zero is no limit.
	MaxTTL time.Duration
}

// CreateRequest describes one sandbox. At most one of Image and Snapshot is set; neither
// means the provider's default base.
type CreateRequest struct {
	// Key makes Create idempotent. podium-node uses the lease ID.
	Key      string
	Image    string
	Snapshot string
	CPU      float64
	MemoryMB int
	TTL      time.Duration
	// Registry is the login for the registry Image comes from, when Podium holds one. A
	// provider that pulls the image itself needs it for a private one.
	Registry *RegistryLogin
	// Ports are every port Expose may later be called for. A provider that has to declare
	// them when the sandbox is made (a published container port) uses this; the others
	// ignore it.
	Ports []int
	// Settings are the task's overrides of the provider's own settings, for this sandbox
	// only. Only a provider that is an Overrider is given any.
	Settings map[string]string
}

// Overrider is a provider a task may override some settings of, for its own sandbox.
type Overrider interface {
	// Override checks settings and returns the capabilities a sandbox made with them has.
	Override(settings map[string]string) (Capabilities, error)
}

// RegistryLogin is a registry username and password. Password is SENSITIVE.
type RegistryLogin struct {
	Username string
	Password string
}

// ExecResult is a finished command, or a started one when it was detached.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Endpoint is an exposed port: its URL, and what every request to it has to carry for the
// provider to let it through.
type Endpoint struct {
	URL     string
	Header  http.Header
	Cookies []*http.Cookie
	// BrowserURL is what a person opens: URL with the provider's credentials in it, when
	// the provider puts them in a link. Empty means URL.
	BrowserURL string
}

// Apply adds the endpoint's credentials to r.
func (e Endpoint) Apply(r *http.Request) {
	for k, vs := range e.Header {
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	for _, c := range e.Cookies {
		r.AddCookie(c)
	}
}

// Limits is the account's remaining room. A negative field is unknown.
type Limits struct {
	MaxSandboxes  int
	FreeSandboxes int
	StartsLeft    int
}

// Error is a provider failure podium-node can act on.
type Error struct {
	Op   string
	Code string
	// Retryable means the same call may succeed later; RetryAfter, when set, says when.
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	msg := e.Op + ": " + e.Code
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// IsRetryable reports whether err is a provider error worth trying again, and after how long.
func IsRetryable(err error) (time.Duration, bool) {
	var pe *Error
	if errors.As(err, &pe) && pe.Retryable {
		return pe.RetryAfter, true
	}
	return 0, false
}

// Factory builds a provider from its block of the node's sandbox config.
type Factory func(cfg map[string]any) (Provider, error)

var (
	mu        sync.Mutex
	factories = map[string]Factory{}
)

// Register makes a provider available by name. It is called from an init function.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[name]; dup {
		panic("sandbox: provider registered twice: " + name)
	}
	factories[name] = f
}

// New builds the named provider.
func New(name string, cfg map[string]any) (Provider, error) {
	mu.Lock()
	f, ok := factories[name]
	mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("sandbox provider %q is not one of %v", name, Names())
	}
	return f(cfg)
}

// Names lists the registered providers.
func Names() []string {
	mu.Lock()
	defer mu.Unlock()
	out := make([]string, 0, len(factories))
	for n := range factories {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
