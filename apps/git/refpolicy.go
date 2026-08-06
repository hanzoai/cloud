package git

// refpolicy.go is the forge's answer to the question a coding agent forces:
// what, exactly, can a run that has been talked into betraying you actually do
// to a repository?
//
// A coding run executes UNTRUSTED MODEL OUTPUT against a real checkout while a
// credential with write access to the org sits one process away. The prompt is
// only half the attack surface — the model also reads the repo, and a README, a
// test fixture or a code comment can carry an instruction. Assume, therefore,
// that the run is hostile and that it has the credential. What stops it?
//
// Not a rule in the sandbox: the sandbox is the thing that is compromised. Not a
// rule in the orchestrator: a stolen credential does not go back through the
// orchestrator. The only place a rule cannot be stepped around is the point where
// refs are actually written — here, in the receiving forge — because every path
// that changes a ref passes through it and no client can decline to.
//
// So the policy is stated once, over the ref commands themselves, and it is
// PURE: a function of what the push is asking to do, with no state, no lookup
// and no identity to spoof.
//
//	refs/heads/agent/* is CREATE-ONLY.
//
// That single sentence carries the weight. The `agent/` namespace is the only
// place a run may write (coding.BranchFor derives the name from the session id,
// so the run does not choose it, and the model never sees the choice). Making it
// create-only means:
//
//   - A run cannot rewrite a branch that already exists. The bait-and-switch —
//     open a clean PR, let a human read it, force-push the payload before the
//     merge — is the signature attack on agentic PR review, and it is refused
//     here rather than mitigated by asking reviewers to be vigilant.
//   - A run cannot delete anything. Not its own branch, not anyone else's.
//   - Two runs cannot collide: the name is the session, and a second push to a
//     taken name is refused rather than silently winning.
//
// A DELETE of the default branch is refused for everyone, agent or not, because
// nothing that is a normal day's work needs it and it is unrecoverable from the
// pusher's side.
//
// # What this deliberately does NOT claim
//
// It does not confine the credential to the `agent/` namespace. A push carrying
// the org's agent credential can still address refs/heads/main, because the
// signal that would distinguish an agent push from a human one does not reach
// this handler today (the gateway consumes the Authorization header and what
// arrives is an org and a subject). Confinement by identity is the next control
// and it belongs exactly here, one condition wider than what is written below.
// Until it lands, the honest statement of the guarantee is: a coding run is
// confined to create-only agent refs BY CONSTRUCTION OF THE RUN, and the forge
// independently guarantees that whatever pushes there cannot rewrite or delete.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// agentRefPrefix is the machine namespace. It matches the branch coding derives
// from a session id (coding.BranchFor), spelled here rather than imported
// because git must not depend on the orchestrator to know its own rules — a
// forge that could only enforce a policy while another app was linked in would
// not be enforcing one.
const agentRefPrefix = "refs/heads/agent/"

// zeroOID is git's "this side does not exist": as the OLD id it means create, as
// the NEW id it means delete.
const zeroOID = "0000000000000000000000000000000000000000"

// refCommand is one ref update a push is asking for.
type refCommand struct {
	Old, New, Ref string
}

// Creates reports a ref that does not yet exist on our side.
func (c refCommand) Creates() bool { return isZero(c.Old) }

// Deletes reports a push asking for the ref to be removed.
func (c refCommand) Deletes() bool { return isZero(c.New) }

// isZero recognises the all-zero object id in either supported hash length,
// tolerating case. Length is checked too, so a short or overlong field cannot
// pass as absent.
func isZero(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	return strings.Trim(strings.ToLower(oid), "0") == ""
}

// checkRefPolicy is the whole rule, as a pure function of the commands. It
// returns the first refusal, or nil.
//
// defaultBranch is the repo's own HEAD branch (short name, e.g. "main"); an
// empty value simply means no branch gets the delete protection, which is a
// weaker answer but never a wrong one.
func checkRefPolicy(cmds []refCommand, defaultBranch string) error {
	def := ""
	if b := strings.TrimSpace(defaultBranch); b != "" {
		def = "refs/heads/" + b
	}
	for _, c := range cmds {
		switch {
		case strings.HasPrefix(c.Ref, agentRefPrefix):
			// CREATE-ONLY. An update and a delete are the same refusal because
			// they are the same capability: changing what a name already points at.
			if !c.Creates() {
				return fmt.Errorf("%s is an agent branch: it may be created and never rewritten or deleted", c.Ref)
			}
		case def != "" && c.Ref == def && c.Deletes():
			return fmt.Errorf("%s is the default branch and cannot be deleted by a push", c.Ref)
		}
	}
	return nil
}

// parseRefCommands reads the command list off the front of a receive-pack
// request body.
//
// The wire is pkt-line: a 4-hex length prefix covering itself, then the payload;
// "0000" is the flush that ends the command list and begins the PACK. The first
// command carries the client's capabilities after a NUL, which is dropped.
//
// It is deliberately STRICT and returns an error rather than a partial list. A
// body it cannot fully account for is a body whose commands it does not know,
// and a policy applied to commands you could not read is not a policy — so an
// unparsable push is refused rather than waved through. n reports how many bytes
// of body the command section occupied, which the caller needs because the
// remainder is the pack and must still reach git verbatim.
func parseRefCommands(body []byte) (cmds []refCommand, n int, err error) {
	c, n, _, err := parseRefCommandsCaps(body)
	return c, n, err
}

// parseRefCommandsCaps is the same read, also reporting the capability list the
// client sent on its first command. The refusal below has to know whether the
// client negotiated side-band-64k, because a report written in the wrong framing
// is a report the client never sees.
func parseRefCommandsCaps(body []byte) (cmds []refCommand, n int, caps string, err error) {
	const maxCommands = 1000 // a push updating more refs than this is not a coding run
	for {
		if n+4 > len(body) {
			return nil, 0, "", fmt.Errorf("truncated pkt-line header")
		}
		var size int
		if size, err = pktLen(body[n : n+4]); err != nil {
			return nil, 0, "", err
		}
		if size == 0 { // flush-pkt: the commands end here and the pack begins
			return cmds, n + 4, caps, nil
		}
		if size < 4 || n+size > len(body) {
			return nil, 0, "", fmt.Errorf("pkt-line length %d overruns the body", size)
		}
		line := body[n+4 : n+size]
		n += size
		if len(cmds) >= maxCommands {
			return nil, 0, "", fmt.Errorf("too many ref updates in one push")
		}
		if i := bytes.IndexByte(line, 0); i >= 0 { // capabilities ride the first command
			caps = string(bytes.TrimRight(line[i+1:], "\n"))
			line = line[:i]
		}
		c, perr := parseRefCommand(string(bytes.TrimRight(line, "\n")))
		if perr != nil {
			return nil, 0, "", perr
		}
		cmds = append(cmds, c)
	}
}

// refusalReport renders the policy's refusal as git's OWN report-status, so the
// person who typed the push reads the actual reason.
//
// This matters more than it looks. Refusing with an HTTP 403 does block the
// push, but what the client prints is "error: RPC failed; HTTP 403" — which is
// indistinguishable from a broken forge, a bad token or an outage. A control
// that cannot explain itself is a control an operator eventually switches off to
// "fix the git server". Git has a way to say no with a reason, so we use it:
//
//	! [remote rejected] agent/x -> agent/x (refs/heads/agent/x is an agent branch: ...)
//
// The push is refused ATOMICALLY — every command gets ng, not just the offending
// one — because a partially-applied push is a worse outcome than a refused one:
// the client would have to reason about which half landed.
//
// The framing follows what the CLIENT negotiated. A client that asked for
// side-band-64k reads the report off band 1 and hangs waiting for it otherwise,
// which is exactly the "unexpected disconnect while reading sideband packet"
// a plain body produces.
func refusalReport(cmds []refCommand, caps, reason string) []byte {
	var report bytes.Buffer
	report.Write(packetWrite("unpack ok\n"))
	for _, c := range cmds {
		report.Write(packetWrite("ng " + c.Ref + " " + reason + "\n"))
	}
	report.WriteString("0000")

	if !hasCap(caps, "side-band-64k") {
		return report.Bytes()
	}
	var out bytes.Buffer
	out.Write(packetWrite("\x01" + report.String())) // band 1: the report stream
	out.WriteString("0000")
	return out.Bytes()
}

// hasCap reports whether the client offered a capability. Exact token match on
// a space-separated list, so "side-band-64k" is not matched by a longer name
// that merely contains it.
func hasCap(caps, want string) bool {
	for _, f := range strings.Fields(caps) {
		if f == want || strings.HasPrefix(f, want+"=") {
			return true
		}
	}
	return false
}

// parseRefCommand splits "<old> <new> <ref>" and validates both ids are
// hexadecimal of a supported length. Validating them here means the policy above
// compares well-formed values and nothing downstream has to re-check.
func parseRefCommand(line string) (refCommand, error) {
	old, rest, ok := strings.Cut(line, " ")
	if !ok {
		return refCommand{}, fmt.Errorf("malformed ref command")
	}
	nw, ref, ok := strings.Cut(rest, " ")
	if !ok {
		return refCommand{}, fmt.Errorf("malformed ref command")
	}
	if !validOID(old) || !validOID(nw) {
		return refCommand{}, fmt.Errorf("malformed object id in ref command")
	}
	if ref == "" || !strings.HasPrefix(ref, "refs/") || strings.Contains(ref, "..") {
		return refCommand{}, fmt.Errorf("malformed ref name")
	}
	return refCommand{Old: old, New: nw, Ref: ref}, nil
}

func validOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// pktLen decodes a 4-byte hex pkt-line length. It rejects anything that is not
// exactly four hex digits, so a body that is not pkt-line at all fails here
// rather than being interpreted as a very large or very small frame.
func pktLen(b []byte) (int, error) {
	for _, c := range b {
		if !isHexDigit(c) {
			return 0, fmt.Errorf("pkt-line length is not hexadecimal")
		}
	}
	v, err := strconv.ParseInt(string(b), 16, 32)
	if err != nil {
		return 0, fmt.Errorf("pkt-line length is not hexadecimal")
	}
	return int(v), nil
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
