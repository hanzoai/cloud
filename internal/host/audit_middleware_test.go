package host

// Integration tests for the audit middleware. They drive REAL requests through
// the zip/fiber stack (app.Fiber().Test) with the audit middleware in front of a
// handler, backed by a REAL on-disk audit store, then read the store back to
// assert what was (and was not) recorded. No mocks — the whole capture path runs.
//
// The middleware trusts SanitizeIdentity to have already validated identity, so
// these tests set the sanitized X-User-* headers directly (as SanitizeIdentity
// would after verifying a JWT) — that is the contract boundary under test here.
// The forgery/bypass properties of SanitizeIdentity itself are proven in
// middleware_identity_test.go; here we prove the middleware records the VALIDATED
// identity and the correct outcome for every security-relevant request.

import (
	"github.com/hanzoai/cloud/sdk/audit"
)

// asAdmin sets the sanitized identity headers a VALIDATED SuperAdmin would
// carry after SanitizeIdentity (X-User-IsAdmin=true, org=admin).
// outcomes is the outcome records among rows. Every change is also on the trail as
// begun, under the same request id, before it ran.
func outcomes(rows []audit.Record) []audit.Record {
	var out []audit.Record
	for _, r := range rows {
		if r.Outcome.Result != "begun" {
			out = append(out, r)
		}
	}
	return out
}
