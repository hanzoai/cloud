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
	// longest is the length of the longest registered secret. It is what the
	// narrator holds back between flushes so a secret cannot be split across two
	// of them — see [blinder.carry].
	longest int
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
	longest := 0
	seen := map[string]bool{}
	add := func(s string) {
		if len(s) < minBlind || seen[s] {
			return
		}
		seen[s] = true
		pairs = append(pairs, s, mark)
		if len(s) > longest {
			longest = len(s)
		}
	}
	// A PEM key BEGINS with the armour, so a blanket skip of "-----" dropped the
	// whole-key registration entirely and left only the per-line forms — which
	// covers a `cat` but not a single-line echo of the whole thing. The armour is
	// skipped only where it IS the whole candidate: as a line of its own.
	addLine := func(s string) {
		if !strings.HasPrefix(s, "-----") {
			add(s)
		}
	}
	// Each secret is registered in every spelling it can LEAVE in, because the
	// event a watcher reads is JSON: a value carrying a quote, a backslash or a
	// newline is re-spelled by the encoder, so a replacement made only on the
	// plain form leaves the escaped one intact in the payload. The escaped
	// spelling of a multi-line key is also its whole self on ONE line, which is
	// the form a JSON log line actually carries.
	both := func(reg func(string), s string) {
		reg(s)
		if q, err := json.Marshal(s); err == nil {
			reg(strings.Trim(string(q), `"`))
		}
	}
	for _, s := range secrets {
		both(add, s)
		if strings.Contains(s, "\n") {
			for _, l := range strings.Split(s, "\n") {
				both(addLine, strings.TrimSpace(l))
			}
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	return &blinder{rep: strings.NewReplacer(pairs...), longest: longest}
}

// carry is how many trailing bytes a streaming caller must hold back between
// two flushes for this blinder to work at all.
//
// A fixed-string replacement only matches a CONTIGUOUS secret, and a stream is
// chopped by a timer rather than by content — so `head -c 200 key; sleep 2; tail
// -c +201 key` puts the two halves in different messages and neither half
// matches. Holding back one byte less than the longest secret guarantees that
// whatever straddled a boundary is whole in the next flush.
//
// Zero for a nil blinder: nothing is registered, so nothing can be split.
func (b *blinder) carry() int {
	if b == nil || b.longest <= 1 {
		return 0
	}
	return b.longest - 1
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
