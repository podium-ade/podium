package wire

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
)

func TestEventsSurviveTheWire(t *testing.T) {
	ts := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	for _, ev := range []docker.Event{
		{Seq: 1, Kind: docker.KindProvisioning, TS: ts},
		{Seq: 2, Kind: docker.KindLog, TS: ts, Payload: docker.LogPayload{Stream: docker.StreamStdout, Bytes: []byte("hi\n"), Offset: 3}},
		{Seq: 3, Kind: docker.KindStep, TS: ts, Payload: docker.StepPayload{Name: "sidecar/db", Status: "ready"}},
		{Seq: 4, Kind: docker.KindArtifact, TS: ts, Payload: docker.ArtifactPayload{ArtifactID: "a", Name: "x.png", SizeBytes: 9}},
		{Seq: 5, Kind: docker.KindPreview, TS: ts, Payload: docker.PreviewPayload{Via: "sandbox", URLs: map[string]string{"web": "https://x"}}},
		{Seq: 6, Kind: docker.KindExited, TS: ts, Payload: docker.ExitedPayload{ExitCode: 137, OOMKilled: true}},
		{Seq: 7, Kind: docker.KindFinished, TS: ts, Payload: docker.FinishedPayload{ExitCode: 137, Held: true, Usage: docker.Usage{WallMS: 5}}},
		{Seq: 8, Kind: docker.KindError, TS: ts, Payload: docker.ErrorPayload{Message: "boom", AbortsRun: true}},
		{Seq: 9, Kind: docker.KindMessage, TS: ts, Payload: docker.MessagePayload{Text: "done", Attachments: []string{"x.png"}}},
	} {
		we, err := FromEvent(ev)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(Frame{Event: &we})
		if err != nil {
			t.Fatal(err)
		}
		var f Frame
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		got, err := f.Event.ToEvent()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, ev) {
			t.Errorf("%s: got %#v, want %#v", ev.Kind, got, ev)
		}
	}
}
