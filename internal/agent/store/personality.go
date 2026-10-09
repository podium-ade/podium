package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/podium-ade/podium/internal/agent/profiles"
	db "github.com/podium-ade/podium/internal/agent/store/db"
	"github.com/podium-ade/podium/internal/ids"
)

// ErrInvalidPersonality is a voice the store will not write. The handler maps it
// to InvalidArgument.
var ErrInvalidPersonality = errors.New("invalid personality")

const (
	// reservedPersonalityName is the built-in assistant. A generated name is never exactly this.
	reservedPersonalityName = "podium"
	// maxPersonalityInstructionRunes caps the text appended after Podium's prompt.
	maxPersonalityInstructionRunes = 4000
	// maxPersonalityNameLen is the length profiles.NameRE allows.
	maxPersonalityNameLen = 32
	// personalityNameAttempts is how many times Create retries an insert that lost a name race.
	personalityNameAttempts = 8
)

// Personality is one person's voice on top of Podium. It has a name, a display name,
// and instructions. Agent, model, and effort are its default for a chat that does not
// pick its own. Empty follows Podium. It does not have skills or a computer.
type Personality struct {
	ID           string
	Login        string
	Name         string
	DisplayName  string
	Instructions string
	Agent        string
	Model        string
	Effort       string
	UpdatedAt    time.Time
}

// PersonalityDraft is what a save sends. The name is not one of the fields a person edits.
// Create assigns it from the display name, and Update keeps the name already stored.
type PersonalityDraft struct {
	Name         string
	DisplayName  string
	Instructions string
	Agent        string
	Model        string
	Effort       string
}

// PreparePersonality trims the display name and the instructions. A name on the draft is
// ignored, including an empty one.
func PreparePersonality(in PersonalityDraft) (PersonalityDraft, error) {
	display, err := cleanPersonalityDisplay(in.DisplayName)
	if err != nil {
		return PersonalityDraft{}, err
	}
	instructions := strings.TrimSpace(in.Instructions)
	if instructions == "" {
		return PersonalityDraft{}, fmt.Errorf("%w: instructions are required", ErrInvalidPersonality)
	}
	if n := utf8.RuneCountInString(instructions); n > maxPersonalityInstructionRunes {
		return PersonalityDraft{}, fmt.Errorf("%w: %d runes is more than the %d-rune limit for instructions",
			ErrInvalidPersonality, n, maxPersonalityInstructionRunes)
	}
	return PersonalityDraft{
		DisplayName:  display,
		Instructions: instructions,
		Agent:        strings.TrimSpace(in.Agent),
		Model:        strings.TrimSpace(in.Model),
		Effort:       strings.TrimSpace(in.Effort),
	}, nil
}

// PersonalityName is the stored name for a display name, avoiding names this login already has.
// It matches profiles.NameRE, stays within 32 characters, and is never exactly "podium".
func PersonalityName(display string, taken []string) (string, error) {
	used := make(map[string]struct{}, len(taken))
	for _, name := range taken {
		used[name] = struct{}{}
	}
	stem := personalitySlug(display)
	for n := 1; n <= 999; n++ {
		candidate := stem
		if n > 1 {
			candidate = personalitySuffixed(stem, n)
		}
		if candidate == reservedPersonalityName {
			continue
		}
		if _, ok := used[candidate]; ok {
			continue
		}
		if profiles.NameRE.MatchString(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: no free name", ErrConflict)
}

// personalitySlug turns a display name into a lowercase stem. Runs that are not ASCII letters
// or digits become one hyphen. A stem that would start with a digit is prefixed. An empty
// stem is "assistant".
func personalitySlug(display string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(display) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			hyphen = false
			continue
		}
		if b.Len() > 0 && !hyphen {
			b.WriteByte('-')
			hyphen = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "assistant"
	}
	if slug[0] >= '0' && slug[0] <= '9' {
		slug = "a-" + slug
	}
	if len(slug) > maxPersonalityNameLen {
		slug = strings.TrimRight(slug[:maxPersonalityNameLen], "-")
	}
	if slug == "" || slug[0] < 'a' || slug[0] > 'z' {
		return "assistant"
	}
	return slug
}

func personalitySuffixed(stem string, n int) string {
	suffix := fmt.Sprintf("-%d", n)
	room := maxPersonalityNameLen - len(suffix)
	base := stem
	if len(base) > room {
		base = base[:room]
	}
	base = strings.TrimRight(base, "-")
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "a"
	}
	return base + suffix
}

func cleanPersonalityDisplay(display string) (string, error) {
	display = strings.Join(strings.Fields(display), " ")
	if display == "" {
		return "", fmt.Errorf("%w: a display name is required", ErrInvalidPersonality)
	}
	if strings.ContainsFunc(display, unicode.IsControl) {
		return "", fmt.Errorf("%w: a display name cannot hold control characters", ErrInvalidPersonality)
	}
	n := utf8.RuneCountInString(display)
	if n > MaxChatTitleRunes {
		return "", fmt.Errorf("%w: %d runes is more than the %d-rune limit for a display name",
			ErrInvalidPersonality, n, MaxChatTitleRunes)
	}
	return display, nil
}

func personalityFromRow(row db.Personality) Personality {
	return Personality{
		ID:           row.ID,
		Login:        row.Login,
		Name:         row.Name,
		DisplayName:  row.DisplayName,
		Instructions: row.Instructions,
		Agent:        row.Agent,
		Model:        row.Model,
		Effort:       row.Effort,
		UpdatedAt:    row.UpdatedAt.UTC(),
	}
}

// ListPersonalities returns one login's voices, by name. Another login's rows are
// not in the result.
func (s *Store) ListPersonalities(ctx context.Context, login string) ([]Personality, error) {
	if login == "" {
		return nil, errors.New("list personalities: a login is required")
	}
	rows, err := s.q.ListPersonalities(ctx, login)
	if err != nil {
		return nil, fmt.Errorf("list personalities of %s: %w", login, err)
	}
	out := make([]Personality, 0, len(rows))
	for _, row := range rows {
		out = append(out, personalityFromRow(row))
	}
	return out, nil
}

// GetPersonality reads one voice by id, whoever owns it. The caller compares the
// login: a handler that must not leak another person's voice needs the row to do it.
func (s *Store) GetPersonality(ctx context.Context, id string) (Personality, error) {
	row, err := s.q.GetPersonality(ctx, id)
	if noRows(err) {
		return Personality{}, fmt.Errorf("%w: personality %s", ErrNotFound, id)
	}
	if err != nil {
		return Personality{}, fmt.Errorf("get personality %s: %w", id, err)
	}
	return personalityFromRow(row), nil
}

// CreatePersonality stores a new voice for login. The name is generated from the display
// name. A name on the draft is ignored. ErrInvalidPersonality means the draft was refused.
// ErrConflict means a free name could not be inserted.
func (s *Store) CreatePersonality(ctx context.Context, login string, in PersonalityDraft) (Personality, error) {
	if login == "" {
		return Personality{}, errors.New("create personality: a login is required")
	}
	prepared, err := PreparePersonality(in)
	if err != nil {
		return Personality{}, err
	}
	existing, err := s.ListPersonalities(ctx, login)
	if err != nil {
		return Personality{}, err
	}
	taken := make([]string, 0, len(existing))
	for _, row := range existing {
		taken = append(taken, row.Name)
	}
	var lastName string
	for attempt := 0; attempt < personalityNameAttempts; attempt++ {
		name, err := PersonalityName(prepared.DisplayName, taken)
		if err != nil {
			return Personality{}, err
		}
		lastName = name
		id := ids.New("pers")
		n, err := s.q.InsertPersonality(ctx, db.InsertPersonalityParams{
			ID:           id,
			Login:        login,
			Name:         name,
			DisplayName:  prepared.DisplayName,
			Instructions: prepared.Instructions,
			Agent:        prepared.Agent,
			Model:        prepared.Model,
			Effort:       prepared.Effort,
			UpdatedAt:    time.Now().UTC(),
		})
		if err != nil {
			return Personality{}, fmt.Errorf("insert personality %s: %w", name, err)
		}
		if n > 0 {
			return s.GetPersonality(ctx, id)
		}
		taken = append(taken, name)
	}
	return Personality{}, fmt.Errorf("%w: personality %s", ErrConflict, lastName)
}

// UpdatePersonality replaces the display name, instructions, and model of one of login's voices.
// The stored name stays, so a display-name edit does not rename chats that already recorded it.
// A name on the draft is ignored. A row that is missing or owned by somebody else is ErrNotFound.
func (s *Store) UpdatePersonality(ctx context.Context, id, login string, in PersonalityDraft) (Personality, error) {
	if id == "" {
		return Personality{}, errors.New("update personality: an id is required")
	}
	if login == "" {
		return Personality{}, errors.New("update personality: a login is required")
	}
	prepared, err := PreparePersonality(in)
	if err != nil {
		return Personality{}, err
	}
	current, err := s.GetPersonality(ctx, id)
	if err != nil {
		return Personality{}, err
	}
	if current.Login != login {
		return Personality{}, fmt.Errorf("%w: personality %s", ErrNotFound, id)
	}
	n, err := s.q.UpdatePersonality(ctx, db.UpdatePersonalityParams{
		ID:           id,
		Login:        login,
		Name:         current.Name,
		DisplayName:  prepared.DisplayName,
		Instructions: prepared.Instructions,
		Agent:        prepared.Agent,
		Model:        prepared.Model,
		Effort:       prepared.Effort,
		UpdatedAt:    time.Now().UTC(),
	})
	if isUniqueViolation(err) {
		return Personality{}, fmt.Errorf("%w: personality %s", ErrConflict, current.Name)
	}
	if err != nil {
		return Personality{}, fmt.Errorf("update personality %s: %w", id, err)
	}
	if n == 0 {
		return Personality{}, fmt.Errorf("%w: personality %s", ErrNotFound, id)
	}
	return s.GetPersonality(ctx, id)
}

// DeletePersonality removes one of login's voices. Chats that named it stay, labeled
// by the display name they snapshotted. ErrNotFound means it was not there or not theirs.
func (s *Store) DeletePersonality(ctx context.Context, id, login string) error {
	if id == "" {
		return errors.New("delete personality: an id is required")
	}
	if login == "" {
		return errors.New("delete personality: a login is required")
	}
	n, err := s.q.DeletePersonality(ctx, db.DeletePersonalityParams{ID: id, Login: login})
	if err != nil {
		return fmt.Errorf("delete personality %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("%w: personality %s", ErrNotFound, id)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
