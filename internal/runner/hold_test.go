package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunHoldsAfterTheCommandExits is the preview contract from inside the container: the
// command's exit is reported at once, marked as holding, and what it left in the background
// keeps running until a SIGTERM ends the hold.
func TestRunHoldsAfterTheCommandExits(t *testing.T) {
	dir := shortDir(t)
	node := newFakeNode(t, dir)
	cfg, _, _ := baseConfig(t, dir)
	cfg.EventsSock = node.path
	cfg.Hold = true
	cfg.HoldExitFile = filepath.Join(dir, "exit")
	pidFile := filepath.Join(dir, "bg.pid")

	done := make(chan int, 1)
	go func() {
		done <- Run(context.Background(), cfg, []string{"sh", "-c",
			"sleep 60 & echo $! > " + pidFile + "; exit 4"})
	}()

	exit := waitForFile(t, cfg.HoldExitFile)
	var ev map[string]any
	if err := json.Unmarshal(exit, &ev); err != nil {
		t.Fatalf("hold exit file %q: %v", exit, err)
	}
	if ev["exit_code"] != float64(4) || ev["holding"] != true {
		t.Fatalf("hold exit file = %v, want exit_code 4 and holding", ev)
	}

	select {
	case code := <-done:
		t.Fatalf("Run returned %d while it should be holding", code)
	case <-time.After(300 * time.Millisecond):
	}
	bg, err := strconv.Atoi(strings.TrimSpace(string(waitForFile(t, pidFile))))
	if err != nil {
		t.Fatalf("background pid: %v", err)
	}
	if err := syscall.Kill(bg, 0); err != nil {
		t.Fatalf("the background process died with the command: %v", err)
	}

	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	select {
	case code := <-done:
		if code != 4 {
			t.Errorf("Run = %d after the hold, want the command's own 4", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM did not end the hold")
	}

	evs := node.events(t)
	last := evs[len(evs)-1]
	if last["kind"] != "exited" || last["holding"] != true {
		t.Errorf("last event = %v, want exited with holding", last)
	}
}

// TestRunDoesNotHoldACancelledCommand: a command that exits because it was told to stop
// was cancelled, not finished, and a cancelled task has nothing to preview.
func TestRunDoesNotHoldACancelledCommand(t *testing.T) {
	dir := shortDir(t)
	node := newFakeNode(t, dir)
	cfg, _, _ := baseConfig(t, dir)
	cfg.EventsSock = node.path
	cfg.Hold = true
	cfg.HoldExitFile = filepath.Join(dir, "exit")

	ready := filepath.Join(dir, "ready")
	go signalSelfWhenReady(ready)
	code := Run(context.Background(), cfg, []string{"sh", "-c",
		`trap 'exit 143' TERM; : > ` + ready + `; while :; do sleep 0.1; done`})
	if code != 143 {
		t.Fatalf("exit code = %d, want 143", code)
	}
	if _, err := os.Stat(cfg.HoldExitFile); err == nil {
		t.Error("a cancelled command wrote a hold exit file")
	}
	evs := node.events(t)
	if last := evs[len(evs)-1]; last["holding"] != nil {
		t.Errorf("last event = %v, want no holding", last)
	}
}

func waitForFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
	return nil
}
