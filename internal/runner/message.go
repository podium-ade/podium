package runner

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

// MaxMessageBytes caps `text`. The node's line scanner allows 64 KiB per line
// (maxRunnerLine); half of that leaves room for the envelope, attachments and JSON
// escaping.
const MaxMessageBytes = 32 << 10

// MessageTypeFinal is what a message is when the task does not say otherwise: the answer.
const MessageTypeFinal = "final"

// AddMessage is `podium-runner message [--type progress|final] [--attach NAME]... TEXT`.
// TEXT of "-" reads the message from stdin.
//
// Like `artifact add` it is a separate invocation of the same binary rather than an API: a
// task says what it has to say from any shell, with no library and no credentials.
func AddMessage(cfg Config, args []string, stdin io.Reader) int {
	typ, text, attachments, err := parseMessageArgs(args, stdin)
	if err != nil {
		fmt.Fprintln(stderrOf(cfg), "podium-runner message:", err)
		return exitUsage
	}

	sock := cfg.EventsSock
	if sock == "" {
		sock = DefaultEventsSock
	}
	client := dialEvents(sock, cfg.dialTimeout())
	if client == nil {
		fmt.Fprintf(stderrOf(cfg), "podium-runner message: no node is listening on %s; "+
			"nothing will read this message\n", sock)
		return exitInternal
	}
	defer client.close()

	client.message(typ, text, attachments)
	return 0
}

// parseMessageArgs handles the flags by hand, as parseArtifactArgs does: the flag package
// would mangle a message that starts with a dash.
func parseMessageArgs(args []string, stdin io.Reader) (typ, text string, attachments []string, err error) {
	typ = MessageTypeFinal
	positional := ""
	seen := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--type" || a == "--attach":
			if i+1 >= len(args) {
				return "", "", nil, fmt.Errorf("%s needs a value", a)
			}
			i++
			if a == "--type" {
				typ = args[i]
			} else {
				attachments = append(attachments, args[i])
			}
		case strings.HasPrefix(a, "--type="):
			typ = strings.TrimPrefix(a, "--type=")
		case strings.HasPrefix(a, "--attach="):
			attachments = append(attachments, strings.TrimPrefix(a, "--attach="))
		case !seen:
			positional, seen = a, true
		default:
			return "", "", nil, fmt.Errorf("unexpected argument %q; the message is one argument, quote it", a)
		}
	}

	if typ == "" {
		return "", "", nil, fmt.Errorf("--type may not be empty; the canonical values are progress and final")
	}
	for _, name := range attachments {
		if strings.Contains(name, "/") {
			return "", "", nil, fmt.Errorf("attachments are artifact names, not paths")
		}
		if name == "" {
			return "", "", nil, fmt.Errorf("--attach may not be empty")
		}
	}
	if !seen {
		return "", "", nil, fmt.Errorf("usage: podium-runner message [--type TYPE] [--attach NAME]... TEXT")
	}

	if positional == "-" {
		body, rerr := io.ReadAll(stdin)
		if rerr != nil {
			return "", "", nil, fmt.Errorf("read the message from stdin: %w", rerr)
		}
		positional = string(body)
	}
	text = strings.TrimRightFunc(positional, unicode.IsSpace)
	if text == "" {
		return "", "", nil, fmt.Errorf("the message is empty; there is nothing to say")
	}
	// A refusal, not a truncation: half a message posted to a Slack thread is worse than a
	// message that failed loudly, and the caller is the one that knows how to split it.
	if len(text) > MaxMessageBytes {
		return "", "", nil, fmt.Errorf("message text is %d bytes; the limit is %d", len(text), MaxMessageBytes)
	}
	return typ, text, attachments, nil
}
