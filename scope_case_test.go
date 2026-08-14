package cloud

import "testing"

// TestScopeOwnsWhatTheRouterMatched pins the case fold, because the router and this
// gate must agree about which requests are a subsystem's own.
//
// Fiber matches case-insensitively, so every spelling below reaches the handler
// registered at /v1/exec. A gate that answered false for any of them would let the
// route run with the subsystem's middleware skipped — and for a subsystem whose
// middleware is a credential check, that is an unauthenticated call to a real
// handler. Each case is a request the ROUTER accepts, so each must be owned.
func TestScopeOwnsWhatTheRouterMatched(t *testing.T) {
	s := &scope{prefixes: []string{"/v1/exec"}}
	for _, path := range []string{
		"/v1/exec",
		"/V1/EXEC",       // the spelling that ran code with no credential
		"/V1/Exec/files", // mixed case, deeper in the subtree
		"/v1/exec/",      // the router treats a trailing slash as the same route
		"/v1/EXEC/upload",
	} {
		if !s.owns(path) {
			t.Errorf("owns(%q) = false; the router serves it, so the gate must cover it", path)
		}
	}
	// The fold must not widen the subtree: a sibling that merely shares a prefix
	// is a different subsystem, whatever its case.
	for _, path := range []string{"/v1/execute", "/V1/EXECUTOR", "/v1/ex"} {
		if s.owns(path) {
			t.Errorf("owns(%q) = true; that is another subsystem's surface", path)
		}
	}
}
