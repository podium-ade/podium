package spec

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseExpose(t *testing.T) {
	s, err := ParseTaskSpec(strings.NewReader(`
image: alpine:3
sidecars:
  accounts: { image: alpine:3 }
expose:
  ports:
    web: { port: 3000 }
    accounts-api: { port: 5011, from: accounts }
`))
	require.NoError(t, err)
	require.NotNil(t, s.Expose)
	assert.Equal(t, Duration(DefaultExposeTTL), s.Expose.TTL)
	assert.Equal(t, ExposedPort{Port: 5011, From: "accounts"}, s.Expose.Ports["accounts-api"])
	assert.Equal(t, "PODIUM_URL_"+"ACCOUNTS_API", URLEnv("accounts-api"))
}

func TestValidateExpose(t *testing.T) {
	cases := map[string]struct {
		expose Expose
		want   string
	}{
		"no ports":        {Expose{TTL: Duration(time.Hour)}, "at least one port"},
		"bad via":         {Expose{TTL: Duration(time.Hour), Via: "wan", Ports: map[string]ExposedPort{"web": {Port: 80}}}, `expose.via "wan"`},
		"bad name":        {Expose{TTL: Duration(time.Hour), Ports: map[string]ExposedPort{"Web": {Port: 80}}}, "name must match"},
		"bad port":        {Expose{TTL: Duration(time.Hour), Ports: map[string]ExposedPort{"web": {Port: 70000}}}, "not a port number"},
		"duplicate port":  {Expose{TTL: Duration(time.Hour), Ports: map[string]ExposedPort{"a": {Port: 80}, "b": {Port: 80}}}, "already exposed as a"},
		"unknown sidecar": {Expose{TTL: Duration(time.Hour), Ports: map[string]ExposedPort{"web": {Port: 80, From: "db"}}}, `from "db" is not a sidecar`},
		"negative ttl":    {Expose{TTL: Duration(-time.Second), Ports: map[string]ExposedPort{"web": {Port: 80}}}, "expose.ttl must be positive"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := &TaskSpec{Image: "alpine:3", Expose: &tc.expose}
			s.ApplyDefaults()
			err := s.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestProtoRoundTripExpose(t *testing.T) {
	want := &TaskSpec{
		Image:    "alpine:3",
		Sidecars: map[string]Sidecar{"db": {Image: "postgres:16"}},
		Expose: &Expose{
			TTL: Duration(2 * time.Hour),
			Via: ExposeViaLAN,
			Ports: map[string]ExposedPort{
				"web": {Port: 3000},
				"db":  {Port: 5432, From: "db"},
			},
		},
	}
	want.ApplyDefaults()
	assert.Equal(t, want, FromProto(want.ToProto()))
}
