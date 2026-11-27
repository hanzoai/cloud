package research

// ssrf.go gates the one BYO surface a research experiment can carry: an `endpoint` URL
// naming the arm a run was measured against (HIP-0512 §5). ssrfSafe is INGEST HYGIENE —
// it refuses the obviously-hostile URL at record time — NOT the dial-time control. A
// rebind attacker can flip DNS AFTER ingest; this slice STORES the endpoint and never
// dials it, so anything that ever DIALS a recorded endpoint must do it through
// outbound.Client, which judges the address the socket actually connects to.
//
// The address rule is hanzoai/outbound's: every answer the host resolves to must be
// public. HTTPS is required, per the HIP.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hanzoai/outbound"
)

// ssrfSafe reports whether rawURL is a safe BYO endpoint to record: an https URL whose
// host resolves ONLY to public addresses. A parse failure, a non-https scheme, a
// missing host, an unresolvable host, or ANY resolved address outbound.Public refuses
// is refused. Resolving and checking EVERY address (not just the first) closes the
// multi-A-record bypass.
func ssrfSafe(ctx context.Context, rawURL string) error {
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
	// The message names the host and nothing the resolver said, so the 422 is not a
	// probe of internal names or of the cluster's nameserver.
	if err := outbound.Host(ctx, host); errors.Is(err, outbound.ErrRefused) {
		return fmt.Errorf("endpoint: host %q is not a permitted target", host)
	} else if err != nil {
		return fmt.Errorf("endpoint: host %q does not resolve", host)
	}
	return nil
}
