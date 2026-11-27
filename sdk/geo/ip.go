package geo

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
//	cloudflare names the client for us. When the hop the chain resolves to is a
//	Cloudflare edge address, the client is Cloudflare's CF-Connecting-IP header:
//	Cloudflare sets it and overwrites any copy the client sent. From any other hop
//	the header is ignored, so nobody who reaches the origin directly can write it.
//	The header is read once and never re-resolved: a Workers subrequest states a
//	Cloudflare address there, and that address is the answer.
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

	"github.com/hanzoai/ai/address"
	"github.com/zap-proto/zip"
)

// ProxiesKey names the operator knob: a comma-separated list of CIDRs
// and bare addresses that are OUR OWN forwarding hops. Set it when a deployment
// is fronted by a proxy on a PUBLIC address (a CDN edge, a cloud load balancer
// with public egress); the default below covers only private space, which is
// every hop inside a cluster.
const ProxiesKey = "CLOUD_TRUSTED_PROXIES"

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

// cloudflareRanges is Cloudflare's published edge address space, read 2026-09-30
// from https://www.cloudflare.com/ips-v4 and https://www.cloudflare.com/ips-v6.
//
// Hard-coded, not fetched: an address in it makes a request header believable, and
// a fetch that fails or is tampered with must never widen that. A range Cloudflare
// adds later is missing here until this list is updated; its visitors resolve to the
// edge address (the answer before this rule), never to a header.
var cloudflareRanges = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

// cloudflare is cloudflareRanges as a set. It is not a trusted-proxy set: a
// Cloudflare hop is never skipped in the chain walk, it only makes connectingHeader
// readable.
var cloudflare = parseProxySet(strings.Join(cloudflareRanges, ","))

// connectingHeader is where Cloudflare states the address that connected to its edge.
const connectingHeader = "CF-Connecting-IP"

// maxForwardedHops bounds how much of a chain is read. A real chain is three
// entries; a request carrying thousands is an attempt to spend CPU in the parse
// itself. Read from the right, so the bound only ever discards the OLDEST
// (left-most, least trustworthy) entries.
const maxForwardedHops = 32

// trustedProxies is resolved once per process. The environment is read at first
// use rather than at init so a test can set it before the first request without
// depending on package initialization order.
var trustedProxies = sync.OnceValue(func() proxySet {
	if s := parseProxySet(os.Getenv(ProxiesKey)); len(s.nets) > 0 {
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

// Trusted reports whether addr is one of our own forwarding hops — for a
// health report or a test, never as a gate. The gate is IP, which applies
// the whole rule.
func Trusted(addr string) bool {
	a, ok := parseClientAddr(addr)
	return ok && trustedProxies().has(a)
}

// IP is the caller's own address: the socket peer for a direct caller, the
// right-most non-proxy entry of the forwarded chain for a proxied one — or
// Cloudflare's CF-Connecting-IP when that entry is a Cloudflare edge — and "" for
// an in-cluster caller that never transited the edge.
//
// It is the ONE client-address read in this repo — the edge rate limiter, the
// abuse sensor, the audit trail and every metered resource share it, so a
// forgeable address cannot enter one of them by another path.
//
// It reads EVERY X-Forwarded-For header line, not just the first. fasthttp keeps
// repeated headers as separate lines, and a client that sends its own line before
// the proxy appends to a second one would otherwise hide the real address behind
// a value it chose.
//
// The rule lives HERE, applied to the raw facts (the socket peer, the chain,
// Cloudflare's header), never to the framework's own resolution: under the host's
// zip.Config proxy trust that is the raw forwarded header, which is not an address
// once the chain has two hops. One rule, one function, no knob.
//
// BEHIND THE HOST THE ANSWER IS THE HOST'S. A subsystem is reached over a unix socket,
// so its peer is the host and the connection the caller made is one only the host
// saw. Re-reading the chain here answered for whatever the host's own peer wrote: a
// caller that reached the host directly sent no chain and became "", the in-cluster
// bucket the kms-operator logs in from, or sent one and named its own bucket. So over
// the socket the host's stamp is read, and nothing else.
func IP(c *zip.Ctx) string {
	if socket(c.Fiber().RequestCtx().RemoteAddr().String()) {
		return stamped(header(c, address.Header))
	}
	return clientAddr(Peer(c), header(c, "X-Forwarded-For"), header(c, connectingHeader), trustedProxies())
}

// socket reports whether a peer is no address at all — a unix socket, which only a
// process in this pod can open, and in front of a subsystem that is the host.
func socket(peer string) bool {
	_, ok := parseClientAddr(peer)
	return !ok
}

// stamped is the host's answer read back: one line holding one address. The host
// stamps "" for an in-cluster caller, and a request carrying none, or two, never
// passed through Stamp — either way no caller is named.
func stamped(lines [][]byte) string {
	if len(lines) != 1 {
		return ""
	}
	a, ok := parseClientAddr(string(lines[0]))
	if !ok {
		return ""
	}
	return a.String()
}

// header is every line of one request header, in a slice of its own.
//
// fasthttp's PeekAll answers in ONE buffer it reuses on the next PeekAll, so two
// reads side by side are the second header twice: the forwarded chain became
// Cloudflare's header, and a caller that reached our ingress around Cloudflare
// named its own address by sending one. The lines themselves stay valid for the
// request; only the slice holding them is reused, so cloning the slice is enough.
func header(c *zip.Ctx, name string) [][]byte {
	return slices.Clone(c.Fiber().Request().Header.PeekAll(name))
}

// Peer is the connection's source address: the one address on a request its caller
// cannot write, and a hop, never a caller — inside a subsystem it is the host's
// socket for every request. The caller is IP. Not c.Fiber().IP(): under the
// host's zip.Config TrustProxy that is the raw X-Forwarded-For value, whatever the
// caller wrote and no address at all for a chain of two or more hops.
func Peer(c *zip.Ctx) string {
	return c.Fiber().RequestCtx().RemoteIP().String()
}

// IPOf is the same rule read off net/http's facts, for a product that serves
// on the standard library rather than on zip. The peer is r.RemoteAddr, the chain is
// every X-Forwarded-For line and Cloudflare's statement is every CF-Connecting-IP
// line — the facts clientAddr already turns on, so this is the same answer, not a
// second one.
//
// It exists because apps/metering had written its own, and that one returned the
// LEFT-MOST forwarded entry: the single value in the chain a caller writes for
// itself. It fed Usage.IP, so a caller could choose the address that landed
// in the billing and audit column — the failure this package's doc opens with.
func IPOf(r *http.Request) string {
	if r == nil {
		return ""
	}
	if socket(r.RemoteAddr) {
		return stamped(lines(r.Header.Values(address.Header)))
	}
	return clientAddr(r.RemoteAddr, lines(r.Header.Values("X-Forwarded-For")), lines(r.Header.Values(connectingHeader)), trustedProxies())
}

// lines is net/http's header values in fasthttp's shape.
func lines(vs []string) [][]byte {
	out := make([][]byte, 0, len(vs))
	for _, v := range vs {
		out = append(out, []byte(v))
	}
	return out
}

// clientAddr IS the rule, as a pure function of the facts it turns on: the socket
// peer, the forwarded chain, Cloudflare's statement, and which addresses are ours.
// Everything interesting about IP is here, where it can be read and tested
// without a server — the exported wrapper only supplies the arguments.
//
// Cloudflare's statement is read only when the resolved hop is a Cloudflare edge,
// and only when it is one line holding one address. A Cloudflare hop without that
// is attributed to the edge itself — one shared bucket, never "", which would
// exempt it as an in-cluster caller.
func clientAddr(peerAddr string, forwarded, connecting [][]byte, tp proxySet) string {
	hop, ok := resolve(peerAddr, forwarded, tp)
	if !ok {
		return ""
	}
	if said, ok := cloudflareSaid(hop, connecting); ok {
		if a, ok := parseClientAddr(said); ok {
			return a.String()
		}
	}
	return hop.String()
}

// cloudflareSaid is what Cloudflare stated under one of its headers: read only when
// hop — the hop resolve reached — is a Cloudflare edge, and only when the header is
// exactly one line. Cloudflare writes these headers and overwrites any copy the
// client sent; from any other hop the same header is the caller's own writing, and
// from an in-cluster caller there is no hop to ask. The address and the country are
// both read through this, so the two cannot drift into two rules.
func cloudflareSaid(hop netip.Addr, lines [][]byte) (string, bool) {
	if !cloudflare.has(hop) || len(lines) != 1 {
		return "", false
	}
	return string(lines[0]), true
}

// resolve is the hop the request reached our infrastructure from: the peer when it
// is not ours, else the right-most forwarded entry that is not ours. False for an
// in-cluster caller.
//
// The chain is walked last-to-first, across lines and within a line, for the same
// reason in both cases: later is nearer to us, and nearer to us is truer.
func resolve(peerAddr string, forwarded [][]byte, tp proxySet) (netip.Addr, bool) {
	peer, ok := parseClientAddr(peerAddr)
	if !ok {
		return netip.Addr{}, false
	}
	if !tp.has(peer) {
		// A direct caller. The peer is the connection's own source address, so it
		// is the one fact about the client that cannot be written by the client,
		// and no header it sent is consulted at all.
		return peer, true
	}
	seen := 0
	for _, f := range slices.Backward(forwarded) {
		hops := strings.Split(string(f), ",")
		for _, hop := range slices.Backward(hops) {
			if seen++; seen > maxForwardedHops {
				return netip.Addr{}, false
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
			return a, true
		}
	}
	return netip.Addr{}, false
}

// CountryHeader is where Cloudflare states the caller's jurisdiction: the ISO 3166-1
// alpha-2 code it resolved from the address that connected to its edge.
const CountryHeader = "CF-IPCountry"

// Country is the jurisdiction Cloudflare resolved the caller from, or "" when
// nothing trustworthy said.
//
// IT IS IP's HEADER RULE, through the same function ([cloudflareSaid]): the
// header is read only when the hop the forwarded chain resolves to is a Cloudflare
// edge, and only when it is one line. A trusted peer is not enough. A request that
// reached the origin around Cloudflare still arrives through our ingress, so its
// peer is ours while the header is whatever the caller wrote:
//
//	a direct caller's header is the CLIENT's own writing.
//
//	a non-Cloudflare hop behind our ingress is a direct caller one proxy removed;
//	its header is its own writing too.
//
//	an in-cluster caller never crossed an edge, so no edge stated anything.
//
//	a Cloudflare hop's header is Cloudflare's, written after it resolved the
//	address it saw. That is the only version of this fact anybody here holds.
//
// Reading any of the first three would let a caller state its own jurisdiction,
// which on a rule that escalates on geography (commerce's risk gate) or decides
// consent (the tag config) is the same as switching the rule off.
//
// WHAT IT IS NOT. It is the country of the ADDRESS, never of the payer: an
// address is what a VPN moves and a proxy relays, so this is a weak signal by
// construction and commerce's risk gate documents it as one. The strong signal —
// the billing or KYC jurisdiction of the account — is not derivable in this binary
// today (there is no billing address, no KYC profile, and the card never touches
// this process), and inventing one from this would be worse than stating that.
//
// The value is normalised to upper case and refused unless it is exactly two
// letters. Cloudflare states "XX" for an address it could not place and "T1" for
// Tor, and neither is a country; both fail the letters test and come back "",
// which is the same answer as silence and the correct one — no jurisdiction was
// established.
//
// Behind the host it is the host's stamp (address.Country), for IP's reason.
func Country(c *zip.Ctx) string {
	return edgeSaid(c, CountryHeader, address.Country, countryCode)
}

// RegionHeader is where Cloudflare states the caller's subdivision when the zone's
// visitor location headers are on: the ISO 3166-2 code without its country ("CA"
// for California), resolved from the same address as CountryHeader.
const RegionHeader = "CF-Region-Code"

// RegionStamp carries the caller's subdivision from the host in front, beside
// address.Country, and is absent when no edge resolved one.
const RegionStamp = "X-Client-Region"

// Region is the subdivision Cloudflare resolved the caller to, or "" when
// nothing trustworthy said. It is Country's rule over RegionHeader, for
// Country's reasons: it decides which consent regime the tag config serves.
func Region(c *zip.Ctx) string {
	return edgeSaid(c, RegionHeader, RegionStamp, regionCode)
}

// edgeSaid reads what the edge said under a header: the host's stamp behind the host,
// else the header itself only when the hop is a Cloudflare edge.
func edgeSaid(c *zip.Ctx, said, stamp string, norm func(string) string) string {
	if socket(c.Fiber().RequestCtx().RemoteAddr().String()) {
		if l := header(c, stamp); len(l) == 1 {
			return norm(string(l[0]))
		}
		return ""
	}
	return edgeValue(Peer(c), header(c, "X-Forwarded-For"), header(c, said), trustedProxies(), norm)
}

// clientCountry IS the rule, as a pure function of the facts it turns on — the
// socket peer, the forwarded chain, what the header said, and which addresses are
// ours — for the same reason [clientAddr] is one: it can be read and tested without
// a server.
func clientCountry(peerAddr string, forwarded, stated [][]byte, tp proxySet) string {
	return edgeValue(peerAddr, forwarded, stated, tp, countryCode)
}

// edgeValue is what a Cloudflare edge stated, normalised, when the hop the chain
// resolves to is one; else "".
func edgeValue(peerAddr string, forwarded, stated [][]byte, tp proxySet, norm func(string) string) string {
	hop, ok := resolve(peerAddr, forwarded, tp)
	if !ok {
		return ""
	}
	said, ok := cloudflareSaid(hop, stated)
	if !ok {
		return ""
	}
	return norm(said)
}

// countryCode is said as an upper-case ISO 3166-1 alpha-2 code, or "" when it is not
// exactly two letters.
func countryCode(said string) string {
	code := strings.ToUpper(strings.TrimSpace(said))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	return code
}

// regionCode is said as an upper-case subdivision code of one to three letters or
// digits ("CA", "NSW", "75"), or "" when it is anything else.
func regionCode(said string) string {
	code := strings.ToUpper(strings.TrimSpace(said))
	if len(code) == 0 || len(code) > 3 {
		return ""
	}
	for _, r := range code {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return code
}

// parseClientAddr parses one chain entry or peer address into a canonical
// address. It accepts a bare address and an address:port pair, UNMAPS
// IPv4-in-IPv6 so "::ffff:1.2.3.4" and "1.2.3.4" are one key rather than two, and
// drops an IPv6 zone, which names a local interface and no remote client — kept,
// it is a second key for one address, and a zoned address is in no prefix.
// The canonical String() is what every counter, record and report keys on.
func parseClientAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}

// ---- carrying the caller's address to a child process -----------------------

// A SUBSYSTEM CANNOT SEE THE CONNECTION. The pod runs one process per subsystem and
// the host reaches each over a unix socket, so the request a child handles has the
// SOCKET as its peer — measured empty, and constant whatever it is. Anything a child
// derives from it is therefore one value for every caller on earth, which is not a
// degraded answer but a single answer wearing everyone's name, and ai's per-visitor
// ceiling is keyed on exactly that value.
//
// So the host answers, because the host is where the connection is. IP above is
// already the ONE hardened definition; the rest of this section only carries it
// across.
//
// THE NAME IT CROSSES UNDER IS address.Header, AND IT IS NOT DEFINED HERE. This host
// writes it and ai reads it, so a copy on each side would be two constants held equal
// by nothing but attention — and the failure that invites is silent, since a stamp
// under one name and a read under another compiles perfectly and simply finds
// nothing. github.com/hanzoai/ai/address is a leaf that imports nothing, so naming it
// costs this host no weight: the single symbol is reachable from both sides and the
// two cannot disagree.

// Stamp writes this host's answer onto the request, replacing anything the
// caller sent under that name.
//
// REGISTER IT BEFORE THE CHILDREN ARE INCLUDED. zip visits an included App with the
// middleware stack as it stood at the inclusion site, so a Use written after the
// plugin mounts never reaches them — the same rule that makes composition order
// harmless one level down makes it load-bearing here.
//
// EVERY LINE GOES, then one is written. Header.Set replaces only the first line under
// a name, so a caller that sent two kept its second, and a child reading any line but
// the first read the caller. The country crosses the same way, under address.Country,
// and the subdivision under RegionStamp, each only when an edge resolved one: absent is the answer for no jurisdiction, and
// a caller's own copy never survives to stand in for it.
func Stamp(c *zip.Ctx) error {
	addr, country, region := IP(c), Country(c), Region(c)
	h := &c.Fiber().Request().Header
	h.Del(address.Header)
	h.Del(address.Country)
	h.Del(RegionStamp)
	h.Set(address.Header, addr)
	if country != "" {
		h.Set(address.Country, country)
	}
	if region != "" {
		h.Set(RegionStamp, region)
	}
	// WHAT A SUBSYSTEM IN ANOTHER PROCESS WILL READ AS THE CALLER, said out loud.
	//
	// Kept, not a probe. Five separate times this estate could not answer "what did
	// that header carry" without a release, and each time the answer was inferred and
	// each inference was wrong at the rung that ships. One line answers it for the
	// next reader too, and the empty case is the interesting one: an empty stamp means
	// the address was not resolvable HERE, which is a different fault from a child
	// that never received the header at all.
	//
	// ONCE, at info. One line per process says what this host computes for a
	// caller, and one line per pod lifetime is not noise. A line per request is: the
	// host's logger has no level knob, so it was written for every routed request,
	// beside the request line that already names the path.
	if log := c.Log(); log != nil {
		first.Do(func() {
			log.Info("client address stamped for another process (first of this process)",
				"addr", addr, "empty", addr == "", "header", address.Header)
		})
	}
	return c.Continue()
}

// first bounds the info line to one per process. A per-request info line on the
// busiest path in the fleet is how an observability line becomes an outage.
var first sync.Once

// IPAcross reads the address back inside a child, which is what the crossing
// tests assert on. A subsystem reads the header itself rather than deriving an
// address of its own — ai does, and nothing installs a resolver any more.
//
// CLONED, and this is the clone that matters. The value comes back out of a buffer
// fasthttp reuses between requests, so uncloned it is a view the NEXT caller
// overwrites — two callers in a row then read one string and become one visitor,
// which is the very collapse this exists to end, reappearing a layer down. Measured:
// dropping this clone fails the crossing test; a clone at the writing end changes
// nothing, because Header.Set already copies.
func IPAcross(r *http.Request) string {
	return strings.Clone(r.Header.Get(address.Header))
}

// Proxies is the resolved trust set as CIDR strings, for a framework that wants
// the allowlist rather than the predicate.
//
// zip resolves the caller once at the client and honours a forwarded header only
// where the app names its own hops (zip.Config.TrustProxy + TrustedProxies), so
// without this the framework's answer is the socket peer — which behind an
// ingress is one address for every caller on earth. Handing it THIS list keeps
// one trust set in one place: the same knob, the same defaults, and no second
// spelling to drift.
func Proxies() []string {
	s := trustedProxies()
	out := make([]string, 0, len(s.nets))
	for _, n := range s.nets {
		out = append(out, n.String())
	}
	return out
}
