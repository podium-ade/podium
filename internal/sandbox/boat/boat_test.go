package boat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/sandbox"
)

func newTest(t *testing.T, h http.HandlerFunc) *Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("boat_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{APIKeyFile: key, APIURL: srv.URL, DefaultType: "small"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateSendsSafeDefaultsAndRetriesStarts(t *testing.T) {
	var calls atomic.Int32
	p := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer boat_test" || r.Header.Get("Idempotency-Key") != "lease-1" {
			t.Errorf("headers: %v", r.Header)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["noEnv"] != true || body["snapshots"] != false || body["monitoring"] != false ||
			body["from"] != "tmpl" || body["ttlSeconds"] != float64(600) {
			t.Errorf("body: %v", body)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"code":"rate_limited","message":"slow down","window":"minute","retryAfterSeconds":0.01}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true,"type":"sandbox.created","sandbox":{"id":"bx_23456789","state":"provisioning"}}`))
	})
	id, err := p.Create(context.Background(), sandbox.CreateRequest{Key: "lease-1", Snapshot: "tmpl", TTL: 10 * time.Minute})
	if err != nil || id != "bx_23456789" || calls.Load() != 2 {
		t.Fatalf("got %q, %v after %d calls", id, err, calls.Load())
	}
}

func TestErrorsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		code      string
		retryable bool
		after     time.Duration
	}{
		{503, `{"code":"out_of_capacity","retryAfterSeconds":120}`, "out_of_capacity", true, 120 * time.Second},
		{409, `{"code":"boat_starting"}`, "boat_starting", true, 0},
		{409, `{"code":"sandbox_starting"}`, "sandbox_starting", true, 0},
		{429, `{"code":"limit_reached"}`, "limit_reached", false, 0},
		{429, `{"code":"rate_limited","window":"hour"}`, "rate_limited", true, time.Minute},
		{502, `{"code":"boat_direct_failed","error":{"details":{"retryable":true}}}`, "boat_direct_failed", true, 0},
		{404, `{"code":"not_found"}`, "not_found", false, 0},
	} {
		var ae apiError
		if err := json.Unmarshal([]byte(tc.body), &ae); err != nil {
			t.Fatal(err)
		}
		e := toError("op", tc.status, ae)
		if e.Code != tc.code || e.Retryable != tc.retryable || e.RetryAfter != tc.after {
			t.Errorf("%s: got %+v", tc.body, e)
		}
	}
}

func TestDeleteConfirmsAndToleratesGone(t *testing.T) {
	p := newTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.Header.Get("X-Ascii-Confirm-Delete") != "bx_23456789" {
			t.Errorf("%s %v", r.Method, r.Header)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"code":"not_found"}`))
	})
	if err := p.Delete(context.Background(), "bx_23456789"); err != nil {
		t.Fatal(err)
	}
}

func TestExposeMovesTheTokenIntoACookie(t *testing.T) {
	p := newTest(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"type":"port.hosted","url":"https://x-7700.on.boat.dev?_token=abc"}`))
	})
	ep, err := p.Expose(context.Background(), "bx_23456789", 7700)
	if err != nil || ep.URL != "https://x-7700.on.boat.dev" || len(ep.Cookies) != 1 || ep.Cookies[0].Value != "abc" {
		t.Fatalf("%+v %v", ep, err)
	}
}

func TestCreateRefusesAnImage(t *testing.T) {
	p := newTest(t, func(http.ResponseWriter, *http.Request) { t.Error("no request expected") })
	_, err := p.Create(context.Background(), sandbox.CreateRequest{Image: "alpine:3"})
	var pe *sandbox.Error
	if !errors.As(err, &pe) || pe.Code != "unsupported" {
		t.Fatal(err)
	}
}

func TestMachineType(t *testing.T) {
	p := &Provider{defType: "small"}
	for _, tc := range []struct {
		cpu  float64
		mem  int
		want string
	}{{0, 0, "small"}, {1, 0, "small"}, {3, 1024, "default"}, {2, 8192, "default"}, {8, 16384, "large"}, {64, 0, "xlarge"}} {
		if got := p.machineType(tc.cpu, tc.mem); got != tc.want {
			t.Errorf("%v/%d: %s, want %s", tc.cpu, tc.mem, got, tc.want)
		}
	}
}
