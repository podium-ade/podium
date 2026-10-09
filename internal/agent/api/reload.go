package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/podium-ade/podium/internal/agent/profiles"
	"github.com/podium-ade/podium/internal/agent/store"
	agentv1 "github.com/podium-ade/podium/internal/proto/podium/agent/v1"
)

// overridesSettingKey is the legacy overlay. It still applies when no assistant catalog
// has been saved, so a conductor that has not opened the new screen keeps answering.
const overridesSettingKey = "profile.overrides"

// assistantsSettingKey is the catalog of assistant definitions. The conductor runs the
// active one. A later create adds a definition to this same row.
const assistantsSettingKey = "profile.assistants"

// ReloadProfile rebuilds the profile a turn runs from and swaps it into live.
//
// A saved assistant definition replaces the assistant fields of the install file. Until
// one has been saved, the legacy override row is applied instead. Playbooks come from
// the profile directory either way.
//
// This is the whole of "a change reaches a running conductor without a restart": every
// reader takes live.Current() per use, so the swap is the only thing that has to happen.
// It is a plain function because the process calls it at boot, before the AgentService
// exists, as well as on every override write and on a timer.
func ReloadProfile(ctx context.Context, st *store.Store, live *profiles.Live) (*profiles.Profile, error) {
	files := live.Files()
	if files == nil {
		return nil, errors.New("this conductor has no profile directory loaded")
	}
	applied, err := applyStored(ctx, st, files)
	if err != nil {
		return nil, err
	}
	live.Set(applied)
	return applied, nil
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
	applied, err := applyStored(ctx, st, files)
	if err != nil {
		return nil, err
	}
	live.SetFiles(files)
	live.Set(applied)
	return applied, nil
}

func applyStored(
	ctx context.Context, st *store.Store, files *profiles.Profile,
) (*profiles.Profile, error) {
	if st == nil {
		return files, nil
	}
	cat, found, err := readCatalog(ctx, st)
	if err != nil {
		return nil, err
	}
	if found {
		def, ok := cat.Active()
		if !ok {
			return nil, errors.New("the saved assistants have no active definition")
		}
		return profiles.ApplyDefinition(files, def)
	}
	ov, err := readOverrides(ctx, st)
	if err != nil {
		return nil, err
	}
	return profiles.Apply(files, ov)
}

// reloadProfile is the same rebuild, remembering why it last failed so GetProfile can say
// the running profile is behind the overrides rather than leaving a browser to guess.
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

// Reconcile is the timer's rebuild: the saved assistant, re-read on an interval so a row
// changed by another conductor or by psql lands here too. A conductor that has not saved
// one still re-reads the legacy override row.
//
// It takes the write lock, and that is the whole reason it exists as a method of its own. A
// rebuild is a read of the file half, a read of the database and then a swap; a timer doing
// that unlocked could read the files, have ReloadProfileDir replace them underneath it, and
// then swap in a profile built from a directory nobody is running any more — undoing an
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

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	applied, err := ReloadProfileDir(ctx, s.store, s.profiles, s.profileDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.setStaleReason(nil)
	s.logger.InfoContext(ctx, "the profile directory was re-read", "login", Login(ctx),
		"profile_dir", s.profileDir, "playbooks", applied.PlaybookNames())
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

func readCatalog(ctx context.Context, st *store.Store) (profiles.AssistantCatalog, bool, error) {
	var cat profiles.AssistantCatalog
	err := st.GetSetting(ctx, assistantsSettingKey, &cat)
	if errors.Is(err, store.ErrNotFound) {
		return profiles.AssistantCatalog{}, false, nil
	}
	if err != nil {
		return profiles.AssistantCatalog{}, false, err
	}
	return cat, true, nil
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
