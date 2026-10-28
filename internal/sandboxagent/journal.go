package sandboxagent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/podium-ade/podium/internal/sandboxagent/wire"
)

// journal is every event of the run, on disk, so a node that reconnects — or restarts and
// adopts the task — can read it again from any sequence number.
type journal struct {
	path string

	mu      sync.Mutex
	f       *os.File
	offsets []int64 // offsets[i] is where the event with seq i+1 starts
	size    int64
	changed chan struct{}
	closed  bool
}

func newJournal(path string) (*journal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &journal{path: path, f: f, changed: make(chan struct{})}, nil
}

func (j *journal) append(ev wire.Event) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Write(line); err != nil {
		return err
	}
	j.offsets = append(j.offsets, j.size)
	j.size += int64(len(line))
	j.wake()
	return nil
}

// close marks the journal complete: readers that reach its end stop instead of waiting.
func (j *journal) close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closed = true
	_ = j.f.Close()
	j.wake()
}

func (j *journal) wake() {
	close(j.changed)
	j.changed = make(chan struct{})
}

// follow sends every event from seq onwards to fn, waiting for new ones until the journal
// is closed or ctx ends.
func (j *journal) follow(ctx context.Context, from uint64, fn func(wire.Event) error) error {
	r, err := os.Open(j.path)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()

	j.mu.Lock()
	if from < 1 {
		from = 1
	}
	var start int64
	if int(from) <= len(j.offsets) {
		start = j.offsets[from-1]
	} else {
		start = j.size
	}
	j.mu.Unlock()
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return err
	}
	br := bufio.NewReaderSize(r, 1<<16)
	var pos = start
	for {
		j.mu.Lock()
		end, closed, changed := j.size, j.closed, j.changed
		j.mu.Unlock()
		for pos < end {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return err
			}
			pos += int64(len(line))
			var ev wire.Event
			if err := json.Unmarshal(line, &ev); err != nil {
				return err
			}
			if err := fn(ev); err != nil {
				return err
			}
		}
		if closed {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
