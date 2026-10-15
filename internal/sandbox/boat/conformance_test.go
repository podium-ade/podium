//go:build boat

package boat

import (
	"os"
	"testing"

	"github.com/podium-ade/podium/internal/sandbox/sandboxtest"
)

// TestConformance needs BOAT_API_KEY_FILE and starts one small sandbox.
func TestConformance(t *testing.T) {
	keyFile := os.Getenv("BOAT_API_KEY_FILE")
	if keyFile == "" {
		t.Skip("BOAT_API_KEY_FILE is not set")
	}
	p, err := New(Config{APIKeyFile: keyFile, DefaultType: "small"})
	if err != nil {
		t.Fatal(err)
	}
	sandboxtest.Run(t, p, sandboxtest.Options{ServeCmd: "python3 -m http.server --bind %s %d"})
}
