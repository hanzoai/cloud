package crawl

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/botauth"
	"github.com/hanzoai/outbound"
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

// client is process-wide so connections pool across fetches. It is the outbound
// client: every address it dials — every redirect hop included — is judged on the
// resolved IP, and a refusal is outbound.ErrRefused, distinguishable from a site
// that was merely down.
//
// Every request it sends, each redirect hop included, carries the deployment's
// Web Bot Auth signature (sdk/botauth, hanzo-inc/cloud#423), so a site can tell a
// Hanzo agent from anything else that sends this User-Agent. The signature names
// the bot; the desktop User-Agent stays, because a site that does not read the
// signature still answers a datacenter IP that looks automated with a challenge.
var client = botauth.Client(outbound.Client(outbound.Options{Timeout: fetchTimeout, Redirects: maxRedirects}))

// Fetch retrieves one URL and returns its readable content.
//
// It returns an error only when there is nothing to read — a refused address, an
// unreachable host, a non-2xx status, a body that is not a document. A page that
// is reachable but yields little content is a Page with short Markdown, because
// "this page is mostly navigation" is a true answer and a caller that treats it as
// a failure would retry forever.
func Fetch(ctx context.Context, raw string) (*Page, error) {
	resp, u, err := open(ctx, raw)
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

// open sends the GET every read of a URL begins with, through the guarded client,
// and answers the response and the URL as asked. Redirects are followed inside
// client.Do, each hop judged by the dialer, so a response that arrives has been
// admitted all the way down its chain; one that does not is outbound.ErrRefused.
func open(ctx context.Context, raw string) (*http.Response, *url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, nil, fmt.Errorf("crawl: bad url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, nil, fmt.Errorf("%w: scheme %q", outbound.ErrRefused, u.Scheme)
	}
	if u.Host == "" {
		return nil, nil, fmt.Errorf("crawl: url has no host: %q", raw)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Asked for explicitly rather than left to the transport, because the transport
	// only auto-decompresses when it added the header itself.
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	return resp, u, nil
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
