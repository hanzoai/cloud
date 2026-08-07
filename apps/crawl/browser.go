package crawl

// Escalation to a real browser.
//
// Fetch is one http.Get. That is right for most of the web and wrong for the
// part of it that renders client-side: the response for a single-page app is a
// near-empty shell, extract finds almost no text, and the caller gets 200 with
// nothing in it. A crawl that returns nothing and says it succeeded is worse
// than one that fails, because nothing upstream can tell.
//
// So: static first, and if what came back is too thin to be the page, ask Hanzo
// Crawl (headless Chromium, ghcr.io/hanzoai/crawl) for the rendered version. It
// runs at crawl.hanzo.svc:11235 — the address browserEndpoint returns — and it
// requires the bearer token below.
//
// Escalation is one-way and best-effort: if the browser is slow or unhappy, the
// static Page still stands. That is a property worth keeping even though the
// service is up, because "the render failed" must never turn a page we already
// have into an error.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// service is the client used to reach the crawl SERVICE, and it deliberately does
// NOT carry the guarded dialer that `client` uses.
//
// The guard refuses non-public addresses, which is right for a URL a caller
// supplied and wrong for this one: the browser lives at crawl.hanzo.svc, an
// in-cluster name that resolves to a private address by design. Sending this
// through `client` meant every escalation was refused with ErrBlocked, so the
// feature could never have worked in production — caught by a test that pointed
// it at 127.0.0.1 and got the refusal instead of a render.
//
// Two different trust classes, two clients: `client` dials wherever a caller
// asked, so it is guarded; `service` dials one address WE configured.
var service = &http.Client{Timeout: browserTimeout}

// resolve is the name lookup reachable uses, as a var so a test can supply an
// answer instead of depending on live DNS. Production keeps the real resolver;
// this exists because the address check is a security boundary and a boundary
// you cannot test with a hostile answer is one you are only assuming holds.
var resolve = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// thinText is the markdown length under which a document is treated as not
// really the page. Deliberately small: the cost of guessing wrong is one extra
// request, and the cost of NOT escalating is silent data loss, so this errs
// toward asking. A real article clears it by an order of magnitude; an app shell
// (a <div id="root"> plus script tags) does not come close.
const thinText = 512

// browserTimeout bounds the escalation. Rendering is seconds, not milliseconds,
// but the caller is a request — so a browser having a bad day must not hold it
// open. Exceeded means "keep the static Page", never an error to the caller.
const browserTimeout = 45 * time.Second

// browserEndpoint is the in-cluster crawl service. Same host:port ai already
// dials, so the two agree on where the browser lives without a shared constant
// across repos.
func browserEndpoint() string {
	if v := strings.TrimSpace(os.Getenv("CRAWL_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://crawl.hanzo.svc:11235"
}

// thin reports whether a Page looks like an unrendered shell rather than the
// document. Text length is the whole test on purpose: "does this have a
// <div id=root>" identifies today's frameworks and misses tomorrow's, whereas
// "the extractor found almost nothing" is the symptom itself and does not date.
func thin(p *Page) bool {
	return p == nil || len(strings.TrimSpace(p.Markdown)) < thinText
}

// markdown decodes the service's `markdown`, which is shape-polymorphic: a bare
// string on some builds, an object with fit_markdown/raw_markdown on others.
// fit_markdown is the boilerplate-stripped one, so it wins when present.
type markdown string

func (m *markdown) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*m = markdown(s)
		return nil
	}
	var obj struct {
		FitMarkdown string `json:"fit_markdown"`
		RawMarkdown string `json:"raw_markdown"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	if obj.FitMarkdown != "" {
		*m = markdown(obj.FitMarkdown)
	} else {
		*m = markdown(obj.RawMarkdown)
	}
	return nil
}

type browserResult struct {
	URL      string                 `json:"url"`
	Success  bool                   `json:"success"`
	Markdown markdown               `json:"markdown"`
	Metadata map[string]interface{} `json:"metadata"`
}

// browserResponse carries results inline; the deployed service answers /crawl
// synchronously with a boolean success and no task id.
type browserResponse struct {
	Success bool            `json:"success"`
	Results []browserResult `json:"results"`
}

// reachable applies the SAME address check the guarded dialer applies, to a URL
// we are about to hand to something that has no such check.
//
// This closes a real hole rather than a theoretical one. Read escalates when a
// static Fetch FAILS, and one reason it fails is the guard refusing an internal
// address. Without this, "crawl http://10.0.0.1/" would be refused by the dialer
// and then politely forwarded to a headless Chromium that fetches it happily —
// escalation as an SSRF bypass, reachable by anyone who can call /v1/crawl.
//
// Resolve-then-check mirrors the dialer, including refusing a host that answers
// with ANY internal address, since one public IP listed beside the target is the
// documented way around a check that only looks at the first answer.
func reachable(ctx context.Context, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("crawl: bad url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrBlocked, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("crawl: url has no host: %q", raw)
	}
	ips, err := resolve(ctx, host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if !public(ip.IP) {
			return fmt.Errorf("%w: %s resolves to %s", ErrBlocked, host, ip.IP)
		}
	}
	return nil
}

// browse asks the browser for one URL. An error here is never fatal to a crawl —
// every caller falls back to the static Page.
func browse(ctx context.Context, raw string) (*Page, error) {
	if err := reachable(ctx, raw); err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"urls":           []string{raw},
		"browser_config": map[string]any{"headless": true},
		"crawler_params": map[string]any{
			"word_count_threshold":   1,
			"exclude_external_links": false,
			"process_iframes":        true,
		},
	})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, browserTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, browserEndpoint()+"/crawl", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The service REQUIRES this: unauthenticated it answers
	// {"detail":"Authentication required"}, so a missing token is not a degraded
	// render, it is no render at all. It reaches both pods from KMS as the
	// crawl-secrets/CRAWL_API_TOKEN key.
	//
	// Sent when present rather than demanded up front because the failure is
	// already well reported — the request returns a non-2xx, escalate() keeps the
	// static Page, and websearch counts the engine blind. One boundary, one
	// refusal, read in one place.
	if tok := strings.TrimSpace(os.Getenv("CRAWL_API_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := service.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("crawl: browser returned %d", resp.StatusCode)
	}

	var out browserResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Results) == 0 {
		return nil, fmt.Errorf("crawl: browser returned no results")
	}
	r := out.Results[0]
	if !r.Success {
		return nil, fmt.Errorf("crawl: browser could not render %s", raw)
	}

	md := strings.TrimSpace(string(r.Markdown))
	if md == "" {
		return nil, fmt.Errorf("crawl: browser rendered %s to nothing", raw)
	}

	meta := map[string]interface{}{}
	for k, v := range r.Metadata {
		meta[k] = v
	}
	meta["renderer"] = "browser"
	page := &Page{URL: raw, Markdown: md, Metadata: meta}
	if r.URL != "" {
		page.URL = r.URL
		meta["sourceURL"] = r.URL
	}
	return page, nil
}

// escalate returns the better of a static Page and a rendered one.
//
// "Better" is longer text, not "the browser answered". A render can come back
// thinner than the static fetch — a consent wall, a bot check, a page that needed
// no JS at all — and taking it on faith would make escalation a downgrade.
func escalate(ctx context.Context, static *Page, raw string) *Page {
	if !thin(static) {
		return static
	}
	rendered, err := browse(ctx, raw)
	if err != nil || rendered == nil {
		return static
	}
	if static != nil && len(rendered.Markdown) <= len(strings.TrimSpace(static.Markdown)) {
		return static
	}
	return rendered
}
