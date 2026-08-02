package reference

// fetch.go takes one publisher's bytes. It is the same shape luxfi/aml
// pkg/screen arrived at for the sanctions lists, for the same reasons, and the
// reasons are worth restating because each one is a measured failure:
//
//   - RETRY TRANSPORT, NEVER A PARSE. A publisher that just refused a TLS
//     handshake will serve the file correctly seconds later; a publisher whose
//     schema changed will fail identically on every attempt, so retrying it only
//     delays the refusal.
//   - A TRUNCATED LIST IS THE DANGEROUS FAILURE. It parses. It yields a shorter
//     list of members, every one of them correct, and nothing anywhere reports a
//     problem — so a response that reaches the read limit is an error rather
//     than a shorter set.
//   - THE DIGEST IS NOT DECORATION. It is what tells a refresh that changed
//     nothing from a refresh that did not run, and it is what makes ingest
//     idempotent: the version IS the content.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	// maxBody bounds one download, and it is the bound that decides how much the
	// PARSE may cost — a parser turns bytes into entries one at a time, so
	// whatever arrives here is allocated before any later gate can look at it.
	//
	// Measured: the largest source in the catalog is AWS's ip-ranges.json at 2.6
	// MB, and the next is under a tenth of that. Sixteen is six times the largest
	// and leaves room for years of growth; the previous sixty-four was twenty-five
	// times it, and twenty-five times a few megabytes of adversarial one-token
	// lines is a heap this one-replica deployment does not have. A publisher that
	// outgrows this refuses on its own row and ages out visibly, which is a
	// condition an operator can see and raise — unlike an OOM.
	maxBody = 16 << 20
	// attempts is how many times a publisher is asked before its source is
	// recorded failed.
	attempts = 3
	// backoff grows per attempt: a publisher that just failed under load is not
	// helped by being asked again immediately.
	backoff = 5 * time.Second
	// fetchTimeout bounds one attempt end to end.
	fetchTimeout = 2 * time.Minute
)

// download takes one URL and returns its bytes. It is a value rather than a
// direct call so the retry behaviour — which decides whether a blip costs a day
// of freshness — is testable without a network.
type download func(ctx context.Context, url string) ([]byte, error)

// wire is the real downloader.
func wire(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hanzo-reference/1")
	client := &http.Client{Timeout: fetchTimeout, CheckRedirect: hop}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if len(body) == maxBody {
		return nil, fmt.Errorf("%s reached the %d byte limit and may be truncated", url, maxBody)
	}
	return body, nil
}

// hop decides whether a publisher's redirect may be followed.
//
// Every Origin in the catalog is an HTTPS constant, so the ONE thing about the
// address this process cannot state in code is where a redirect goes. This runs
// inside the cluster, so "wherever the publisher says" reaches the pod network
// and the instance metadata address; and a hop to http:// hands the whole
// baseline every tenant's decisions read to anyone on the path.
//
// So a hop keeps the two properties the origin already had — TLS, and a
// destination outside this network — and is refused otherwise. The literal
// address forms are what a redirect can carry without a lookup; a name that
// RESOLVES inward is not caught here and does not need to be, because it is the
// publisher's own DNS and the same trust as the bytes themselves.
func hop(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return fmt.Errorf("%s redirected %d times", via[0].URL, len(via))
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("%s redirected to %s, and a reference source is fetched over TLS or not at all", via[0].URL, req.URL.Scheme)
	}
	if inward(req.URL.Hostname()) {
		return fmt.Errorf("%s redirected to %s, which is inside this network", via[0].URL, req.URL.Host)
	}
	return nil
}

// inward reports whether a host is a literal address this process should never
// be sent to: private, loopback, link-local (the instance metadata address is
// one), unspecified or multicast. A name is not judged here — resolving it is the
// publisher's own DNS.
func inward(host string) bool {
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	a = a.Unmap()
	return a.IsPrivate() || !a.IsGlobalUnicast()
}

// pull downloads one source and parses it, retrying transport failures.
func pull(ctx context.Context, get download, src Source, wait func(context.Context, time.Duration) error) ([]Entry, error) {
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		body, err := get(ctx, src.Origin)
		if err != nil {
			last = fmt.Errorf("attempt %d of %d: %w", attempt, attempts, err)
			// The context ending is the deployment shutting down or giving up, not the
			// publisher failing, so there is nothing to retry into.
			if ctx.Err() != nil {
				return nil, last
			}
			if attempt < attempts {
				if werr := wait(ctx, time.Duration(attempt)*backoff); werr != nil {
					return nil, last
				}
				continue
			}
			return nil, last
		}
		return src.parse(body)
	}
	return nil, last
}

// pause waits, or returns early when the context ends.
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// digest is the content address of a parsed source: the version.
//
// It is computed over the SORTED, canonical rendering of the entries rather
// than over the downloaded bytes, and that is the whole point. A publisher who
// reorders their file, or adds a timestamp comment, has not changed the set —
// digesting the bytes would mint a new version and re-ingest every row for a
// change that is not one. Digesting the meaning makes the version an identity:
// the same set is the same version, whoever fetched it and whenever.
func digest(entries []Entry) string {
	h := sha256.New()
	for _, e := range order(entries) {
		fmt.Fprintf(h, "%s\x00", e.Key)
		for _, k := range keys(e.Value) {
			fmt.Fprintf(h, "%s\x01%s\x02", k, e.Value[k])
		}
		fmt.Fprintf(h, "%g\x03%d\x04%d\x1e", e.Score, e.Orgs, e.N)
	}
	return hex.EncodeToString(h.Sum(nil))
}
