package node

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"

	podiumv1 "github.com/alvaroibarguen/podium/internal/proto/podium/v1"
)

// MinRedactableSecret is the shortest value worth searching a log stream for. Below it the
// false positives cost more than the protection is worth: a two-byte secret would redact
// every occurrence of those two bytes in every line the task ever prints.
const MinRedactableSecret = 8

// maxRedactCarry bounds how many bytes of a chunk may be held back to catch a match that
// straddles a chunk boundary. It is the design's 256-byte tail.
const maxRedactCarry = 256

// redactor rewrites a task's log chunks so a secret's bytes never reach the server.
//
// This is defence in depth and nothing more. It is string matching: a task that base64s
// its own credential twice, gzips it, prints it one character per line, or leaks it
// through a length or a timing is not covered, and no redactor could cover it. The
// controls that actually matter are that a value is only ever sent to the node running the
// task that asked for it, that it lives in a tmpfs and in process memory, and that it is
// never written to the database. Redaction catches the common accident — a task echoing
// its own environment, a client library logging a URL with a password in it — and should
// never be relied on for more than that.
//
// A nil *redactor is a task with no secrets and passes everything through untouched.
type redactor struct {
	patterns []redactPattern
	// firstByte is a cheap filter: most positions in a log stream cannot start a match
	// and are rejected with one array lookup.
	firstByte [256]bool
	maxLen    int

	// carry holds the tail of each source's stream that could still be the beginning of a
	// match. Sources are the task's two streams plus one per sidecar, keyed by
	// stream+sidecar, and each carries independently because their bytes interleave.
	carry map[string][]byte
}

// redactPattern is one form of one secret and what replaces it.
type redactPattern struct {
	pattern []byte
	replace []byte
}

// newRedactor builds a matcher over every resolved secret's value and its common
// encodings. It returns nil when there is nothing worth matching, which is the fast path
// for the overwhelming majority of tasks.
func newRedactor(resolved []*podiumv1.ResolvedSecret) *redactor {
	var patterns []redactPattern
	seen := make(map[string]struct{})
	for _, s := range resolved {
		replace := []byte(fmt.Sprintf("[redacted:%s]", s.GetName()))
		for _, form := range secretForms(s.GetValue()) {
			if _, dup := seen[string(form)]; dup {
				continue
			}
			seen[string(form)] = struct{}{}
			patterns = append(patterns, redactPattern{pattern: form, replace: replace})
		}
	}
	if len(patterns) == 0 {
		return nil
	}
	// Longest first, so a value that is a prefix of another form never wins over it.
	sort.SliceStable(patterns, func(i, j int) bool {
		return len(patterns[i].pattern) > len(patterns[j].pattern)
	})

	r := &redactor{patterns: patterns, carry: make(map[string][]byte)}
	for _, p := range patterns {
		r.firstByte[p.pattern[0]] = true
		if len(p.pattern) > r.maxLen {
			r.maxLen = len(p.pattern)
		}
	}
	return r
}

// secretForms is the value plus the encodings a task is most likely to print it in. Only
// forms of at least MinRedactableSecret bytes are kept.
func secretForms(value []byte) [][]byte {
	if len(value) < MinRedactableSecret {
		return nil
	}
	candidates := [][]byte{
		bytes.Clone(value),
		[]byte(base64.StdEncoding.EncodeToString(value)),
		[]byte(base64.RawStdEncoding.EncodeToString(value)),
		[]byte(base64.URLEncoding.EncodeToString(value)),
		[]byte(base64.RawURLEncoding.EncodeToString(value)),
		[]byte(url.QueryEscape(string(value))),
		[]byte(url.PathEscape(string(value))),
	}
	out := make([][]byte, 0, len(candidates))
	for _, c := range candidates {
		if len(c) >= MinRedactableSecret {
			out = append(out, c)
		}
	}
	return out
}

// redact rewrites one run of output from one source and returns the bytes that are safe to
// emit now.
//
// It may hold some bytes back: the trailing run that is a proper prefix of some secret,
// capped at maxRedactCarry. Those bytes are emitted by the next call, or by flush when the
// source's output ends, so nothing is lost and a match that straddles two chunks is still
// caught. Output that does not look like the start of a secret — which is nearly all of it
// — is never delayed at all.
func (r *redactor) redact(stream, sidecar string, data []byte) []byte {
	if r == nil || len(data) == 0 {
		return data
	}
	key := stream + "\x00" + sidecar
	work := data
	if carried := r.carry[key]; len(carried) > 0 {
		work = append(bytes.Clone(carried), data...)
	}

	keep := r.prefixSuffix(work)
	if keep > 0 {
		r.carry[key] = bytes.Clone(work[len(work)-keep:])
	} else {
		delete(r.carry, key)
	}
	return r.replace(work[:len(work)-keep])
}

// flush drains a source's held-back tail. It is called when a source's run of output ends:
// the source changes, a non-log event arrives, or the task finishes.
func (r *redactor) flush(stream, sidecar string) []byte {
	if r == nil {
		return nil
	}
	key := stream + "\x00" + sidecar
	carried := r.carry[key]
	if len(carried) == 0 {
		return nil
	}
	delete(r.carry, key)
	return r.replace(carried)
}

// replace rewrites every occurrence of every pattern. Matching is leftmost, longest: at
// each position the longest pattern that matches wins, and scanning resumes after it.
func (r *redactor) replace(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	// Nothing to do is the common case; avoid allocating for it.
	if !r.mayMatch(data) {
		return data
	}
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if !r.firstByte[data[i]] {
			out = append(out, data[i])
			i++
			continue
		}
		matched := false
		for _, p := range r.patterns {
			if bytes.HasPrefix(data[i:], p.pattern) {
				out = append(out, p.replace...)
				i += len(p.pattern)
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, data[i])
			i++
		}
	}
	return out
}

// mayMatch reports whether any pattern's first byte occurs at all.
func (r *redactor) mayMatch(data []byte) bool {
	for _, b := range data {
		if r.firstByte[b] {
			return true
		}
	}
	return false
}

// prefixSuffix is the length of the longest suffix of data that is a *proper* prefix of
// some pattern — the bytes that might still turn into a match once more output arrives.
// A complete match is not a proper prefix, so a secret sitting at the very end of a chunk
// is redacted now rather than held.
func (r *redactor) prefixSuffix(data []byte) int {
	longest := min(r.maxLen-1, maxRedactCarry, len(data))
	for k := longest; k > 0; k-- {
		tail := data[len(data)-k:]
		for _, p := range r.patterns {
			if len(p.pattern) > k && bytes.HasPrefix(p.pattern, tail) {
				return k
			}
		}
	}
	return 0
}
