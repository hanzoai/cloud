package research

// ssrf.go gates the one BYO surface a research experiment can carry: an `endpoint` URL
// naming the arm a run was measured against (HIP-0512 §5). ssrfSafe is BEST-EFFORT INGEST
// HYGIENE — it refuses the obviously-hostile URL at record time — NOT the dial-time
// control. A rebind attacker can flip DNS AFTER ingest, and the denylist is inherently
// incomplete; this slice STORES the endpoint and never dials it, so the authoritative
// control is the DialContext IP-pin that must land WITH the first dialer (design note at
// dialTimeGuardTODO). Do not ship any code that DIALS a recorded endpoint until that pin
// exists.
//
// The refusal set is a DENYLIST of the address classes a customer endpoint must never
// resolve to (an allowlist over arbitrary customer endpoints is impractical). HTTPS is
// required, per the HIP.

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ssrfSafe reports whether rawURL is a safe BYO endpoint to record: an https URL whose
// host resolves ONLY to routable public addresses. A parse failure, a non-https scheme, a
// missing host, an unresolvable host, or ANY resolved address in a blocked class is
// refused. Resolving and checking EVERY address (not just the first) closes the
// multi-A-record bypass. NOTE: resolution here is advisory (rebind can change it post-
// ingest) — see the file header.
func ssrfSafe(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("endpoint: unparseable url")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("endpoint: scheme %q not allowed (https required)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("endpoint: missing host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if blockedIP(ip) {
			return fmt.Errorf("endpoint: address %s is not a permitted target", ip)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("endpoint: host %q does not resolve", host)
	}
	if len(ips) == 0 {
		return fmt.Errorf("endpoint: host %q resolves to no address", host)
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return fmt.Errorf("endpoint: host %q resolves to blocked address %s", host, ip)
		}
	}
	return nil
}

// blockedIP reports whether ip is in a class a BYO endpoint must never target. Go's stdlib
// predicates cover loopback, the RFC-1918/RFC-4193 private ranges, link-local (which
// INCLUDES the 169.254.169.254 / fd00:ec2::254 cloud metadata addresses), the unspecified
// address, and multicast; extraDeniedCIDRs adds the ranges the stdlib predicates MISS —
// CGNAT (where Alibaba's IMDS lives), the IANA-special protocol/test/reserved blocks
// (where Oracle's IMDS lives), and their v6 analogues.
func blockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || isMetadata(ip) {
		return true
	}
	for _, n := range extraDeniedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// extraDeniedCIDRs are the non-private, non-link-local ranges the stdlib predicates do NOT
// cover but a BYO endpoint must never reach — each proven reachable past a naive denylist.
var extraDeniedCIDRs = mustCIDRs(
	"100.64.0.0/10",   // CGNAT (RFC 6598) — Alibaba Cloud IMDS 100.100.100.200 lives here
	"0.0.0.0/8",       // "this network" (RFC 1122) — 0.x.x.x is not a routable target
	"192.0.0.0/24",    // IETF protocol assignments (RFC 6890) — Oracle Cloud IMDS 192.0.0.192
	"192.0.2.0/24",    // TEST-NET-1 (RFC 5737)
	"198.18.0.0/15",   // benchmarking (RFC 2544)
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"240.0.0.0/4",     // reserved (RFC 1112), incl. 255.255.255.255 broadcast
	"64:ff9b::/96",    // NAT64 (RFC 6052) — can embed a private v4
	"2001:db8::/32",   // documentation (RFC 3849)
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// metadataIPs are the well-known cloud instance-metadata service addresses (AWS/GCP/Azure
// IMDS at 169.254.169.254, its IPv6 form on AWS at fd00:ec2::254). Covered by the
// link-local check; enumerated here so the intent is legible.
var metadataIPs = []net.IP{
	net.ParseIP("169.254.169.254"),
	net.ParseIP("fd00:ec2::254"),
}

func isMetadata(ip net.IP) bool {
	for _, m := range metadataIPs {
		if ip.Equal(m) {
			return true
		}
	}
	return false
}

// dialTimeGuardTODO documents the control that MUST land with the first code path that
// DIALS a recorded endpoint (the durable-execution increment): a net.Dialer.Control /
// custom Resolver that (1) resolves through a controlled egress resolver, (2) re-checks
// every candidate address with blockedIP AT CONNECT time and PINS the connection to that
// exact validated IP so a rebind between resolve and dial cannot swap it, and (3) bounds
// redirects, response size, and connection lifetime. ssrfSafe above is ingest hygiene, not
// this. Named as a symbol so it is greppable and cannot be silently forgotten.
const dialTimeGuardTODO = "dial-time IP-pin DialContext — required before any endpoint dialer ships (HIP-0512 §5)"
