package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/alvaroibarguen/podium/internal/agent/profiles"
	"github.com/alvaroibarguen/podium/internal/agent/store"
)

// overridesSettingKey is the settings row the profile overrides live in. One row, because
// the four fields are one decision an operator makes on one screen.
const overridesSettingKey = "profile.overrides"

// ReloadProfile rebuilds the profile a turn runs from — the profile directory, the skills
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
	// Re-read the directory. The web UI writes skill files now, so the file half is not
	// fixed at boot any more — and a reload that used the cached copy would swap in a
	// profile that disagrees with what is on disk until the next restart.
	//
	// A directory that cannot be re-read KEEPS THE COPY WE HAVE rather than failing the
	// reload. Two reasons: a conductor running the last good profile beats one running
	// none, and this same function is what commits a stored-skill edit — so a hand-editing
	// mistake in a YAML file must not also block the database half of the profile. The
	// operator finds out on the next write to the directory, which reports it directly.
	if files.Dir != "" {
		if reread, err := profiles.Load(files.Dir); err == nil {
			live.SetFiles(reread)
			files = reread
		}
	}
	ov, err := readOverrides(ctx, st)
	if err != nil {
		return nil, err
	}
	stored, err := st.ListStoredSkills(ctx)
	if err != nil {
		return nil, err
	}
	merged, _, err := profiles.Merge(files, ov, skillsOf(stored))
	if err != nil {
		return nil, err
	}
	live.Set(merged)
	return merged, nil
}

// ReloadProfile is the same rebuild, remembering why it last failed so GetProfile can say
// the running profile is behind the database rather than leaving a browser to guess.
func (s *AgentService) ReloadProfile(ctx context.Context) error {
	if s.profiles == nil || s.store == nil {
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this conductor has no profile loaded"))
	}
	_, err := ReloadProfile(ctx, s.store, s.profiles)
	s.setStaleReason(err)
	return err
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

func skillsOf(rows []store.StoredSkill) []profiles.Skill {
	out := make([]profiles.Skill, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Skill)
	}
	return out
}
