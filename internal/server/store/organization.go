package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/podium-ade/podium/internal/server/store/db"
)

// Caps on the two names. The table checks them too; these are here so a caller gets a
// sentence rather than a constraint violation.
const (
	MaxOrganizationNameRunes = 80
	MaxDeploymentNameRunes   = 40
)

// Organization is what this deployment is called and whose it is. Empty fields are "not
// set": a reader falls back to its own default (Podium) rather than showing a blank.
type Organization struct {
	OrganizationName string
	DeploymentName   string
	UpdatedBy        string
	UpdatedAt        time.Time
}

// GetOrganization reads the names. A deployment that never set them gets the zero value.
func (s *Store) GetOrganization(ctx context.Context) (Organization, error) {
	row, err := s.q.GetOrganization(ctx)
	if noRows(err) {
		return Organization{}, nil
	}
	if err != nil {
		return Organization{}, fmt.Errorf("get organization: %w", err)
	}
	return organizationFromRow(row), nil
}

// SetOrganization replaces both names. Surrounding space is trimmed; a name with a line
// break or over its cap is refused.
func (s *Store) SetOrganization(ctx context.Context, in Organization) (Organization, error) {
	in.OrganizationName = strings.TrimSpace(in.OrganizationName)
	in.DeploymentName = strings.TrimSpace(in.DeploymentName)
	if err := ValidateOrganization(in); err != nil {
		return Organization{}, err
	}
	row, err := s.q.SetOrganization(ctx, db.SetOrganizationParams{
		OrganizationName: in.OrganizationName,
		DeploymentName:   in.DeploymentName,
		UpdatedBy:        in.UpdatedBy,
	})
	if err != nil {
		return Organization{}, fmt.Errorf("set organization: %w", err)
	}
	return organizationFromRow(row), nil
}

// ValidateOrganization says why a pair of names cannot be saved, or nil. The names are
// expected trimmed.
func ValidateOrganization(in Organization) error {
	if err := validateName("organization name", in.OrganizationName, MaxOrganizationNameRunes); err != nil {
		return err
	}
	return validateName("deployment name", in.DeploymentName, MaxDeploymentNameRunes)
}

func validateName(what, v string, limit int) error {
	if !utf8.ValidString(v) {
		return fmt.Errorf("%w: the %s is not valid UTF-8", ErrInvalidArgument, what)
	}
	if n := utf8.RuneCountInString(v); n > limit {
		return fmt.Errorf("%w: the %s is %d characters; the limit is %d", ErrInvalidArgument, what, n, limit)
	}
	if strings.ContainsAny(v, "\r\n\t<>") {
		return fmt.Errorf("%w: the %s cannot hold a line break, a tab, < or >", ErrInvalidArgument, what)
	}
	return nil
}

func organizationFromRow(row db.Organization) Organization {
	return Organization{
		OrganizationName: row.OrganizationName,
		DeploymentName:   row.DeploymentName,
		UpdatedBy:        row.UpdatedBy,
		UpdatedAt:        row.UpdatedAt,
	}
}
