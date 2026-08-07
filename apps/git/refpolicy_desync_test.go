package git

// Red's CRITICAL, pinned. The exploit is verbatim from the re-review, verified
// there against real git 2.43.
//
// The bypass needed no signature and no nonce. It needed only a payload that
// THIS parser and git read differently: git concatenates every push-certificate
// pkt-line and takes the commands between the first "\n\n" and the signature
// (builtin/receive-pack.c queue_commands_from_cert), while this parser treats
// one pkt-line as one logical line. Embed a "\n\n" in a header payload and the
// two part ways — we parse zero commands, git parses one and applies it.
//
// Zero commands was the whole exploit: checkRefPolicy over an empty slice
// refuses nothing, so a grant confined to one agent ref "satisfied" the policy,
// and the raw bytes went on to git receive-pack, which wrote refs/heads/main and
// fired the deploy reactor.
//
// Two guards close it and both are tested here, because either alone would do
// and defence in depth means neither is load-bearing on its own.

import (
	"strings"
	"testing"
)

// desyncPkt renders one pkt-line the way a client would. Named apart from the
// package's existing pkt helper so this file stands alone as the exploit's record.
func desyncPkt(payload string) string {
	n := len(payload) + 4
	const hex = "0123456789abcdef"
	return string([]byte{
		hex[(n>>12)&0xf], hex[(n>>8)&0xf], hex[(n>>4)&0xf], hex[n&0xf],
	}) + payload
}

// theExploit is Red's canonical 262-byte command section: a certificate whose
// `pusher` payload smuggles a blank line and a trunk-writing command.
func theExploit() []byte {
	return []byte(
		desyncPkt("push-cert\x00report-status\n") +
			desyncPkt("certificate version 0.1\n") +
			desyncPkt("pusher x\n\n0000000000000000000000000000000000000000 "+
				"1111111111111111111111111111111111111111 refs/heads/main\n") +
			desyncPkt("-----BEGIN PGP SIGNATURE-----\n") +
			desyncPkt("x\n") +
			desyncPkt("-----END PGP SIGNATURE-----\n") +
			desyncPkt("push-cert-end\n") +
			"0000")
}

// The framing guard: a payload that two parsers can read two ways is refused
// before either reading can matter.
func TestEmbeddedNewlineIsRefused(t *testing.T) {
	_, _, _, err := parseRefCommandsCaps(theExploit())
	if err == nil {
		t.Fatal("the push-cert desync exploit parsed without error — a grant can write refs/heads/main")
	}
	if !strings.Contains(err.Error(), "embedded newline") {
		t.Errorf("refused, but not as a framing defect: %v", err)
	}
}

// The policy guard, independently: even if a frame ever parses to nothing, a
// grant-bearing push that names no ref is a contradiction, not a no-op.
func TestAGrantMayNotWriteNothing(t *testing.T) {
	if err := checkRefPolicy(nil, "main", "refs/heads/agent/mine"); err == nil {
		t.Fatal("an empty command list satisfied a grant's confinement — " +
			"this is the shape that made the desync exploitable")
	}
}

// The sentence the whole coding path leans on, stated head-on: a run's grant is
// for its own branch, and refs/heads/main is not it.
//
// apps/coding pushes with this grant now (sandboxrunner.go), so this is no longer
// a claim about a credential nobody uses — it is the rule that stands between a
// prompt-injected model and the trunk. It is asserted for a CREATE, which is the
// friendliest thing a push can ask for and therefore the last shape anyone would
// think to refuse: main already exists, so a create against it would be refused
// anyway, and the point is that the GRANT refuses it first and would refuse it in
// a repository where main did not exist yet.
func TestAGrantMayNotWriteTheTrunk(t *testing.T) {
	for _, ref := range []string{"refs/heads/main", "refs/heads/master", "refs/heads/release/2.1", "refs/tags/v1"} {
		cmds := []refCommand{{
			Old: "0000000000000000000000000000000000000000",
			New: "1111111111111111111111111111111111111111",
			Ref: ref,
		}}
		if err := checkRefPolicy(cmds, "main", "refs/heads/agent/abc123def456"); err == nil {
			t.Fatalf("a grant confined to an agent branch wrote %s", ref)
		}
	}
	// The control that gives it meaning: its OWN ref still goes through, or the
	// rule is just an outage.
	own := []refCommand{{
		Old: "0000000000000000000000000000000000000000",
		New: "1111111111111111111111111111111111111111",
		Ref: "refs/heads/agent/abc123def456",
	}}
	if err := checkRefPolicy(own, "main", "refs/heads/agent/abc123def456"); err != nil {
		t.Fatalf("a run cannot write its own branch: %v", err)
	}
}

// The same emptiness stays harmless for a principal: an empty push is a no-op
// and always was. Refusing it would break ordinary clients for no gain.
func TestAPrincipalMayPushNothing(t *testing.T) {
	if err := checkRefPolicy(nil, "main", ""); err != nil {
		t.Errorf("an empty push by a principal must stay a no-op, got %v", err)
	}
}

// The negative control that gives the tests above their meaning: an ordinary
// single-line pkt-line still parses. If this ever fails, the guard is refusing
// legitimate traffic and the fix is worse than the bug.
func TestAnOrdinaryPushStillParses(t *testing.T) {
	body := []byte(desyncPkt("0000000000000000000000000000000000000000 "+
		"1111111111111111111111111111111111111111 refs/heads/agent/mine\x00report-status\n") + "0000")
	cmds, _, _, err := parseRefCommandsCaps(body)
	if err != nil {
		t.Fatalf("a plain single-line push must still parse: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Ref != "refs/heads/agent/mine" {
		t.Fatalf("parsed %d commands, want 1 naming the agent ref: %+v", len(cmds), cmds)
	}
}
