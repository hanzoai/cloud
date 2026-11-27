package git

// The two ways the control was WRONG about the wire, rather than wrong about
// the rule: a refusal the client could not read, and two ordinary pushes it
// could not parse.
//
// Both matter more than they look. A control that garbles its own refusal
// produces "RPC failed / unexpected disconnect", which is indistinguishable
// from a broken forge — and a control that refuses `git push` from a shallow
// clone is a control an operator switches off to "fix the git server". The
// refusals below are the ones a person reads; the parses below are the ones a
// person makes by accident.

import (
	"fmt"
	"strings"
	"testing"
)

// pktLines splits a pkt-line stream, failing the test the moment a frame does
// not account for itself — which is exactly the desync a 5-hex length causes.
func pktLines(t *testing.T, b []byte) []string {
	t.Helper()
	var out []string
	for n := 0; n < len(b); {
		if n+4 > len(b) {
			t.Fatalf("truncated pkt-line header at %d (of %d)", n, len(b))
		}
		size, err := pktLen(b[n : n+4])
		if err != nil {
			t.Fatalf("pkt-line length at %d is not hex (%q): %v", n, b[n:n+4], err)
		}
		if size == 0 {
			out = append(out, "")
			n += 4
			continue
		}
		if size < 4 || n+size > len(b) {
			t.Fatalf("pkt-line at %d claims %d bytes, %d remain — THE CLIENT DESYNCS HERE",
				n, size, len(b)-n)
		}
		out = append(out, string(b[n+4:n+size]))
		n += size
	}
	return out
}

// A pkt-line's length is FOUR hex digits, so a payload over 65516 bytes cannot
// be framed at all — packetWrite renders five digits and the client reads the
// first four as a different, tiny frame. Wrapping a whole multi-ref report in
// one band-1 packet hit that, and the report the control exists to deliver
// became the disconnect it exists to avoid.
func TestARefusalReportIsAlwaysFramed(t *testing.T) {
	for _, n := range []int{1, 200, maxCommandsForTest} {
		t.Run(fmt.Sprintf("%d refs", n), func(t *testing.T) {
			cmds := make([]refCommand, n)
			for i := range cmds {
				cmds[i] = refCommand{
					Old: zeroOID, New: strings.Repeat("a", 40),
					Ref: fmt.Sprintf("refs/heads/agent/%s-%04d", strings.Repeat("long", 12), i),
				}
			}
			reason := "refs/heads/agent/x is an agent branch: it may be created and never rewritten or deleted"

			plain := refusalReport(cmds, "report-status", reason)
			if got := len(pktLines(t, plain)); got < n {
				t.Fatalf("plain report carried %d lines for %d refs", got, n)
			}

			// The side-band framing is the one that broke. Every band-1 packet must
			// frame, and their concatenated payloads must be the whole report.
			banded := refusalReport(cmds, "report-status side-band-64k", reason)
			var body strings.Builder
			for _, line := range pktLines(t, banded) {
				if line == "" {
					continue // flush
				}
				if line[0] != 1 {
					t.Fatalf("a report packet was not on band 1: %q", line[:1])
				}
				body.WriteString(line[1:])
			}
			inner := pktLines(t, []byte(body.String()))
			if len(inner) < n+1 {
				t.Fatalf("the demuxed report carried %d lines, want at least %d", len(inner), n+1)
			}
			if !strings.HasPrefix(inner[0], "unpack ok") {
				t.Fatalf("the report does not start with unpack ok: %q", inner[0])
			}
			ng := 0
			for _, l := range inner {
				if strings.HasPrefix(l, "ng ") {
					ng++
				}
			}
			if ng != n {
				t.Fatalf("the report refused %d of %d refs — a partially reported push is worse than a refused one", ng, n)
			}
			t.Logf("%d refs → %d band-1 packets, every frame accounted for", n, len(pktLines(t, banded)))
		})
	}
}

// maxCommandsForTest mirrors the parser's own cap: the report has to survive the
// largest push the parser will accept, or the cap is where the framing breaks.
const maxCommandsForTest = 1000

// A push from a `--depth` clone declares its cut points as `shallow <oid>` lines
// BEFORE the commands. The parser read the first one as a malformed ref command
// and answered 400 "unreadable push" — fail-closed, but closed on an ordinary
// client doing an ordinary thing, which is how a control gets removed.
//
// These bytes are what git 2.43 actually sent, captured off the wire.
func TestAShallowPushParses(t *testing.T) {
	body := pkt("shallow 287a5329583dc99b85336a97a080bb3c69e60887\n") +
		pkt(zeroOID+" d5451c2923a6e5e76bac3b0982e39725854acdab refs/heads/probe\x00 "+
			"report-status side-band-64k quiet object-format=sha1\n") +
		"0000PACK\x00\x00\x00\x02"

	cmds, n, caps, err := parseRefCommandsCaps([]byte(body))
	if err != nil {
		t.Fatalf("a shallow push must parse: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Ref != "refs/heads/probe" {
		t.Fatalf("shallow push read %d commands: %+v", len(cmds), cmds)
	}
	if !cmds[0].Creates() {
		t.Fatal("the command's old id was misread")
	}
	if !hasCap(caps, "side-band-64k") {
		t.Fatalf("capabilities were lost: %q", caps)
	}
	if rest := body[n:]; !strings.HasPrefix(rest, "PACK") {
		t.Fatalf("the pack boundary is wrong: n=%d leaves %q", n, rest[:min(8, len(rest))])
	}
	t.Log("a shallow push parses, and the pack boundary is exact")
}

// `git push --signed` wraps the commands in a push certificate: capabilities on
// the push-cert line, a header, the commands, a signature, push-cert-end. The
// parser saw "certificate version 0.1" as a ref command and answered 400.
//
// The blank line inside the signature block is deliberate — a PGP signature has
// one, and a parser that treats "blank line" as a single state ends the
// signature early and then reads base64 as ref commands.
func TestASignedPushParses(t *testing.T) {
	body := pkt("shallow 287a5329583dc99b85336a97a080bb3c69e60887\n") +
		pkt("push-cert\x00 report-status side-band-64k quiet object-format=sha1\n") +
		pkt("certificate version 0.1\n") +
		pkt("pusher SHA256:A/xDgQT1lLbfvmwznvqmd3uvBvXDItACPtNSAF+bm9Q  1786026933 -0700\n") +
		pkt("pushee https://git.hanzo.ai/acme/code.git\n") +
		pkt("nonce abc123nonce\n") +
		pkt("\n") +
		pkt(zeroOID+" d5451c2923a6e5e76bac3b0982e39725854acdab refs/heads/agent/abc123def456\n") +
		pkt("-----BEGIN PGP SIGNATURE-----\n") +
		pkt("\n") + // the blank line a PGP armor header carries
		pkt("iQEzBAABCgAdFiEE0000000000000000000000000000000000\n") +
		pkt("-----END PGP SIGNATURE-----\n") +
		pkt("push-cert-end\n") +
		"0000PACK\x00\x00\x00\x02"

	cmds, n, caps, err := parseRefCommandsCaps([]byte(body))
	if err != nil {
		t.Fatalf("a signed push must parse: %v", err)
	}
	if len(cmds) != 1 || cmds[0].Ref != "refs/heads/agent/abc123def456" {
		t.Fatalf("signed push read %d commands: %+v", len(cmds), cmds)
	}
	if !hasCap(caps, "side-band-64k") {
		t.Fatalf("capabilities did not come off the push-cert line: %q", caps)
	}
	if rest := body[n:]; !strings.HasPrefix(rest, "PACK") {
		t.Fatalf("the pack boundary is wrong: n=%d leaves %q", n, rest[:min(8, len(rest))])
	}
	// And the policy still SEES those commands — parsing a certificate must not
	// become a way to smuggle a push past the rule.
	if err := checkRefPolicy(cmds, "main", "refs/heads/agent/somebody-else"); err == nil {
		t.Fatal("A SIGNED PUSH ESCAPED ITS GRANT — the certificate is not a bypass")
	}
	t.Log("a signed push parses, and its commands are still judged")
}

// An unterminated certificate is a body we cannot account for, and a policy
// applied to commands you could not read is not a policy.
func TestAnUnclosedCertificateIsRefused(t *testing.T) {
	body := pkt("push-cert\x00 report-status\n") +
		pkt("certificate version 0.1\n") +
		pkt("\n") +
		pkt(zeroOID+" d5451c2923a6e5e76bac3b0982e39725854acdab refs/heads/main\n") +
		"0000PACK"
	if _, _, _, err := parseRefCommandsCaps([]byte(body)); err == nil {
		t.Fatal("an unterminated push certificate was accepted")
	}
	t.Log("an unterminated certificate is refused, not partially read")
}
