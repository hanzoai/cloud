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
// orchestrator. It has to be in the receiving forge, at the point refs are
// actually written.
//
// # The claim this file used to make, and why it was false
//
// It said receive-pack was "the point every path that changes a ref passes
// through and no client can decline to". That was wrong, and the error was not a
// detail — it was the whole argument. This function guarded ONE of eight ref
// writers into the same bare repository. The other seven took a credential the
// run already held: POST /repos/:name/push writes a branch from a JSON body,
// /repos/:name/mirror force-overwrites every ref and deletes any the source
// lacks, SSH receive-pack ran the same git binary with no policy in front of it,
// and the import path fast-forwards a named ref from an upstream. A run refused
// at the front door walked in through any of them.
//
// The bait-and-switch that motivated this file was therefore still live, and in
// a nastier form than a force-push: a FAST-FORWARD CHILD posted onto the branch
// a human had just approved, which trips no "the branch was rewritten" signal
// anywhere because it is append-only.
//
// Two things fixed it, and they are different fixes to different problems:
//
//  1. The credential stopped being an identity (grant.go). A run now holds a
//     grant — a bounded permission to create ONE ref in ONE repository, which
//     resolves to no principal at all — so every one of those doors but the wire
//     receive-pack is shut to it by the ordinary authorization it already had,
//     not by a new check. That is also why the merge door (writer 9) needs no
//     rule of its own about runs: it reads its org from the validated principal,
//     and a grant is not one.
//  2. This policy moved to ALL of the writers, not one (see refwriters.go for
//     the enumeration and where each states its intent).
//
// # The policy
//
// Stated once, over the ref commands themselves, and PURE: a function of what is
// being asked, with no state, no lookup and no identity to spoof.
//
//	refs/heads/agent/* is CREATE-ONLY, and a grant may write only its own ref.
//
// The `agent/` namespace is the only place a run may write (coding.BranchFor
// derives the name from the session id, so the run does not choose it and the
// model never sees the choice). Making it create-only means:
//
//   - A run cannot rewrite a branch that already exists — nor fast-forward it,
//     which is the same capability wearing a friendlier hat: changing what a
//     name already points at.
//   - A run cannot delete anything. Not its own branch, not anyone else's.
//   - Two runs cannot collide: the name is the session, and a second push to a
//     taken name is refused rather than silently winning.
//
// A DELETE of the default branch is refused for everyone, agent or not, because
// nothing that is a normal day's work needs it and it is unrecoverable from the
// pusher's side.
//
// grantedRef is the second sentence, and it is about the BEARER rather than the
// namespace: a caller holding a grant may write the one ref that grant names and
// nothing else, so a run cannot reach another run's branch even though both live
// in a create-only namespace. A caller with a real principal passes "" and is
// bound by the namespace rules alone — a human fixing their own repository is
// not the threat this file is about.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// # Every writer, and where each states its intent
//
// The rule is only as good as the count of doors it stands in. There are nine
// ways a ref in one of our bare repositories can change, and they are:
//
//  1. HTTP receive-pack      smart_http.go receivePack — parses the commands off
//     the wire and calls checkRefPolicy.
//  2. SSH receive-pack       pack.go sshReceivePack — reads the same command
//     section off the channel (gitexec.go runPackSSHScreened) and calls the same
//     function. Was unguarded.
//  3. Client-less push       push.go corePush — states its one command as a
//     refCommand and calls the same function. Was unguarded.
//  4. Mirror-in fetch        mirror.go mirrorInto — cannot be judged
//     command-by-command (the commands are whatever the remote advertises), so
//     it is refused STRUCTURALLY: mirrorExcludeAgent removes the machine
//     namespace from the refmap, and therefore from --prune. Was unguarded.
//  5. Mirror-in HEAD         mirror.go mirrorInto's symbolic-ref — checkHeadRef.
//     Was unguarded.
//  6. Import tags            github_import.go fetchTags — refs/tags/*:refs/tags/*
//     is non-forcing and cannot name refs/heads at all, so it can neither reach
//     the machine namespace nor delete anything. Structurally safe already;
//     stated here so the next reader does not have to re-derive it.
//  7. Inbound fast-forward   github_import.go inboundFastForward — states its one
//     command and calls the same function. A fast-forward is not a safe
//     exception: appending to a branch a reviewer approved is the attack. Was
//     unguarded.
//  8. Import HEAD            github_import.go setHeadIfPresent — checkHeadRef.
//     Was unguarded.
//  9. Merge a pull request   merge.go fastForward — advancing base is a ref
//     write, so it states its one command as a refCommand and calls the same
//     function. Guarded from the start: it was written after this list existed,
//     which is the list working. It is also the only writer that COMPARE-AND-SETS
//     — it read base to judge the merge, so it hands that value back to go-git
//     and the write fails rather than discarding a push that landed in between.
//
// A tenth door is a change to this list, not just a new function.

// agentRefPrefix is the machine namespace. It matches the branch coding derives
// from a session id (coding.BranchFor), spelled here rather than imported
// because git must not depend on the orchestrator to know its own rules — a
// forge that could only enforce a policy while another app was linked in would
// not be enforcing one.
const agentRefPrefix = "refs/heads/agent/"

// zeroOID is git's "this side does not exist": as the OLD id it means create, as
// the NEW id it means delete.
const zeroOID = "0000000000000000000000000000000000000000"

// The four sections of a `git push --signed` command stream. A push certificate
// nests ref commands inside a header and a signature, and each part ends on a
// different token, so the state is named rather than inferred from a flag — a
// blank line means "the header is over" in one of them and nothing at all in
// another.
const (
	certNone = iota // an ordinary push
	certHead        // certificate version / pusher / pushee / nonce
	certCmds        // the ref commands themselves
	certSig         // the signature block
)

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
//
// grantedRef is the ref a GRANT confines its bearer to, or "" for a caller
// authenticated as a principal. It is checked first because it is the tighter
// statement: a bearer outside its grant is refused whatever the namespace rules
// would have said.
func checkRefPolicy(cmds []refCommand, defaultBranch, grantedRef string) error {
	// A GRANT MUST NAME WHAT IT WRITES. Defence in depth behind the framing
	// guard in parseRefCommandsCaps, and the second half of the same lesson.
	//
	// The loop below is a filter: it refuses commands it dislikes. Over an EMPTY
	// slice it refuses nothing and returns nil, so "no commands" reads as "policy
	// satisfied" — which is exactly how a parser desync turned into a bypass. A
	// caller that parsed nothing and a caller that was sent nothing are
	// indistinguishable here, and only one of them is harmless.
	//
	// For a principal, an empty push is merely a no-op and stays allowed. For a
	// GRANT it is a contradiction: a grant exists to write one named ref, so a
	// grant-bearing push that names none did not parse the way we think it did.
	// Refuse it and let the caller find out, rather than forwarding bytes we could
	// not read to a program that can.
	if grantedRef != "" && len(cmds) == 0 {
		return fmt.Errorf("this push names no ref; a grant may only write %s", grantedRef)
	}

	def := ""
	if b := strings.TrimSpace(defaultBranch); b != "" {
		def = "refs/heads/" + b
	}
	for _, c := range cmds {
		if grantedRef != "" && c.Ref != grantedRef {
			return fmt.Errorf("this push may only write %s, not %s", grantedRef, c.Ref)
		}
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

// checkHeadRef refuses to make a machine branch the repository's default.
//
// HEAD is not under refs/, so no refspec and no ref command constrains it — the
// two importers set it with `symbolic-ref` from whatever the SOURCE advertises.
// Pointing it at an agent branch would publish a run's unreviewed work as the
// repository's default: what a fresh clone checks out, what the console shows,
// and what every "base" defaults to. Nothing legitimate needs it.
func checkHeadRef(ref string) error {
	if strings.HasPrefix(ref, agentRefPrefix) {
		return fmt.Errorf("%s is an agent branch and cannot become the default", ref)
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
// client sent on its first line. The refusal below has to know whether the
// client negotiated side-band-64k, because a report written in the wrong framing
// is a report the client never sees.
//
// Three shapes of command section exist on the wire, and a parser that knew only
// the first refused two kinds of ordinary push with "unreadable push":
//
//   - PLAIN — the commands, capabilities on the first one after a NUL.
//   - SHALLOW — a client pushing from a `--depth` clone declares its cut points
//     as `shallow <oid>` lines BEFORE the commands. They are state, not a ref
//     update: git's own receive-pack reads them and so must anything standing in
//     front of it. Skipping them is not a concession; the pack that follows is
//     still verified by git, which is what decides whether a shallow push is
//     acceptable at all.
//   - SIGNED — `git push --signed` wraps the commands in a push certificate. The
//     capabilities ride the `push-cert` line instead of a command, the commands
//     sit between the certificate header and its signature, and `push-cert-end`
//     closes it. The commands inside are the SAME commands, so the policy reads
//     them the same way.
//
// It stays STRICT about what it cannot account for: a body it cannot fully parse
// is refused rather than waved through, because a policy applied to commands you
// could not read is not a policy. n reports how many bytes of body the command
// section occupied, which the caller needs because the remainder is the pack and
// must still reach git verbatim.
func parseRefCommandsCaps(body []byte) (cmds []refCommand, n int, caps string, err error) {
	const (
		maxCommands = 1000    // a push updating more refs than this is not a coding run
		maxSection  = 1 << 20 // the command section is small; the PACK is the big part
	)
	cert := certNone
	for {
		if n+4 > len(body) {
			return nil, 0, "", fmt.Errorf("truncated pkt-line header")
		}
		if n > maxSection {
			return nil, 0, "", fmt.Errorf("ref command section is too large")
		}
		var size int
		if size, err = pktLen(body[n : n+4]); err != nil {
			return nil, 0, "", err
		}
		if size == 0 { // flush-pkt: the commands end here and the pack begins
			if cert != certNone {
				return nil, 0, "", fmt.Errorf("push certificate was never closed")
			}
			return cmds, n + 4, caps, nil
		}
		if size < 4 || n+size > len(body) {
			return nil, 0, "", fmt.Errorf("pkt-line length %d overruns the body", size)
		}
		line := body[n+4 : n+size]
		n += size

		// Capabilities ride the first line that carries a NUL — the first command
		// on a plain push, the `push-cert` line on a signed one.
		if i := bytes.IndexByte(line, 0); i >= 0 {
			caps = string(bytes.TrimRight(line[i+1:], "\n"))
			line = line[:i]
		}
		text := string(bytes.TrimRight(line, "\n"))

		// ONE PKT-LINE IS ONE LOGICAL LINE, and this is where that becomes true
		// rather than assumed.
		//
		// Everything below reads `text` as a single line: it compares it to
		// `push-cert`, matches a `-----BEGIN` prefix, and treats a blank one as the
		// end of a certificate header. Real git does NOT parse a certificate that
		// way — queue_commands_from_cert concatenates every cert pkt-line payload
		// into one buffer and takes the commands to be the lines between the first
		// "\n\n" and the signature offset (builtin/receive-pack.c). So a payload
		// carrying an EMBEDDED newline means two parsers reading two different
		// things from identical bytes, and the disagreement is total: we see a
		// header line and parse ZERO commands, git sees a blank line followed by a
		// command and applies it.
		//
		// That is not a hypothetical. Verified against git 2.43: a 262-byte
		// certificate whose `pusher` payload contains "\n\n<old> <new>
		// refs/heads/main\n" makes this parser return no commands at all —
		// and checkRefPolicy over an empty slice returns nil, because an empty
		// command list satisfies EVERY policy, including a grant confined to one
		// agent ref. The raw bytes are then forwarded to git receive-pack, which
		// writes refs/heads/main and fires the deploy reactor. The confinement
		// this file exists to enforce is bypassed by the framing, not by the rule.
		//
		// Refusing the frame is narrower and safer than teaching this parser git's
		// concatenation: parity with a second implementation has to be re-proved
		// every time either side changes, whereas a payload with no embedded
		// newline can only be read one way BY BOTH. Nothing legitimate is lost —
		// git's send-pack emits one line per pkt-line.
		if strings.Contains(text, "\n") {
			return nil, 0, "", fmt.Errorf("pkt-line payload carries an embedded newline")
		}

		switch {
		case text == "push-cert":
			if cert != certNone {
				return nil, 0, "", fmt.Errorf("nested push certificate")
			}
			cert = certHead
			continue
		case text == "push-cert-end":
			if cert == certNone {
				return nil, 0, "", fmt.Errorf("push certificate closed but never opened")
			}
			cert = certNone
			continue
		case cert == certHead:
			// certificate version / pusher / pushee / nonce, ended by a blank
			// line. None of it is a ref update and none of it is trusted here:
			// verifying the certificate is git's business, not the policy's.
			if text == "" {
				cert = certCmds
			}
			continue
		case cert == certSig:
			// Opaque to us; only push-cert-end above leaves this state, so a blank
			// line inside a signature block cannot be mistaken for anything.
			continue
		case cert == certCmds && strings.HasPrefix(text, "-----BEGIN"):
			cert = certSig
			continue
		case strings.HasPrefix(text, "shallow "):
			continue
		}

		if len(cmds) >= maxCommands {
			return nil, 0, "", fmt.Errorf("too many ref updates in one push")
		}
		c, perr := parseRefCommand(text)
		if perr != nil {
			return nil, 0, "", perr
		}
		cmds = append(cmds, c)
	}
}

// readCommandSection reads the pkt-lines a pushing client sends, up to and
// including the flush that ends them, and returns those bytes VERBATIM.
//
// It exists for the transports that get a STREAM rather than a body — the SSH
// channel, where the pack that follows may be gigabytes and must never be
// buffered. It knows only framing; what the bytes MEAN is read out of them by
// the one parser above, so there is a single implementation of the command
// grammar and the stream transport cannot come to a different conclusion from
// the buffered one.
//
// It is bounded: a client that never sends a flush hits maxSection and is
// refused rather than read forever.
func readCommandSection(r io.Reader) ([]byte, error) {
	const maxSection = 1 << 20
	var out bytes.Buffer
	var hdr [4]byte
	for {
		if out.Len() > maxSection {
			return nil, fmt.Errorf("ref command section is too large")
		}
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, fmt.Errorf("truncated pkt-line header")
		}
		size, err := pktLen(hdr[:])
		if err != nil {
			return nil, err
		}
		out.Write(hdr[:])
		if size == 0 { // flush: the commands end here and the pack begins
			return out.Bytes(), nil
		}
		if size < 4 {
			return nil, fmt.Errorf("pkt-line length %d is not a frame", size)
		}
		if _, err := io.CopyN(&out, r, int64(size-4)); err != nil {
			return nil, fmt.Errorf("truncated pkt-line payload")
		}
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
//
// A pkt-line cannot exceed 65520 bytes INCLUDING its own 4-byte length, and the
// length is 4 hex digits — so a longer payload does not merely truncate, it
// renders a 5-digit number that the client reads as a different, tiny frame and
// then desynchronizes on. Wrapping a whole multi-ref report in one band-1 packet
// hit that at around 200 refs and produced the exact "RPC failed / unexpected
// disconnect" this function exists to avoid. So the report is CHUNKED across as
// many band-1 packets as it needs, which is what side-band-64k means, and each
// ng line is bounded independently so one absurd ref name cannot overflow a
// frame on its own.
func refusalReport(cmds []refCommand, caps, reason string) []byte {
	var report bytes.Buffer
	report.Write(packetWrite("unpack ok\n"))
	for _, c := range cmds {
		report.Write(packetWrite(clip("ng "+c.Ref+" "+reason, maxPktPayload-1) + "\n"))
	}
	report.WriteString("0000")

	if !hasCap(caps, "side-band-64k") {
		return report.Bytes()
	}
	// Band 1 is the report stream. The band byte is part of the payload, so each
	// chunk carries one fewer byte than a bare pkt-line could.
	var out bytes.Buffer
	for rest := report.Bytes(); len(rest) > 0; {
		n := min(len(rest), maxPktPayload-1)
		out.Write(packetWrite("\x01" + string(rest[:n])))
		rest = rest[n:]
	}
	out.WriteString("0000")
	return out.Bytes()
}

// maxPktPayload is the most a single pkt-line may carry: git's 65520-byte frame
// less its own 4-byte hex length.
const maxPktPayload = 65520 - 4

// clip bounds one report line so it always fits a frame, cutting on a rune
// boundary so a truncated line stays valid UTF-8 for whatever prints it.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
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
