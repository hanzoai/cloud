package destinations

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// umami.go forwards conversions to Hanzo Analytics (analytics.hanzo.ai) — our Umami
// fork — via its fixed public collect contract POST /api/send. Config: websiteId (the
// Umami website UUID, a NON-SECRET public id, exactly like a GA4 measurement id) and an
// optional host override (an org that self-hosts the fork). There is NO API secret:
// /api/send is an unauthenticated public beacon keyed by the website id, so the fan-out
// treats this Secret-less, Fallback-less destination as credential-less (resolveSecret).
//
// One POST per conversion (Umami's collect takes ONE event and derives the visitor
// session from the end-user User-Agent + IP): the adapter forwards the END USER's UA
// (User-Agent header) and IP (X-Forwarded-For) so Umami attributes the session and geo
// correctly, never the cloud's own. A pageview is sent WITHOUT a name (Umami's pageview
// vs. custom-event distinction); every other event carries its canonical name.

const umamiID = "analytics"

// umamiHost is the default Hanzo Analytics collect host. A package var so a test points
// it at a mock and an org may override per-connection; never mutated in production.
var umamiHost = "https://analytics.hanzo.ai"

// umamiUA is the fallback User-Agent when an event carries no end-user agent. Umami's
// /api/send REQUIRES a User-Agent (it hashes UA + IP into the daily session id).
const umamiUA = "hanzo-cloud/1.0 (+https://hanzo.ai)"

type umami struct{}

func init() { register(umami{}) }

func (umami) ID() string       { return umamiID }
func (umami) Name() string     { return "Hanzo Analytics" }
func (umami) Category() string { return categoryAnalytics }

func (umami) Spec() Spec {
	return Spec{
		Fields: []Field{
			{Key: "websiteId", Label: "Website ID", Required: true, Example: "b1e2c3d4-5678-90ab-cdef-1234567890ab"},
			{Key: "host", Label: "Host (optional, self-hosted)", Required: false, Example: umamiHost},
		},
		// No Secrets, no Fallback: /api/send is a public, website-id-keyed beacon.
	}
}

// umamiEnvelope is Umami's fixed collect body: {type, payload}. Exposed to tests as the
// pure render of one conversion.
type umamiEnvelope struct {
	Type    string       `json:"type"`
	Payload umamiPayload `json:"payload"`
}

type umamiPayload struct {
	Website    string         `json:"website"`
	Name       string         `json:"name,omitempty"` // omitted ⇒ Umami records a pageview
	Hostname   string         `json:"hostname,omitempty"`
	URL        string         `json:"url,omitempty"`
	Referrer   string         `json:"referrer,omitempty"`
	DistinctID string         `json:"id,omitempty"` // Umami keys the session on `id`: sessionId = id ? uuid(website,id) : uuid(website,ip,ua,salt)
	Timestamp  int64          `json:"timestamp,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

// umamiBuild renders one conversion into the collect envelope. Pure — tests assert the
// pageview-has-no-name rule, the website id, the hostname/url split, and the commerce
// data. A pageview ($pageview / EventPageView) is sent WITHOUT a name so Umami records
// it as a pageview, not a custom event.
func umamiBuild(cfg Config, cv Conversion) umamiEnvelope {
	p := umamiPayload{
		Website:    cfg.get("websiteId"),
		DistinctID: strings.TrimSpace(cv.User.ExternalID),
		Referrer:   strings.TrimSpace(cv.Referrer),
		Data:       analyticsData(cv),
	}
	if !cv.Time.IsZero() {
		p.Timestamp = cv.Time.Unix()
	}
	if cv.Standard != EventPageView {
		p.Name = umamiName(cv.Name)
	}
	p.Hostname, p.URL = splitURL(cv.URL)
	return umamiEnvelope{Type: "event", Payload: p}
}

// umamiName strips the reserved $ prefix from a canonical name (Umami event names are
// plain) and bounds it to Umami's 50-char event-name limit.
func umamiName(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	if len(s) > 50 {
		return s[:50]
	}
	return s
}

// splitURL splits a URL into (hostname, path+query). A relative or unparseable value
// yields ("", raw) so Umami still records the path.
func splitURL(raw string) (host, path string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", raw
	}
	path = u.Path
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return u.Host, path
}

func (d umami) Send(ctx context.Context, cfg Config, _ string, batch []Conversion) (Result, error) {
	website := cfg.get("websiteId")
	if website == "" {
		return Result{}, fmt.Errorf("analytics: websiteId is required")
	}
	endpoint := umamiEndpoint(cfg)
	sent := 0
	for _, cv := range batch {
		headers := map[string]string{"User-Agent": umamiAgent(cv)}
		if ip := strings.TrimSpace(cv.User.IP); ip != "" {
			headers["X-Forwarded-For"] = ip
		}
		// /api/send returns a text token on 200; out=nil discards the body.
		if err := postJSON(ctx, umamiID, endpoint, headers, umamiBuild(cfg, cv), nil); err != nil {
			return Result{Sent: sent}, err
		}
		sent++
	}
	return Result{Sent: sent}, nil
}

// umamiEndpoint is the collect URL: the per-connection host override, else the default
// Hanzo Analytics host, + Umami's fixed /api/send path (an EXTERNAL contract, verbatim).
func umamiEndpoint(cfg Config) string {
	host := cfg.get("host")
	if host == "" {
		host = umamiHost
	}
	return strings.TrimRight(host, "/") + "/api/send"
}

// umamiAgent is the end-user User-Agent Umami hashes into the session, falling back to
// the cloud's own agent when the event carried none (Umami rejects an absent UA).
func umamiAgent(cv Conversion) string {
	if ua := strings.TrimSpace(cv.User.UserAgent); ua != "" {
		return ua
	}
	return umamiUA
}
