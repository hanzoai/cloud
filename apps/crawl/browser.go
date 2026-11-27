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
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/botauth"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/outbound"

	"github.com/hanzoai/cloud/sdk"
)

// service is the client used to reach the crawl SERVICE, and it deliberately does
// NOT carry the guarded dialer that `client` uses.
//
// The guard refuses non-public addresses, which is right for a URL a caller
// supplied and wrong for this one: the browser lives at crawl.hanzo.svc, an
// in-cluster name that resolves to a private address by design. Sending this
// through `client` meant every escalation was refused, so the feature could
// never have worked in production — caught by a test that pointed it at
// 127.0.0.1 and got the refusal instead of a render.
//
// Two different trust classes, two clients: `client` dials wherever a caller
// asked, so it is guarded; `service` dials one address WE configured.
var service = &http.Client{Timeout: browserTimeout}

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

// browserMaxBody caps the service's response, and it is NOT maxBody for the same
// reason `service` is not `client`: that cap bounds a page an outside caller
// pointed us at, this one bounds an envelope from a service we configured. The
// envelope wraps the page — markdown plus metadata plus the render's own
// bookkeeping — so it is structurally larger than the document it carries, and a
// page well within maxBody can arrive here past it. Truncated JSON does not
// degrade, it fails to decode, so an undersized cap turns exactly the pages worth
// escalating into "the render failed" with nothing anywhere saying why.
const browserMaxBody = 64 << 20

// browserEndpoint is the in-cluster crawl service. Same host:port ai already
// dials, so the two agree on where the browser lives without a shared constant
// across repos.
func browserEndpoint() string {
	if v := env.Or("CRAWL_URL", ""); v != "" {
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

// browserResult is one rendered page. A render that asked for a screenshot also
// carries the picture, the page's DOM and its links; `links` stays raw because
// its shape is the renderer's and only a caller that wants it reads it.
type browserResult struct {
	URL         string          `json:"url"`
	Success     bool            `json:"success"`
	Markdown    markdown        `json:"markdown"`
	Metadata    map[string]any  `json:"metadata"`
	Screenshot  string          `json:"screenshot"`
	CleanedHTML string          `json:"cleaned_html"`
	Links       json.RawMessage `json:"links"`
}

// browserResponse carries results inline; the deployed service answers /crawl
// synchronously with a boolean success and no task id.
type browserResponse struct {
	Success bool            `json:"success"`
	Results []browserResult `json:"results"`
}

// reachable refuses a URL whose own host is not public, before a render is paid
// for. It judges the first host only: the browser follows redirects, frames and
// scripts to hosts this cannot see, so it does not bound the browser. The
// browser's pod does, with an egress policy that admits only public addresses
// (universe charts/app/values/hanzo/crawl.yaml).
//
// outbound.Host refuses a host that answers with ANY non-public address, since
// one public IP listed beside the target is the documented way around a check
// that only looks at the first answer, and the browser picks which one it dials.
func reachable(ctx context.Context, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("crawl: bad url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", outbound.ErrRefused, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("crawl: url has no host: %q", raw)
	}
	return outbound.Host(ctx, host)
}

// signatures is the Web Bot Auth signature for each host a render of raw may start
// at (raw's and each landed URL's), keyed by the @authority a verifier reads. The
// renderer puts one on the main-frame navigation to its host and nowhere else
// (crawl-v1 crawl/signature.py). A host that cannot be signed goes unsigned.
func signatures(ctx context.Context, raw string, landed ...string) map[string]map[string]string {
	s := botauth.Default()
	if s == nil {
		return nil
	}
	out := map[string]map[string]string{}
	for _, v := range append([]string{raw}, landed...) {
		u, err := url.Parse(strings.TrimSpace(v))
		if err != nil || u.Host == "" {
			continue
		}
		auth := botauth.AuthorityOf(u)
		if _, done := out[auth]; done {
			continue
		}
		h, _, err := s.SignFor(ctx, u.Scheme, auth)
		if err != nil {
			continue
		}
		out[auth] = map[string]string{
			"Signature-Agent": h.Get("Signature-Agent"),
			"Signature-Input": h.Get("Signature-Input"),
			"Signature":       h.Get("Signature"),
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// browse asks the browser for one URL and answers the page as markdown. An error
// here is never fatal to a crawl — every caller falls back to the static Page.
// landed is where the static fetch ended, if there was one: the browser may start
// there too, and is given a signature for that host as well.
func browse(ctx context.Context, raw string, landed ...string) (*Page, error) {
	r, err := visit(ctx, raw, nil, func(r *browserResult) error {
		if strings.TrimSpace(string(r.Markdown)) == "" {
			return fmt.Errorf("crawl: browser rendered %s to nothing", raw)
		}
		return nil
	}, landed...)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{}
	maps.Copy(meta, r.Metadata)
	meta["renderer"] = "browser"
	page := &Page{URL: raw, Markdown: strings.TrimSpace(string(r.Markdown)), Metadata: meta}
	if r.URL != "" {
		page.URL = r.URL
		meta["sourceURL"] = r.URL
	}
	return page, nil
}

// visit asks the browser for one URL, with `config` added to the run's
// crawler_config, and answers what it drew.
//
// It is also where the render is paid for. This one function is the whole of the
// escalation and of the render op: every caller reaches the browser through it
// and none can reach it any other way, so gating and debiting here cannot drift
// from what was actually rendered. See meter.go. `usable` says what a render must
// hold to be worth its fee; a render that fails it is an error and is not charged.
func visit(ctx context.Context, raw string, config map[string]any, usable func(*browserResult) error, landed ...string) (*browserResult, error) {
	if err := reachable(ctx, raw); err != nil {
		return nil, err
	}
	ch, err := afford(ctx, cloud.PayerOf(ctx))
	if err != nil {
		return nil, err
	}
	// Give the hold back on every exit; settling takes it, this covers the rest.
	defer ch.Release()
	run := map[string]any{}
	maps.Copy(run, config)
	if sigs := signatures(ctx, raw, landed...); sigs != nil {
		run["request_signatures"] = sigs
	}
	payload := map[string]any{
		"urls":           []string{raw},
		"browser_config": map[string]any{"headless": true},
		"crawler_params": map[string]any{
			"word_count_threshold":   1,
			"exclude_external_links": false,
			"process_iframes":        true,
		},
	}
	if len(run) > 0 {
		payload["crawler_config"] = run
	}
	body, err := json.Marshal(payload)
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
	if tok := env.Or("CRAWL_API_TOKEN", ""); tok != "" {
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, browserMaxBody)).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Results) == 0 {
		return nil, fmt.Errorf("crawl: browser returned no results")
	}
	r := &out.Results[0]
	if !r.Success {
		return nil, fmt.Errorf("crawl: browser could not render %s", raw)
	}
	if err := usable(r); err != nil {
		return nil, err
	}

	charge(ch)
	return r, nil
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
	var landed []string
	if static != nil && static.URL != "" {
		landed = append(landed, static.URL)
	}
	rendered, err := browse(ctx, raw, landed...)
	if err != nil || rendered == nil {
		return static
	}
	if static != nil && len(rendered.Markdown) <= len(strings.TrimSpace(static.Markdown)) {
		return static
	}
	return rendered
}
