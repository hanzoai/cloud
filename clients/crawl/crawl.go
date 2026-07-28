// Package crawl is Hanzo Crawl: fetch one URL and return its readable content as
// markdown, in-process, in Go.
//
// It replaces the dial to a separate Crawl4AI deployment. That service was named
// in config but did not exist — crawl.hanzo.svc.cluster.local was NXDOMAIN — so
// every scrape returned {success:false, "no such host"} while the surface in front
// of it answered 200. This package is the same move already made for the SEARCH
// half in clients/websearch/search.go, which replaced a SearXNG pod with in-process
// Go: one fewer non-Go dependency, one fewer thing that can be down, and no network
// hop for work that is a fetch and a parse.
//
// The engine is three orthogonal steps, each in its own file and each testable
// without the others:
//
//	Fetch    (this file)  URL   → HTML bytes, over guarded HTTP
//	extract  (extract.go) HTML  → the readable subtree + metadata
//	render   (markdown.go) node → markdown
//
// # Fetching untrusted URLs from inside the cluster is the security boundary
//
// The caller supplies the URL, so this is a server-side request forgery primitive
// by construction, and it runs INSIDE the cluster with an in-namespace service DNS
// and a cloud metadata endpoint on 169.254.169.254 that hands out credentials to
// anyone who asks. A naive fetcher here is a credential exfiltration hole, not a
// bug. The separate service got network policy for free by being a separate pod;
// folding it in means this package must carry the guard itself.
//
// So: only http/https, and every address actually dialed must be a public unicast
// address. The check is in the DIALER, not on the hostname, because checking a
// hostname up front and then letting the transport resolve it again is a
// time-of-check/time-of-use gap — DNS rebinding walks straight through it. Dialing
// is the only moment the real destination is known, and that is where it is
// enforced. Redirects are followed but re-enter the same dialer, so a public URL
// that 302s to 169.254.169.254 is refused at the hop that matters.
package crawl

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// A realistic desktop UA, for the same reason search.go carries one: sites
	// serve datacenter IPs a challenge page instead of content when the client
	// looks automated, and a challenge page extracts to confident nonsense.
	browserUA = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"

	// maxBody caps what is read from a response. Content is fetched to be read by
	// a model with a bounded context, so the cap protects memory without costing
	// anything real: past a few megabytes of HTML the extractor is picking through
	// boilerplate, not article text.
	maxBody = 8 << 20

	// maxRedirects is Go's own default, made explicit because the redirect chain is
	// part of the security boundary here (each hop is re-checked by the dialer).
	maxRedirects = 10

	// fetchTimeout bounds one page fetch end to end. Callers get an error, never a
	// hang: the surfaces in front of this are request-scoped and a stuck fetch would
	// hold a connection open behind them.
	fetchTimeout = 20 * time.Second
)

// Page is one crawled document. It is what every surface in front of this package
// renders, so it holds the extracted content and the provenance needed to cite it —
// not the raw HTML, which no caller has wanted and which would keep the whole
// response alive in memory.
type Page struct {
	// URL is the FINAL url after redirects, not the one requested. Callers cite it,
	// so it must be where the content actually came from.
	URL string
	// Title is the document title, best-effort from <title> or og:title.
	Title string
	// Markdown is the readable content. Empty is a legitimate result for a page
	// that is genuinely all chrome; it is not an error.
	Markdown string
	// Metadata carries what the document said about itself (description, og:*,
	// language, status). Map-shaped because it is passed through to a JSON contract
	// that predates this package and accepts arbitrary keys.
	Metadata map[string]any
}

// ErrBlocked is returned when a URL resolves to an address this package refuses to
// dial. It is deliberately distinguishable: a caller may want to report a blocked
// fetch differently from a site that was merely down, and a log line that cannot
// tell them apart makes an attack look like an outage.
var ErrBlocked = errors.New("crawl: refused to dial a non-public address")

// client is process-wide so connections pool across fetches. Its transport carries
// the guarded dialer, which is what makes every request — including every redirect
// hop — subject to the address check.
var client = &http.Client{
	Timeout:   fetchTimeout,
	Transport: guardedTransport(),
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("crawl: stopped after %d redirects", maxRedirects)
		}
		// Scheme is re-checked per hop: https → file:// or a custom scheme is a
		// downgrade the dialer would never see, because it never dials.
		if s := req.URL.Scheme; s != "http" && s != "https" {
			return fmt.Errorf("%w: redirect to scheme %q", ErrBlocked, s)
		}
		return nil
	},
}

func guardedTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		// Resolve here and dial the CHECKED ip literal. Handing the hostname back to
		// the dialer would resolve a second time, and a name that answers differently
		// on the second lookup is exactly the rebinding case the check exists for.
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			if !public(ip.IP) {
				// Refuse the whole fetch rather than skipping to the next address: a
				// host that answers with ANY internal address is not a host we are
				// willing to reach, and trying the rest would let an attacker get a
				// fetch by listing one public IP alongside the target.
				return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlocked, host, ip.IP)
			}
		}
		for _, ip := range ips {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("crawl: no address for %s", host)
		}
		return nil, lastErr
	}
	return t
}

// public reports whether ip is a globally routable unicast address — the only kind
// this package will dial.
//
// Written as an allowlist of "is public" rather than a blocklist of known-bad
// ranges. A blocklist is one forgotten range (unique-local fc00::/7, the IPv4
// carrier-grade NAT block, 0.0.0.0/8, a v4-mapped v6 address smuggling 127.0.0.1
// past a v4-only check) away from being useless, and the forgotten one is never
// noticed until it is used.
func public(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// Collapse v4-mapped v6 (::ffff:127.0.0.1) to its v4 form first, so the v4
	// rules below actually apply to it instead of it being judged as v6.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if !ip.IsGlobalUnicast() {
		// Covers loopback, link-local (including 169.254.169.254), multicast and
		// the unspecified address in one predicate, for both families.
		return false
	}
	if ip.IsPrivate() {
		// RFC1918 and IPv6 unique-local.
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0: // 0.0.0.0/8 "this network"
			return false
		case v4[0] == 100 && v4[1]&0xc0 == 64: // 100.64.0.0/10 CGNAT
			return false
		}
	}
	return true
}

// Fetch retrieves one URL and returns its readable content.
//
// It returns an error only when there is nothing to read — a refused address, an
// unreachable host, a non-2xx status, a body that is not a document. A page that
// is reachable but yields little content is a Page with short Markdown, because
// "this page is mostly navigation" is a true answer and a caller that treats it as
// a failure would retry forever.
func Fetch(ctx context.Context, raw string) (*Page, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("crawl: bad url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q", ErrBlocked, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("crawl: url has no host: %q", raw)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Asked for explicitly rather than left to the transport, because the transport
	// only auto-decompresses when it added the header itself.
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("crawl: %s returned %d", u.Redacted(), resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !document(ct) {
		return nil, fmt.Errorf("crawl: %s is %s, not a document", u.Redacted(), ct)
	}

	body := io.Reader(io.LimitReader(resp.Body, maxBody))
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		zr, err := gzip.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("crawl: gzip: %w", err)
		}
		defer zr.Close()
		// Re-limit AFTER decompression: the cap above bounds bytes on the wire, and
		// a small compressed body can expand without bound. Both limits are needed.
		body = io.LimitReader(zr, maxBody)
	}

	// resp.Request.URL, not u — it is the URL after redirects, which is what the
	// content is actually from and what relative links must resolve against.
	final := resp.Request.URL

	page, err := extract(body, final)
	if err != nil {
		return nil, err
	}
	page.URL = final.String()
	page.Metadata["status"] = resp.StatusCode
	page.Metadata["sourceURL"] = final.String()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		page.Metadata["contentType"] = ct
	}
	return page, nil
}

// document reports whether a Content-Type is markup this package can read. Anything
// else (an image, a PDF, an octet-stream) is refused at the header rather than fed
// to an HTML parser, which would otherwise return a Page full of binary noise.
func document(ct string) bool {
	mt := strings.ToLower(strings.TrimSpace(ct))
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	switch mt {
	case "text/html", "application/xhtml+xml", "text/plain", "application/xml", "text/xml":
		return true
	}
	return false
}
