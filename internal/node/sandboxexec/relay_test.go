package sandboxexec

import (
	"testing"

	"github.com/podium-ade/podium/internal/node/docker"
)

func TestAdoptFilterSkipsWhatTheServerHas(t *testing.T) {
	f := &adoptFilter{skipStdout: 5, skipStderr: 0, seq: 4}
	log := func(stream, s string, offset int64) docker.Event {
		return docker.Event{Kind: docker.KindLog, Payload: docker.LogPayload{Stream: stream, Bytes: []byte(s), Offset: offset}}
	}
	for _, tc := range []struct {
		ev   docker.Event
		keep bool
		text string
	}{
		{log(docker.StreamStdout, "abc", 3), false, ""},
		{log(docker.StreamStdout, "defg", 7), true, "fg"},
		{log(docker.StreamStdout, "hi", 9), true, "hi"},
		{log(docker.StreamStderr, "e", 1), true, "e"},
		{docker.Event{Kind: docker.KindLog, Payload: docker.LogPayload{Stream: docker.StreamSidecar, Bytes: []byte("x"), Offset: 1}}, false, ""},
		{docker.Event{Seq: 3, Kind: docker.KindArtifact}, false, ""},
		{docker.Event{Seq: 5, Kind: docker.KindArtifact}, true, ""},
		{docker.Event{Seq: 2, Kind: docker.KindFinished}, true, ""},
	} {
		ev := tc.ev
		if got := f.keep(&ev); got != tc.keep {
			t.Errorf("%v: keep %v, want %v", tc.ev, got, tc.keep)
			continue
		}
		if p, ok := ev.Payload.(docker.LogPayload); ok && tc.keep && string(p.Bytes) != tc.text {
			t.Errorf("%v: kept %q, want %q", tc.ev, p.Bytes, tc.text)
		}
	}
}

func TestRegistryForPicksTheImagesOwnRegistry(t *testing.T) {
	creds := []docker.RegistryCredential{
		{Host: "ghcr.io", Username: "octo", Password: []byte("ghp")},
		{Host: "us-docker.pkg.dev", Username: "_json_key", Password: []byte("{}")},
	}
	got := registryFor("us-docker.pkg.dev/acme/images/app:1", creds)
	if got == nil || got.Username != "_json_key" || got.Password != "{}" {
		t.Fatalf("got %+v", got)
	}
	if got := registryFor("alpine:3", creds); got != nil {
		t.Fatalf("a public image gets no login, got %+v", got)
	}
	if got := registryFor("", creds); got != nil {
		t.Fatalf("a snapshot task gets no login, got %+v", got)
	}
}
