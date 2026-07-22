package link

// carrier.go passes a resolved credential from the router to an account-aware
// upstream WITHOUT ever serializing it. The credential rides the request context as
// a PROCESS-LOCAL value — never a header, body, argv, error, or log — so a
// co-resident provider egress (the in-process ai path) reads it at the instant it
// dials the provider and discards it immediately after. Crossing a process boundary
// is deliberately out of scope: the value lives only in this process's memory, which
// is exactly the "credentials never leave KMS→memory" invariant made mechanical.

import "context"

type ctxKey int

const (
	credKey ctxKey = iota
	acctKey
)

// WithCredential returns a context carrying cred for an account-aware upstream to
// read at egress. Process-local; never serialized.
func WithCredential(ctx context.Context, cred Credential) context.Context {
	return context.WithValue(ctx, credKey, cred)
}

// CredentialFrom returns the routed credential the router attached, if any. The
// provider egress calls this to authenticate its upstream call with the caller's own
// account, then discards the value. Absent ⟹ the request was not account-routed
// (the platform's own path), so the egress uses its normal credential.
func CredentialFrom(ctx context.Context) (Credential, bool) {
	c, ok := ctx.Value(credKey).(Credential)
	return c, ok
}

// WithAccount carries the NON-SECRET account identity beside the credential, so the
// egress can attribute/audit by account without a second parameter.
func WithAccount(ctx context.Context, a Account) context.Context {
	return context.WithValue(ctx, acctKey, a)
}

// AccountFrom returns the routed account identity, if any.
func AccountFrom(ctx context.Context) (Account, bool) {
	a, ok := ctx.Value(acctKey).(Account)
	return a, ok
}
