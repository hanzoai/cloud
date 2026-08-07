package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRedact_StripsCredentialInAURL covers the credential the key denylist cannot
// see: one embedded in a URL VALUE, under a key whose name says nothing.
//
// This is the shape apps/coding builds to clone a repo, and it is the exact
// argument an agent's tool call carries into a trace. Recording it verbatim would
// put a live token in the span store, where it outlives the sandbox that used it.
func TestRedact_StripsCredentialInAURL(t *testing.T) {
	in := json.RawMessage(`{
      "cloneUrl": "https://x-access-token:ghp_LIVE_TOKEN_VALUE@github.com/acme/repo",
      "mirror":   "https://ghp_BARE_TOKEN@github.com/acme/repo",
      "endpoint": "https://api.example.com/v1/things",
      "note":     "cloned from https://user:hunter2@git.example.com/x",
      "nested":   {"repo": {"url": "git+ssh://deploy:s3cr3t@git.example.com/y"}},
      "list":     ["https://u:p4ss@h.example.com/z"]
    }`)
	out := Redact(in)
	s := string(out)

	for _, leak := range []string{"ghp_LIVE_TOKEN_VALUE", "ghp_BARE_TOKEN", "hunter2", "s3cr3t", "p4ss"} {
		if strings.Contains(s, leak) {
			t.Fatalf("credential %q survived redaction: %s", leak, s)
		}
	}
	// The USER half is kept — it names how the call authenticated and is not the
	// secret — and so is every URL that carried no credential at all.
	for _, keep := range []string{"x-access-token", "github.com/acme/repo", "https://api.example.com/v1/things", "deploy"} {
		if !strings.Contains(s, keep) {
			t.Fatalf("redaction dropped non-secret detail %q: %s", keep, s)
		}
	}
	if !strings.Contains(s, redactedMarker) {
		t.Fatalf("no redaction marker in output: %s", s)
	}
}

// TestStripURLCredential_LeavesOrdinaryTextAlone: the rule is structural, so text
// that merely contains an "@" is not a credential and must survive unchanged.
func TestStripURLCredential_LeavesOrdinaryTextAlone(t *testing.T) {
	for _, s := range []string{
		"mail z@hanzo.ai about it",
		"https://api.example.com/v1/things",
		"@hanzo what is the status",
		"",
		"user@host without a scheme",
	} {
		if got := stripURLCredential(s); got != s {
			t.Fatalf("stripURLCredential(%q) = %q, want it unchanged", s, got)
		}
	}
}
