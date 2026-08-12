package sandbox

// blind.go keeps a caller's secrets out of what a command publishes.
//
// # Why it is here and not in the caller
//
// A sandbox command leaves by TWO doors. The result goes back to the caller, and
// the narration goes STRAIGHT TO THE SESSION as the bytes are produced —
// durable events, an SSE feed, a chat thread — without passing through the
// caller at all. A caller that redacted the result it received would therefore
// have published the unredacted stream minutes earlier, and nothing downstream
// can take a secret back out of a message already delivered.
//
// So redaction happens where the bytes are PRODUCED. The caller says what must
// never appear (plane.RunIn.Blind); this applies it to both doors.
//
// # What it is not
//
// It is not a defence against a program that ENCODES a secret before printing
// it — base64, hex, one character per line. Nothing pattern-based could be. It
// is the guarantee that a secret this process was handed does not leave it in
// the form it was handed, which is what a `cat`, an `env` dump or a stack trace
// actually produces.

import (
	"encoding/json"
	"strings"
)

// minBlind is the shortest string worth hiding.
//
// A one- or two-character secret would match everywhere and turn every message
// into redaction marks, which destroys the output without protecting anything —
// and a secret that short is not a secret. Real credentials are far longer than
// this, so the bound costs nothing and stops a caller (or a bug) from blanking
// a session by asking to hide "a".
const minBlind = 8

// mark is what a hidden value is replaced with. Spelled the same way the
// orchestrator spells it, so a reader sees one word for one thing.
const mark = "[redacted]"

// blinder replaces a fixed set of secrets wherever they appear.
//
// The zero value and a nil *blinder are both usable and hide nothing, so a
// command with no secrets needs no branch at any call site.
type blinder struct {
	rep *strings.Replacer
}

// newBlinder builds a blinder for these secrets.
//
// Each secret is hidden BOTH whole and line by line. A PEM private key is the
// case that matters: it arrives as one multi-line string, and a program that
// prints it with its own indentation, or a log that re-wraps it, leaves a
// whole-string match finding nothing while every secret line is still there.
// The armour lines are skipped — they are the same public constant in every
// OpenSSH key, and hiding them only makes the output unreadable.
func newBlinder(secrets []string) *blinder {
	var pairs []string
	seen := map[string]bool{}
	add := func(s string) {
		if len(s) < minBlind || seen[s] || strings.HasPrefix(s, "-----") {
			return
		}
		seen[s] = true
		pairs = append(pairs, s, mark)
	}
	// Each secret is registered in every spelling it can LEAVE in, because the
	// event a watcher reads is JSON: a value carrying a quote, a backslash or a
	// newline is re-spelled by the encoder, so a replacement made only on the
	// plain form leaves the escaped one intact in the payload. The escaped
	// spelling of a multi-line key is also its whole self on ONE line, which is
	// the form a JSON log line actually carries.
	both := func(s string) {
		add(s)
		if q, err := json.Marshal(s); err == nil {
			add(strings.Trim(string(q), `"`))
		}
	}
	for _, s := range secrets {
		both(s)
		if strings.Contains(s, "\n") {
			for _, l := range strings.Split(s, "\n") {
				both(strings.TrimSpace(l))
			}
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	return &blinder{rep: strings.NewReplacer(pairs...)}
}

// hide returns s with every known secret replaced.
func (b *blinder) hide(s string) string {
	if b == nil || b.rep == nil || s == "" {
		return s
	}
	return b.rep.Replace(s)
}

// result returns r with both streams hidden — the second door.
func (b *blinder) result(r ExecResult) ExecResult {
	if b == nil {
		return r
	}
	r.Stdout, r.Stderr = b.hide(r.Stdout), b.hide(r.Stderr)
	return r
}
