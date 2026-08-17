package clientip

// The caller's address, and the ONE rule cloud derives it by.
//
// An address is not a header. X-Forwarded-For is a list a client may write the
// first entries of and each hop appends to, so the LEFT-MOST entry is whatever
// the client typed — it is the one value in the chain that is always attacker
// controlled. Reading it as "the client" is how a request from one host becomes
// a million distinct clients: it defeats every per-IP limit keyed on it, it puts
// a chosen address into an audit row and a velocity counter, and it fills any
// table keyed on it without bound.
//
// THE RULE, in one function:
//
//	the socket peer is the truth. If the peer is not one of OUR proxies, it IS
//	the client — a TCP source address cannot be forged inside an established
//	connection, so nothing it says about itself is needed.
//
//	only a trusted peer's chain is readable. When the peer IS one of ours, walk
//	X-Forwarded-For from the RIGHT — the end each hop appends to — and take the
//	first entry that is not itself one of our proxies. Everything to its left was
//	written before our infrastructure saw the request and is therefore hearsay.
//
//	our own traffic has no client. A chain that is entirely our own addresses is
//	an in-cluster caller (a sibling service, the console BFF); it never transited
//	the public edge, so it has no client address and gets "". That is the same
//	answer this function has always given for a request with no chain at all, and
//	it is what keeps in-cluster callers out of the public edge's rate limiter.
//
// WHY A CIDR SET AND NOT A HOP COUNT. A hop count is a promise about topology
// that nothing enforces; the day an extra proxy appears, a count silently reads
// one entry too far to the left — back into attacker-written territory. A set of
// addresses is checkable against the deployment and fails in the safe direction:
// an unlisted proxy is treated as a client, which over-attributes traffic to our
// own edge rather than under-attributing an attacker's.

import (
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/zap-proto/zip"
)

// TrustedProxiesEnv names the operator knob: a comma-separated list of CIDRs
// and bare addresses that are OUR OWN forwarding hops. Set it when a deployment
// is fronted by a proxy on a PUBLIC address (a CDN edge, a cloud load balancer
// with public egress); the default below covers only private space, which is
// every hop inside a cluster.
const TrustedProxiesEnv = "CLOUD_TRUSTED_PROXIES"

// defaultTrustedProxies is the address space our own hops live in when nobody
// says otherwise: loopback, the unspecified address (never a real peer — it is
// what an in-memory test connection reports), RFC1918 private space, the
// carrier-grade NAT range a managed load balancer forwards from, link-local, and
// IPv6 unique-local. A public address is NEVER trusted by default: trusting one
// by accident is what turns every customer into one shared bucket.
var defaultTrustedProxies = []string{
	"127.0.0.0/8", "::1/128",
	"0.0.0.0/32", "::/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"100.64.0.0/10",
	"169.254.0.0/16", "fe80::/10",
	"fc00::/7",
}

// maxForwardedHops bounds how much of a chain is read. A real chain is three
// entries; a request carrying thousands is an attempt to spend CPU in the parse
// itself. Read from the right, so the bound only ever discards the OLDEST
// (left-most, least trustworthy) entries.
const maxForwardedHops = 32

// trustedProxies is resolved once per process. The environment is read at first
// use rather than at init so a test can set it before the first request without
// depending on package initialization order.
var trustedProxies = sync.OnceValue(func() proxySet {
	if s := parseProxySet(os.Getenv(TrustedProxiesEnv)); len(s.nets) > 0 {
		return s
	}
	// An unset — or entirely unparseable — knob falls back to the defaults rather
	// than to an EMPTY set. Trusting nothing sounds safer and is not: it would
	// make the ingress itself the "client", collapsing every caller into one
	// bucket and one audit address. The safe failure here is the private-space
	// default, which is correct for every in-cluster deployment we run.
	return parseProxySet(strings.Join(defaultTrustedProxies, ","))
})

// proxySet is the set of addresses that are our own forwarding hops.
type proxySet struct{ nets []netip.Prefix }

func parseProxySet(spec string) proxySet {
	var s proxySet
	for raw := range strings.SplitSeq(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if p, err := netip.ParsePrefix(raw); err == nil {
			s.nets = append(s.nets, p.Masked())
			continue
		}
		// A bare address is the single-host prefix it denotes.
		if a, err := netip.ParseAddr(raw); err == nil {
			s.nets = append(s.nets, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		}
	}
	return s
}

func (s proxySet) has(a netip.Addr) bool {
	for _, n := range s.nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// TrustedProxy reports whether addr is one of our own forwarding hops — for a
// health report or a test, never as a gate. The gate is ClientIP, which applies
// the whole rule.
func TrustedProxy(addr string) bool {
	a, ok := parseClientAddr(addr)
	return ok && trustedProxies().has(a)
}

// ClientIP is the caller's own address, as the FRAMEWORK resolved it.
//
// IT DOES NOT DERIVE ONE. zip resolves the address once at the seam and publishes
// it (caller.go: inflight{ip: fc.IP()}), and its own logger reads that rather than
// re-deriving, for a reason it states plainly: "an address the log line derived by
// its own rule would be the one field on the line that disagrees with every other
// surface". This used to be that second rule, and it did disagree — measured in
// production, 4107 requests where zip reported a real address on the same log line
// that this returned "".
//
// The disagreement was not a bug in the arithmetic. It was two trust sets: zip
// honours a proxy header only where the APP configured TrustedProxies, and this
// carried a private set of its own that the framework never saw. Ours counted the
// in-cluster peer as a proxy and walked a forwarded chain that carries nothing, so
// it answered "unknown" for every caller — correct by its own rule, and useless.
//
// Proxy awareness belongs in the framework's config, where fiber applies it to the
// value everything reads. Configure zip.Config.TrustProxy/TrustedProxies; do not
// grow a second opinion here.
func ClientIP(c *zip.Ctx) string {
	return zip.CallerOf(c.Context()).IP
}

// clientAddr IS the rule, as a pure function of the three facts it turns on: the
// socket peer, the forwarded chain, and which addresses are ours. Everything
// interesting about ClientIP is here, where it can be read and tested without a
// server — the exported wrapper only supplies the arguments.
//
// The chain is walked last-to-first, across lines and within a line, for the same
// reason in both cases: later is nearer to us, and nearer to us is truer.
func clientAddr(peerAddr string, forwarded [][]byte, tp proxySet) string {
	peer, ok := parseClientAddr(peerAddr)
	if !ok {
		return ""
	}
	if !tp.has(peer) {
		// A direct caller. The peer is the connection's own source address, so it
		// is the one fact about the client that cannot be written by the client,
		// and no header it sent is consulted at all.
		return peer.String()
	}
	seen := 0
	for _, f := range slices.Backward(forwarded) {
		hops := strings.Split(string(f), ",")
		for _, hop := range slices.Backward(hops) {
			if seen++; seen > maxForwardedHops {
				return ""
			}
			a, ok := parseClientAddr(hop)
			if !ok {
				// Not an address at all. It cannot be a hop and it must never become
				// a key, so it is skipped rather than passed through — an unparseable
				// entry is exactly how an unbounded keyspace gets fed.
				continue
			}
			if tp.has(a) {
				continue
			}
			return a.String()
		}
	}
	return ""
}

// CountryHeader is the name our edge states the caller's jurisdiction under: the
// ISO 3166-1 alpha-2 code it resolved from the connecting address. One name, so
// an operator has one thing to set at the edge and one thing to STRIP from
// inbound requests.
const CountryHeader = "CF-IPCountry"

// ClientCountry is the jurisdiction our edge resolved the caller from, or "" when
// nothing trustworthy said.
//
// IT IS THE SAME TRUST RULE AS [ClientIP], applied to a header instead of a
// chain, and it is written beside it so the two cannot drift into two rules. A
// header is a claim; what makes a claim readable is WHO the socket peer is:
//
//	a direct caller's header is the CLIENT's own writing. It is refused outright
//	— reading it would let any caller state its own jurisdiction, which on a rule
//	that escalates on geography is the same as switching the rule off.
//
//	a trusted peer's header is our EDGE's, written after the edge resolved the
//	address it saw. That is the only version of this fact anybody here holds.
//
// WHAT IT IS NOT. It is the country of the ADDRESS, never of the payer: an
// address is what a VPN moves and a proxy relays, so this is a weak signal by
// construction and is documented as one at its one consumer. The strong signal —
// the billing or KYC jurisdiction of the account — is not derivable in this binary
// today (there is no billing address, no KYC profile, and the card never touches
// this process), and inventing one from this would be worse than stating that.
//
// The value is normalised to upper case and refused unless it is exactly two
// letters. Cloudflare states "XX" for an address it could not place and "T1" for
// Tor, and neither is a country; both fail the letters test and come back "",
// which is the same answer as silence and the correct one — no jurisdiction was
// established.
func ClientCountry(c *zip.Ctx) string {
	return clientCountry(c.Fiber().IP(), string(c.Fiber().Request().Header.Peek(CountryHeader)), trustedProxies())
}

// clientCountry IS the rule, as a pure function of the three facts it turns on —
// the socket peer, what the header said, and which addresses are ours — for the
// same reason [clientAddr] is one: it can be read and tested without a server.
func clientCountry(peerAddr, stated string, tp proxySet) string {
	peer, ok := parseClientAddr(peerAddr)
	if !ok || !tp.has(peer) {
		// No peer we can place, or a direct caller. Either way the header is not
		// our edge's and is therefore not evidence.
		return ""
	}
	code := strings.ToUpper(strings.TrimSpace(stated))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	return code
}

// parseClientAddr parses one chain entry or peer address into a canonical
// address. It accepts a bare address and an address:port pair, and it UNMAPS
// IPv4-in-IPv6 so "::ffff:1.2.3.4" and "1.2.3.4" are one key rather than two.
// The canonical String() is what every counter, record and report keys on.
func parseClientAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

// ---- carrying the caller's address to a child process -----------------------

// ClientIPHeader carries the caller's address from this host to a subsystem running
// as its own process.
//
// A SUBSYSTEM CANNOT SEE THE CONNECTION. The pod runs one process per subsystem and
// the host reaches each over a unix socket, so the request a child handles has the
// SOCKET as its peer — measured empty, and constant whatever it is. Anything a child
// derives from it is therefore one value for every caller on earth, which is not a
// degraded answer but a single answer wearing everyone's name. It cost us a live
// defect: ai's public lane keys a per-visitor ceiling on the caller's address, and
// one address for everyone made that ceiling one bucket for the whole internet.
//
// So the host answers, because the host is where the connection is. ClientIP is
// already the ONE hardened definition; this only carries it across.
//
// A HEADER, because that is what survives the crossing — proven by behaviour that
// already ships rather than by a test written for the question: Authorization
// reaches ai's controllers today, which is the same journey. Forgery is handled by
// always OVERWRITING, so a caller's own claim is gone before any child reads it.
const ClientIPHeader = "X-Hanzo-Client-Ip"

// StampClientIP writes this host's answer onto the request, replacing anything the
// caller sent under that name.
//
// REGISTER IT BEFORE THE CHILDREN ARE INCLUDED. zip visits an included App with the
// middleware stack as it stood at the inclusion site, so a Use written after the
// plugin mounts never reaches them — the same rule that makes composition order
// harmless one level down makes it load-bearing here.
func StampClientIP(c *zip.Ctx) error {
	addr := ClientIP(c)
	c.Fiber().Request().Header.Set(ClientIPHeader, addr)
	// WHAT A SUBSYSTEM IN ANOTHER PROCESS WILL READ AS THE CALLER, said out loud.
	//
	// Kept, not a probe. Five separate times this estate could not answer "what did
	// that header carry" without a release, and each time the answer was inferred and
	// each inference was wrong at the rung that ships. One line answers it for the
	// next reader too, and the empty case is the interesting one: an empty stamp means
	// the address was not resolvable HERE, which is a different fault from a child
	// that never received the header at all.
	//
	// ONCE AT INFO, then per-request at debug.
	//
	// The once is not a nicety, it is the whole point: this deployment emits no debug
	// at all — measured, 0 debug lines in 8000 — and reads no level knob, so a
	// debug-only line is not quiet, it is invisible, and shipping one would have cost
	// another release to learn nothing. One info line per process says what this host
	// computes for a caller, which is the fact that has been unavailable all day, and
	// one line per pod lifetime is not noise.
	//
	// The per-request line stays at debug for whoever turns it on.
	if log := c.Log(); log != nil {
		first.Do(func() {
			log.Info("client address stamped for another process (first of this process)",
				"addr", addr, "empty", addr == "", "header", ClientIPHeader)
		})
		log.Debug("client address stamped for another process",
			"addr", addr,
			"empty", addr == "",
			"header", ClientIPHeader,
			"path", string(c.Fiber().Request().URI().Path()))
	}
	return c.Continue()
}

// first bounds the info line to one per process. A per-request info line on the
// busiest path in the fleet is how an observability line becomes an outage.
var first sync.Once

// ClientIPAcross reads the address back inside a child. Subsystems install it rather
// than deriving an address of their own.
//
// CLONED, and this is the clone that matters. The value comes back out of a buffer
// fasthttp reuses between requests, so uncloned it is a view the NEXT caller
// overwrites — two callers in a row then read one string and become one visitor,
// which is the very collapse this exists to end, reappearing a layer down. Measured:
// dropping this clone fails the crossing test; a clone at the writing end changes
// nothing, because Header.Set already copies.
func ClientIPAcross(r *http.Request) string {
	return strings.Clone(r.Header.Get(ClientIPHeader))
}
