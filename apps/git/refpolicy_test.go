package git

// These are the tests for the one rule that holds when everything else has
// failed: the run is prompt-injected, the model is hostile, the credential is in
// its hands. What can it still do to the repository?
//
// Each case below is written as the attack it refuses, not as the branch it
// covers, because the value of this file is the list of attacks — a future
// change that keeps the coverage and loses an attack has lost the point.

import (
	"fmt"
	"strings"
	"testing"
)

const (
	oidA = "1111111111111111111111111111111111111111"
	oidB = "2222222222222222222222222222222222222222"
)

// pkt frames one pkt-line the way git does: 4 hex digits of total length,
// covering themselves, then the payload.
func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// body builds a receive-pack request: the command list, the flush, then some
// bytes standing in for the PACK.
func body(lines ...string) []byte {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(pkt(l))
	}
	b.WriteString("0000")
	b.WriteString("PACK-and-then-some-object-data")
	return []byte(b.String())
}

// THE ATTACK: open a clean PR, let a human read it, then force-push the payload
// onto the same branch before the merge lands. The reviewer approved one diff
// and merged another. This is the signature attack on agentic PR review and it
// is the reason agent refs are create-only.
func TestAnAgentBranchCannotBeRewrittenUnderAReviewer(t *testing.T) {
	cmds, _, err := parseRefCommands(body(oidA + " " + oidB + " refs/heads/agent/abc123"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := checkRefPolicy(cmds, "main", ""); err == nil {
		t.Fatal("a force-push over an existing agent branch was allowed; a reviewer can be shown one diff and merged another")
	}
}

// THE ATTACK: erase the evidence, or erase someone else's work in flight.
func TestAnAgentBranchCannotBeDeleted(t *testing.T) {
	cmds, _, err := parseRefCommands(body(oidA + " " + zeroOID + " refs/heads/agent/abc123"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := checkRefPolicy(cmds, "main", ""); err == nil {
		t.Fatal("an agent branch delete was allowed")
	}
}

// THE ATTACK: destroy the repository's trunk.
func TestTheDefaultBranchCannotBeDeleted(t *testing.T) {
	cmds, _, err := parseRefCommands(body(oidA + " " + zeroOID + " refs/heads/main"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := checkRefPolicy(cmds, "main", ""); err == nil {
		t.Fatal("the default branch was deletable by a push")
	}
}

// The rule must not break the thing it exists to permit: a run creating its own
// fresh branch, which is every legitimate coding push.
func TestARunMayCreateItsOwnBranch(t *testing.T) {
	cmds, n, err := parseRefCommands(body(zeroOID + " " + oidB + " refs/heads/agent/abc123"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := checkRefPolicy(cmds, "main", ""); err != nil {
		t.Fatalf("a run could not create its own branch: %v", err)
	}
	// The pack must still be reachable behind the commands, byte for byte —
	// the policy reads the body, it does not consume it.
	rest := string(body(zeroOID + " " + oidB + " refs/heads/agent/abc123")[n:])
	if !strings.HasPrefix(rest, "PACK") {
		t.Fatalf("the command section ended in the wrong place; the pack would be corrupted: %q", rest)
	}
}

// An ordinary human push to an ordinary branch is untouched. A security control
// that stops normal work gets switched off, so this is a load-bearing case.
func TestOrdinaryPushesAreUntouched(t *testing.T) {
	for _, line := range []string{
		oidA + " " + oidB + " refs/heads/main",             // update trunk
		zeroOID + " " + oidB + " refs/heads/feature/thing", // new feature branch
		oidA + " " + zeroOID + " refs/heads/feature/thing", // delete a feature branch
		zeroOID + " " + oidB + " refs/tags/v1.2.3",         // tag
	} {
		cmds, _, err := parseRefCommands(body(line))
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		if err := checkRefPolicy(cmds, "main", ""); err != nil {
			t.Errorf("ordinary push %q was refused: %v", line, err)
		}
	}
}

// THE ATTACK: hide a forbidden command behind a permitted one. Every command in
// the push is checked, not just the first.
func TestAForbiddenCommandCannotHideBehindAPermittedOne(t *testing.T) {
	cmds, _, err := parseRefCommands(body(
		zeroOID+" "+oidB+" refs/heads/agent/fresh",
		oidA+" "+oidB+" refs/heads/agent/existing", // the payload
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := checkRefPolicy(cmds, "main", ""); err == nil {
		t.Fatal("a rewrite smuggled in behind a legitimate create was allowed")
	}
}

// The first command carries the client's capability list after a NUL. If that
// were not stripped, the ref name would carry trailing junk, the prefix test
// would still match but a future exact comparison would not — and worse, a
// parser that failed here would refuse every real push.
func TestCapabilitiesOnTheFirstCommandAreStripped(t *testing.T) {
	cmds, _, err := parseRefCommands(body(
		zeroOID + " " + oidB + " refs/heads/agent/abc123\x00report-status side-band-64k agent=git/2.39",
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Ref != "refs/heads/agent/abc123" {
		t.Fatalf("capabilities leaked into the ref name: %+v", cmds)
	}
}

// A body the policy cannot fully read must be REFUSED, never waved through: a
// rule applied to commands you could not parse is not a rule. These are the
// shapes an attacker would try in order to be unreadable on purpose.
func TestAnUnreadablePushIsRefusedRatherThanIgnored(t *testing.T) {
	cases := map[string][]byte{
		"not pkt-line at all":     []byte("hello there"),
		"length overruns body":    []byte("00ff" + oidA),
		"header truncated":        []byte("00"),
		"no flush before the end": []byte(pkt(zeroOID + " " + oidB + " refs/heads/agent/x")),
		"malformed object id":     body("zzzz " + oidB + " refs/heads/agent/x"),
		"ref escaping the tree":   body(zeroOID + " " + oidB + " refs/heads/../../etc/x"),
		"ref outside refs/":       body(zeroOID + " " + oidB + " HEAD"),
	}
	for name, raw := range cases {
		if _, _, err := parseRefCommands(raw); err == nil {
			t.Errorf("%s: parsed without error; a push we cannot read would reach git unchecked", name)
		}
	}
}

// The all-zero id means "absent", and it must be recognised at both hash
// lengths and in either case — a delete spelled in capitals is still a delete.
func TestAbsenceIsRecognisedAtEveryHashLength(t *testing.T) {
	for _, z := range []string{
		strings.Repeat("0", 40),
		strings.Repeat("0", 64),
		strings.Repeat("0", 40),
	} {
		if !isZero(z) {
			t.Errorf("%q was not recognised as absent", z)
		}
	}
	for _, notZero := range []string{"", "0", strings.Repeat("0", 39), strings.Repeat("0", 41), oidA} {
		if isZero(notZero) {
			t.Errorf("%q was wrongly read as absent; a delete could be smuggled or a create refused", notZero)
		}
	}
}

// A repo whose HEAD could not be read still gets the agent rules. Withholding
// one protection is the safe failure; refusing every push to that repo is not.
func TestAnUnknownDefaultBranchStillProtectsAgentRefs(t *testing.T) {
	cmds, _, err := parseRefCommands(body(oidA + " " + oidB + " refs/heads/agent/abc123"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := checkRefPolicy(cmds, "", ""); err == nil {
		t.Fatal("agent refs lost their protection when the default branch was unknown")
	}
}
