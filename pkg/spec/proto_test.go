package spec

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProtoRoundTrip(t *testing.T) {
	want := &TaskSpec{
		Image:       "ghcr.io/acme/build:1",
		Command:     []string{"sh", "-c", "make"},
		WorkingDir:  "/src",
		Env:         map[string]string{"CI": "true"},
		Labels:      []string{"linux/arm64"},
		Timeout:     Duration(30 * time.Second),
		MaxAttempts: 3,
	}

	p := want.ToProto()
	require.NotNil(t, p)
	assert.Equal(t, want.Image, p.GetImage())
	assert.Equal(t, 30*time.Second, p.GetTimeout().AsDuration())

	assert.Equal(t, want, FromProto(p))
}

func TestProtoRoundTripDefaults(t *testing.T) {
	want := &TaskSpec{Image: "alpine:3"}
	want.ApplyDefaults()
	assert.Equal(t, want, FromProto(want.ToProto()))
}

func TestProtoNil(t *testing.T) {
	var s *TaskSpec
	assert.Nil(t, s.ToProto())
	assert.Nil(t, FromProto(nil))
}
