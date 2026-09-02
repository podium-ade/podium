// Package podiumv1 holds the generated podium.v1 wire types plus the hand-written log
// redaction helper below. `buf generate` only writes the files it produces, so it never
// touches this one — do not add `clean: true` to buf.gen.yaml without moving it first.
package podiumv1

import "google.golang.org/protobuf/proto"

// RedactForLog returns a copy of an Assign that is safe to hand to a log statement.
//
// In MVP-0 Assign carries no credentials, so this is a defensive clone and nothing more.
// It becomes load-bearing in step 09, when Assign gains resolved_secrets (and later
// registry_auths): those fields get cleared here and every log site already goes through
// this helper. Never log an Assign without it.
func RedactForLog(a *Assign) *Assign {
	if a == nil {
		return nil
	}
	c, ok := proto.Clone(a).(*Assign)
	if !ok {
		return nil
	}
	return c
}
