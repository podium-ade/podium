// Package podiumv1 holds the generated podium.v1 wire types plus the hand-written log
// redaction helper below. `buf generate` only writes the files it produces, so it never
// touches this one — do not add `clean: true` to buf.gen.yaml without moving it first.
package podiumv1

import "google.golang.org/protobuf/proto"

// RedactForLog returns a copy of an Assign that is safe to hand to a log statement.
//
// Assign.resolved_secrets carries plaintext secret values and Assign.registry_credentials
// carries registry passwords, so this is the only form of an Assign that may ever reach a
// logger. The clone keeps the task ID, the lease, the spec
// and the deadline — everything an operator needs to follow an assignment — and drops the
// values. The names are dropped with them: a secret name is not a value, but the spec's
// own SecretRefs already carry the names, so keeping a second copy here buys nothing.
//
// Never log an Assign without it. internal/server's TestNoAssignIsLoggedUnredacted is the
// lint rule that enforces this across the tree.
func RedactForLog(a *Assign) *Assign {
	if a == nil {
		return nil
	}
	c, ok := proto.Clone(a).(*Assign)
	if !ok {
		return nil
	}
	c.ResolvedSecrets = nil
	c.RegistryCredentials = nil
	return c
}
