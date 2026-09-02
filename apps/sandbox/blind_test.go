package sandbox

// blind_test.go asserts the property the orchestrator CANNOT assert for itself:
// that a secret named by a caller does not leave this process, by either path.
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
	"time"
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

// BOTH PATHS. The result returned to the caller and the narration are separate
// paths out of this process, and a secret must not survive either.
func TestBlind_HidesBothEndpoints(t *testing.T) {
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

// A SECRET SPLIT ACROSS TWO FLUSHES IS STILL HIDDEN.
//
// This is the bypass that survives encoding-awareness: the stream is chopped by
// a CLOCK, so a program that writes half the key, waits past the flush interval,
// then writes the rest puts the two halves in different messages and a
// fixed-string replacement matches neither. `head -c 200 key; sleep 2; tail -c
// +201 key` is the whole attack.
func TestTell_ASecretSplitAcrossFlushesIsStillHidden(t *testing.T) {
	secret := strings.ReplaceAll(pem, "\n", "") // one long contiguous run of bytes
	b := newBlinder([]string{secret})
	if b.carry() != len(secret)-1 {
		t.Fatalf("carry = %d, want one less than the secret (%d)", b.carry(), len(secret))
	}

	tl := &tell{org: "acme", session: "s", blind: b}
	half := len(secret) / 2

	// First write, flushed on its own tick.
	tl.buf = append(tl.buf, secret[:half]...)
	first := tl.take(time.Now(), false)

	// Second write, a flush interval later — the rest of the secret.
	tl.buf = append(tl.buf, secret[half:]...)
	second := tl.take(time.Now().Add(2*tellEvery), false)

	// The last words, whatever is left.
	third := tl.take(time.Now().Add(4*tellEvery), true)

	whole := b.hide(first) + b.hide(second) + b.hide(third)
	if strings.Contains(whole, secret) {
		t.Fatalf("the split secret was reassembled in the clear:\n%q", whole)
	}
	// And not merely because it was dropped — the redaction must have fired.
	if !strings.Contains(whole, mark) {
		t.Fatalf("nothing was redacted; the secret may simply have been lost: %q", whole)
	}
}

// A forced flush keeps NOTHING back. done() is a watcher's last word, and a
// carry-over that swallowed it would trade a leak for a silence.
func TestTell_AForcedFlushHoldsNothingBack(t *testing.T) {
	b := newBlinder([]string{"a-secret-value-long-enough"})
	tl := &tell{org: "acme", session: "s", blind: b}
	tl.buf = append(tl.buf, "the tail end of the output"...)
	if got := tl.take(time.Now(), true); got != "the tail end of the output" {
		t.Fatalf("a forced flush dropped its last words: %q", got)
	}
	if len(tl.buf) != 0 {
		t.Fatalf("a forced flush left %d bytes behind", len(tl.buf))
	}
}

// With no secrets there is nothing to hold back, so ordinary output is not
// delayed by a byte.
func TestTell_NoSecretsMeansNoCarry(t *testing.T) {
	tl := &tell{org: "acme", session: "s", blind: newBlinder(nil)}
	tl.buf = append(tl.buf, "plain output"...)
	if got := tl.take(time.Now(), false); got != "plain output" {
		t.Fatalf("output was held back with no secrets registered: %q", got)
	}
}

// The WHOLE key is registered, not only its lines. A blanket skip of the "-----"
// armour dropped the whole-key form — which covers a `cat` (line by line) but
// not a single-line echo of the entire thing.
func TestBlind_RegistersTheWholeKeyNotOnlyItsLines(t *testing.T) {
	b := newBlinder([]string{pem})
	if got := b.hide("dumped: " + pem); strings.Contains(got, secretLines(t, pem)[0]) {
		t.Fatalf("the whole key survived: %q", got)
	}
	if b.carry() < len(pem)-1 {
		t.Fatalf("carry = %d; the whole key was never registered, so a split of it is not covered", b.carry())
	}
}

// A SECRET STRADDLING THE 8KiB TRUNCATION IS NOT EMITTED TAIL-FIRST.
//
// The buffer is front-truncated when a command outruns the flush interval. Cut
// before redacting, that cut discards the FRONT of a straddling secret and emits
// its tail in the clear — a suffix of a private key is still key material. Hidden
// first, every complete secret is already a marker, so the cut can only land in
// ordinary text or in one.
func TestTell_ASecretStraddlingTheTruncationIsNotEmitted(t *testing.T) {
	secret := strings.ReplaceAll(pem, "\n", "")
	b := newBlinder([]string{secret})
	tl := &tell{org: "acme", session: "s", blind: b}

	// Fill past the cap, then place the secret so it spans the truncation point:
	// its head is in the discarded region and its tail is in the kept one.
	head := strings.Repeat("x", tellCap-len(secret)/2)
	tl.buf = append(tl.buf, (head + secret + strings.Repeat("y", 512))...)

	out := tl.take(time.Now(), true) // forced, so nothing is held back

	tail := secret[len(secret)/2:]
	if strings.Contains(out, tail) {
		t.Fatalf("the tail of a truncated secret was emitted in the clear:\n%q", out)
	}
	if !strings.Contains(out, mark) {
		t.Fatalf("the secret was not redacted before the cut: %q", out)
	}
}
