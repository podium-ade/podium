package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
	agentv1 "github.com/alvaroibarguen/podium/internal/proto/podium/agent/v1"
)

// overridesSettingKey is the settings row the profile overrides live in. One row, because
// the four fields are one decision an operator makes on one screen.
const overridesSettingKey = "profile.overrides"

// ReloadProfile rebuilds the profile a turn runs from — the profile directory, the playbooks
// the database holds and the stored overrides — and swaps it into live.
//
// This is the whole of "a change reaches a running conductor without a restart": every
// reader takes live.Current() per use, so the swap is the only thing that has to happen.
// It is a plain function because the process calls it at boot, before the AgentService
// exists, as well as on every write and on a timer.
func ReloadProfile(ctx context.Context, st *store.Store, live *profiles.Live) (*profiles.Profile, error) {
	files := live.Files()
	if files == nil {
		return nil, errors.New("this conductor has no profile directory loaded")
	}
	merged, err := mergeStored(ctx, st, files)
	if err != nil {
		return nil, err
	}
	live.Set(merged)
	return merged, nil
}

// ReloadProfileDir is the same rebuild after re-reading the profile directory off disk, which
// is the half a restart used to be the only way to change: profile.yaml, playbooks/*.yaml and
// every prompt a `file:` points at.
//
// **Nothing is swapped until all of it succeeds.** A directory in the middle of being edited
// is a load error, and a load error has to leave the conductor running the profile it already
// has rather than half of a new one. That is also why this is only ever called for an
// operator who asked: a timer re-reading a file somebody is saving would be a way to break a
// working bot by touching a keyboard.
func ReloadProfileDir(
	ctx context.Context, st *store.Store, live *profiles.Live, dir string,
) (*profiles.Profile, error) {
	if dir == "" {
		return nil, errors.New("this conductor has no profile directory to re-read")
	}
	files, err := profiles.Load(dir)
	if err != nil {
		return nil, err
	}
	merged, err := mergeStored(ctx, st, files)
	if err != nil {
		return nil, err
	}
	live.SetFiles(files)
	live.Set(merged)
	return merged, nil
}

// mergeStored is the profile the files and the database make together, validated. It swaps
// nothing: both callers above decide what to do with what it returns.
func mergeStored(
	ctx context.Context, st *store.Store, files *profiles.Profile,
) (*profiles.Profile, error) {
	ov, err := readOverrides(ctx, st)
	if err != nil {
		return nil, err
	}
	stored, err := st.ListStoredPlaybooks(ctx)
	if err != nil {
		return nil, err
	}
	merged, _, err := profiles.Merge(files, ov, playbooksOf(stored))
	if err != nil {
		return nil, err
	}
	return merged, nil
}

// reloadProfile is the same rebuild, remembering why it last failed so GetProfile can say
// the running profile is behind the database rather than leaving a browser to guess.
//
// **Call it holding writeMu.** Every caller has just written the row it rebuilds from and
// holds the lock already; the one that had not was the reconcile timer, which is why that
// goes through Reconcile below.
func (s *AgentService) reloadProfile(ctx context.Context) error {
	if s.profiles == nil || s.store == nil {
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	_, err := ReloadProfile(ctx, s.store, s.profiles)
	s.setStaleReason(err)
	return err
}

// Reconcile is the timer's rebuild: the stored half, re-read on an interval so a row changed
// by another conductor or by psql lands here too.
//
// It takes the write lock, and that is the whole reason it exists as a method of its own. A
// rebuild is a read of the file half, a read of the database and then a swap; a timer doing
// that unlocked could read the files, have ReloadProfileDir replace them underneath it, and
// then swap in a profile merged from a directory nobody is running any more — undoing an
// operator's re-read seconds after they asked for it.
func (s *AgentService) Reconcile(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.reloadProfile(ctx)
}

// ReloadProfileDir re-reads the profile directory and swaps what it finds in.
//
// The failure is an InvalidArgument and not an Internal on purpose: what fails here is
// almost always the YAML the operator has just written, and the message profiles.Load
// returns names the file and the line. Nothing is swapped when it does, so the stale reason
// is not touched either — the conductor is still running the profile it was, and the error
// in front of the operator is the whole report.
func (s *AgentService) ReloadProfileDir(
	ctx context.Context, _ *connect.Request[agentv1.ReloadProfileDirRequest],
) (*connect.Response[agentv1.ReloadProfileDirResponse], error) {
	if s.profiles == nil || s.store == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	if s.profileDir == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor was started with no profile directory, so there is "+
				"nothing on disk to re-read"))
	}

	// The same lock a playbook write takes: a re-read is a read-validate-swap of exactly the
	// set a concurrent save is validating against.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	merged, err := ReloadProfileDir(ctx, s.store, s.profiles, s.profileDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// It merged, so the database half merged too, whatever it last failed at.
	s.setStaleReason(nil)
	s.logger.InfoContext(ctx, "the profile directory was re-read", "login", Login(ctx),
		"profile_dir", s.profileDir, "playbooks", merged.PlaybookNames())
	return connect.NewResponse(&agentv1.ReloadProfileDirResponse{}), nil
}

func (s *AgentService) setStaleReason(err error) {
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	s.stale.Store(&reason)
}

func (s *AgentService) staleReason() string {
	if r := s.stale.Load(); r != nil {
		return *r
	}
	return ""
}

func readOverrides(ctx context.Context, st *store.Store) (profiles.Overrides, error) {
	var ov profiles.Overrides
	err := st.GetSetting(ctx, overridesSettingKey, &ov)
	if errors.Is(err, store.ErrNotFound) {
		return profiles.Overrides{}, nil
	}
	if err != nil {
		return profiles.Overrides{}, err
	}
	return ov, nil
}

func playbooksOf(rows []store.StoredPlaybook) []profiles.Playbook {
	out := make([]profiles.Playbook, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Playbook)
	}
	return out
}
