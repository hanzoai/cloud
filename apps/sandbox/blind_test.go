package sandbox

// blind_test.go asserts the property the orchestrator CANNOT assert for itself:
// that a secret named by a caller does not leave this process, by either door.
//
// The bug it pins is not hypothetical. A run's credential is a file in the
// sandbox, so "cat the key" is one sentence in a README away, and the bytes a
// command produces are narrated into the session AS THEY ARE WRITTEN — durable
// events, an SSE feed, a chat thread. A scrub at the caller runs after all of
// that has already been delivered.

import (
	"encoding/json"
	"strings"
	"testing"
)

const pem = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtz
c2gtZWQyNTUxOQAAACBTRUNSRVRLRVlNQVRFUklBTFNIT1VMRE5PVExFQUsxMjM0
-----END OPENSSH PRIVATE KEY-----`

func secretLines(t *testing.T, s string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); len(l) >= minBlind && !strings.HasPrefix(l, "-----") {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		t.Fatal("the fixture has no secret-bearing lines")
	}
	return out
}

// A KEY PRINTED BY A COMMAND DOES NOT REACH THE SESSION — whole, or one line at
// a time, which is how `cat` actually produces it.
func TestBlind_HidesAKeyWholeAndLineByLine(t *testing.T) {
	b := newBlinder([]string{pem})
	for _, line := range secretLines(t, pem) {
		if got := b.hide("read: " + line); strings.Contains(got, line) {
			t.Fatalf("a key LINE survived: %q", got)
		}
	}
	if got := b.hide(pem); strings.Contains(got, secretLines(t, pem)[0]) {
		t.Fatalf("the whole key survived: %q", got)
	}
	if !strings.Contains(b.hide(pem), mark) {
		t.Fatal("nothing was marked as redacted")
	}
}

// The gateway token is the DEPLOYMENT's identity, injected as HANZO_API_KEY, so
// an `env` dump publishes it. It is blinded the same way.
func TestBlind_HidesTheGatewayToken(t *testing.T) {
	const tok = "hk-DEPLOYMENTGATEWAYTOKENVALUE"
	b := newBlinder([]string{pem, tok})
	got := b.hide("HANZO_API_KEY=" + tok + "\nPATH=/usr/bin")
	if strings.Contains(got, tok) {
		t.Fatalf("the gateway token survived an env dump: %q", got)
	}
	if !strings.Contains(got, "PATH=/usr/bin") {
		t.Fatalf("redaction ate the rest of the output: %q", got)
	}
}

// BOTH DOORS. The result returned to the caller and the narration are separate
// paths out of this process, and a secret must not survive either.
func TestBlind_HidesBothDoors(t *testing.T) {
	b := newBlinder([]string{pem})
	line := secretLines(t, pem)[0]
	r := b.result(ExecResult{Stdout: "key is " + line, Stderr: "also " + line})
	if strings.Contains(r.Stdout, line) || strings.Contains(r.Stderr, line) {
		t.Fatalf("a secret survived the result: %+v", r)
	}
}

// A secret whose JSON ENCODING differs from its plain form is still hidden. A
// key containing a quote or a backslash is re-spelled by the encoder, so a
// replacement made only on the plain form would leave the escaped one intact —
// and the event payload is JSON.
func TestBlind_SurvivesJSONEscaping(t *testing.T) {
	const tricky = `secret"with\backslash-and-quote-1234`
	b := newBlinder([]string{tricky})
	payload, err := json.Marshal(line{Message: "printed " + tricky})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// This is exactly what tell.say does: hide the field, then hide the encoded
	// form as well.
	if hidden := b.hide(string(payload)); strings.Contains(hidden, `backslash-and-quote-1234`) {
		t.Fatalf("the escaped spelling survived: %q", hidden)
	}
}

// A blinder REFUSES to hide something too short to be a secret. Hiding "a"
// would blank every message without protecting anything.
func TestBlind_IgnoresValuesTooShortToBeSecrets(t *testing.T) {
	if b := newBlinder([]string{"a", "ab", ""}); b != nil {
		t.Fatalf("a one-character secret built a blinder: %q", b.hide("a quick brown fox"))
	}
	// And no secrets at all is a nil blinder whose methods are still safe.
	var none *blinder
	if got := none.hide("untouched"); got != "untouched" {
		t.Fatalf("nil blinder changed the output: %q", got)
	}
}
