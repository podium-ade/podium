package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmailDomain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		login string
		want  string
	}{
		{"alice@acme.com", "acme.com"},
		{"Alice@Acme.COM", "acme.com"},
		{"alice@gmail.com", ""},
		{"alice@googlemail.com", ""},
		{"not-an-email", ""},
		{"@acme.com", "acme.com"},
		{"alice@", ""},
		{"alice@localhost", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.login, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, EmailDomain(tc.login))
		})
	}
}

func TestUserDomainPrefersHostedDomain(t *testing.T) {
	t.Parallel()
	require.Equal(t, "acme.com", UserDomain(User{Login: "bob@alias.com", HostedDomain: "acme.com"}))
	require.Equal(t, "alias.com", UserDomain(User{Login: "bob@alias.com"}))
	require.Equal(t, "", UserDomain(User{Login: "bob@gmail.com"}))
}
