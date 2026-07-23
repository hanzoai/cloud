package research

// ssrf.go gates the one BYO surface a research experiment can carry: an `endpoint`
// URL naming the arm a run was measured against (HIP-0512 §5 — "BYO endpoints are an
// SSRF surface and are gated accordingly"). An experiment is otherwise inert evidence
// (it is stored, not fetched), so the gate exists to refuse a hostile URL AT INGEST —
// before any later worker that dials the recorded arm can be steered at loopback, the
// RFC-1918/ULA private ranges, link-local, or the cloud metadata service.
//
// The refusal set is a DENYLIST of the address classes a customer endpoint must never
// resolve to (an allowlist is wrong here — the whole point is arbitrary customer
// endpoints). HTTPS is required, per the HIP.
//
// SCOPE BOUNDARY (left for the durable-execution increment, HIP-0512 §5 / Reference
// Implementation stage 3): this validates the URL's resolved addresses at INGEST. The
// complete control — a controlled egress resolver, pinning the validated IP through to
// the dial so a DNS rebind cannot swap it (TOCTOU), plus redirect/response/lifetime
// bounds — belongs with the code path that actually dials the arm. This slice stores
// the endpoint; it does not dial it, so ingest-time validation is the correct gate for
// this slice and the dial-time pin is the next increment.

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ssrfSafe reports whether rawURL is a safe BYO endpoint to record: an https URL whose
// host resolves ONLY to routable public addresses. A parse failure, a non-https
// scheme, a missing host, an unresolvable host, or ANY resolved address in a blocked
// class is refused. Resolving and checking EVERY address (not just the first) closes
// the multi-A-record bypass where a name serves one public and one private address.
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
	// A literal IP host is checked directly; a name is resolved and EVERY address it
	// yields is checked, so a name cannot smuggle one private address past the gate.
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

// blockedIP reports whether ip is in a class a BYO endpoint must never target:
// loopback, the RFC-1918/RFC-4193 private ranges, link-local (which INCLUDES the
// 169.254.169.254 / fd00:ec2::254 cloud metadata addresses), the unspecified address,
// and multicast. The metadata addresses are additionally named explicitly — they are
// the highest-value SSRF target and naming them documents the intent even though the
// link-local check already covers them.
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast() ||
		isMetadata(ip)
}

// metadataIPs are the well-known cloud instance-metadata service addresses (AWS/GCP/
// Azure IMDS at 169.254.169.254, its IPv6 form on AWS at fd00:ec2::254). Covered by
// the link-local check; enumerated here so the intent is legible and future non-
// link-local metadata endpoints have one place to land.
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
