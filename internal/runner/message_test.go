package runner

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// messageSocket is the node's half of the socket for one `message` invocation: it accepts
// one connection and hands back the first line written to it.
func messageSocket(t *testing.T) (sock string, lines <-chan string) {
	t.Helper()
	dir := shortTempDir(t)
	sock = filepath.Join(dir, "events.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	out := make(chan string, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 0, 4096), 64*1024)
		if sc.Scan() {
			out <- sc.Text()
		}
	}()
	return sock, out
}

type wireMessage struct {
	V           int      `json:"v"`
	Kind        string   `json:"kind"`
	TS          string   `json:"ts"`
	Type        string   `json:"type"`
	Text        string   `json:"text"`
	Attachments []string `json:"attachments"`
}

func awaitMessage(t *testing.T, lines <-chan string) wireMessage {
	t.Helper()
	select {
	case line := <-lines:
		var ev wireMessage
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("undecodable event %q: %v", line, err)
		}
		if ev.V != protocolVersion || ev.Kind != kindMessage {
			t.Fatalf("envelope = v%d %q", ev.V, ev.Kind)
		}
		if ev.TS == "" {
			t.Error("the envelope carries no timestamp")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no message event reached the socket")
		return wireMessage{}
	}
}

func devnull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestAddMessageDefaultsToFinal: a message with no --type is the turn's answer.
func TestAddMessageDefaultsToFinal(t *testing.T) {
	sock, lines := messageSocket(t)
	cfg := Config{EventsSock: sock, DialTimeout: time.Second}

	if code := AddMessage(cfg, []string{"all done  \n"}, nil); code != 0 {
		t.Fatalf("AddMessage exited %d", code)
	}
	ev := awaitMessage(t, lines)
	if ev.Type != MessageTypeFinal {
		t.Errorf("type = %q, want %q", ev.Type, MessageTypeFinal)
	}
	if ev.Text != "all done" {
		t.Errorf("text = %q; trailing whitespace must be trimmed", ev.Text)
	}
	if len(ev.Attachments) != 0 {
		t.Errorf("attachments = %v, want none", ev.Attachments)
	}
}

// TestAddMessageCarriesTypeAndAttachments covers both spellings of every flag, and that a
// type outside the canonical pair is accepted: the set is open on purpose.
func TestAddMessageCarriesTypeAndAttachments(t *testing.T) {
	for name, args := range map[string][]string{
		"separate": {"--type", "review", "--attach", "shot-1.png", "--attach", "shot-2.png", "the answer"},
		"equals":   {"--type=review", "--attach=shot-1.png", "--attach=shot-2.png", "the answer"},
	} {
		t.Run(name, func(t *testing.T) {
			sock, lines := messageSocket(t)
			if code := AddMessage(Config{EventsSock: sock, DialTimeout: time.Second}, args, nil); code != 0 {
				t.Fatalf("AddMessage exited %d", code)
			}
			ev := awaitMessage(t, lines)
			if ev.Type != "review" || ev.Text != "the answer" {
				t.Errorf("event = %+v", ev)
			}
			if len(ev.Attachments) != 2 || ev.Attachments[0] != "shot-1.png" || ev.Attachments[1] != "shot-2.png" {
				t.Errorf("attachments = %v", ev.Attachments)
			}
		})
	}
}

// TestAddMessageReadsStdin: `-` is how a task sends more than a shell can comfortably quote.
func TestAddMessageReadsStdin(t *testing.T) {
	sock, lines := messageSocket(t)
	body := "line one\nline two\n"

	code := AddMessage(Config{EventsSock: sock, DialTimeout: time.Second},
		[]string{"--type", "progress", "-"}, strings.NewReader(body))
	if code != 0 {
		t.Fatalf("AddMessage exited %d", code)
	}
	ev := awaitMessage(t, lines)
	if ev.Type != "progress" || ev.Text != "line one\nline two" {
		t.Errorf("event = %+v", ev)
	}
}

// TestAddMessageRefusesOver32KiB: exactly at the cap passes, one byte over fails. The
// runner refuses rather than truncating — half a message posted somewhere is worse than a
// message that failed loudly.
func TestAddMessageRefusesOver32KiB(t *testing.T) {
	sock, lines := messageSocket(t)
	cfg := Config{EventsSock: sock, DialTimeout: time.Second, Stderr: devnull(t)}

	atCap := strings.Repeat("x", MaxMessageBytes)
	if code := AddMessage(cfg, []string{atCap}, nil); code != 0 {
		t.Fatalf("a %d-byte message exited %d, want 0", len(atCap), code)
	}
	if ev := awaitMessage(t, lines); len(ev.Text) != MaxMessageBytes {
		t.Fatalf("text is %d bytes, want %d", len(ev.Text), MaxMessageBytes)
	}

	_, _, _, err := parseMessageArgs([]string{strings.Repeat("x", MaxMessageBytes+1)}, nil)
	if err == nil {
		t.Fatal("a message one byte over the cap was accepted")
	}
	want := "message text is 32769 bytes; the limit is 32768"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
	if code := AddMessage(cfg, []string{strings.Repeat("x", MaxMessageBytes+1)}, nil); code != exitUsage {
		t.Errorf("exited %d, want %d", code, exitUsage)
	}
}

// TestAddMessageRejectsPathAttachments: an attachment is an artifact name, and the two are
// easy to confuse from inside a container that has the file on disk.
func TestAddMessageRejectsPathAttachments(t *testing.T) {
	cfg := Config{EventsSock: filepath.Join(shortTempDir(t), "events.sock"), DialTimeout: time.Millisecond, Stderr: devnull(t)}
	if code := AddMessage(cfg, []string{"--attach", "dir/a.png", "x"}, nil); code != exitUsage {
		t.Errorf("exited %d, want %d", code, exitUsage)
	}
	_, _, _, err := parseMessageArgs([]string{"--attach", "dir/a.png", "x"}, nil)
	if err == nil || err.Error() != "attachments are artifact names, not paths" {
		t.Errorf("error = %v", err)
	}
}

// TestAddMessageRequiresText: an empty final would post an empty message somewhere.
func TestAddMessageRequiresText(t *testing.T) {
	cfg := Config{EventsSock: filepath.Join(shortTempDir(t), "events.sock"), DialTimeout: time.Millisecond, Stderr: devnull(t)}
	for name, args := range map[string][]string{
		"no positional":  {"--type", "final"},
		"empty text":     {""},
		"blank text":     {"   \n"},
		"empty type":     {"--type=", "hi"},
		"dangling flag":  {"hi", "--attach"},
		"two positional": {"hello", "world"},
	} {
		t.Run(name, func(t *testing.T) {
			if code := AddMessage(cfg, args, nil); code != exitUsage {
				t.Errorf("exited %d, want %d", code, exitUsage)
			}
		})
	}
	if code := AddMessage(cfg, []string{"-"}, strings.NewReader("\n\n")); code != exitUsage {
		t.Errorf("blank stdin exited %d, want %d", code, exitUsage)
	}
}

// TestAddMessageNoSocketIsInternalError: nothing is going to read the message, and saying
// so with a non-zero status beats pretending it was delivered.
func TestAddMessageNoSocketIsInternalError(t *testing.T) {
	cfg := Config{
		EventsSock:  filepath.Join(shortTempDir(t), "events.sock"),
		DialTimeout: 10 * time.Millisecond,
		Stderr:      devnull(t),
	}
	if code := AddMessage(cfg, []string{"anybody there?"}, nil); code != exitInternal {
		t.Errorf("exited %d, want %d", code, exitInternal)
	}
}
