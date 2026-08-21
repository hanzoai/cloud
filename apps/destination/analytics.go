package destination

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// analytics.go forwards conversions to Hanzo Analytics (analytics.hanzo.ai) over its
// public collect contract. Config: websiteId (the site's UUID, a NON-SECRET public id,
// exactly like a GA4 measurement id) and an optional host override for an org running its
// own. There is NO API secret — collect is a public beacon keyed by the website id — so
// the fan-out treats this destination as credential-less (resolveSecret).
//
// ONE POST per conversion, because collect takes one event and derives the visitor session
// from the User-Agent and IP. The adapter forwards the END USER's UA and IP
// (X-Forwarded-For) so the session and its geo are attributed to the visitor rather than
// to this cloud. A pageview is sent WITHOUT a name, which is how collect tells a pageview
// from a custom event; everything else carries its canonical name.

const analyticsID = "analytics"

// analyticsHost is the default Hanzo Analytics collect host. A package var so a test points
// it at a mock and an org may override per-connection; never mutated in production.
var analyticsHost = "https://analytics.hanzo.ai"

// analyticsUA is the fallback User-Agent when an event carries no end-user agent. Analytics's
// /api/send REQUIRES a User-Agent (it hashes UA + IP into the daily session id).
const analyticsUA = "hanzo-cloud/1.0 (+https://hanzo.ai)"

type analytics struct{}

func init() { register(analytics{}) }

func (analytics) ID() string       { return analyticsID }
func (analytics) Name() string     { return "Hanzo Analytics" }
func (analytics) Category() string { return categoryAnalytics }

func (analytics) Spec() Spec {
	return Spec{
		Fields: []DestinationField{
			{Key: "websiteId", Label: "Website ID", Required: true, Example: "b1e2c3d4-5678-90ab-cdef-1234567890ab"},
			{Key: "host", Label: "Host (optional, self-hosted)", Required: false, Example: analyticsHost},
		},
		// No Secrets, no Fallback: /api/send is a public, website-id-keyed beacon.
	}
}

// analyticsEnvelope is Analytics's fixed collect body: {type, payload}. Exposed to tests as the
// pure render of one conversion.
type analyticsEnvelope struct {
	Type    string           `json:"type"`
	Payload analyticsPayload `json:"payload"`
}

type analyticsPayload struct {
	Website    string         `json:"website"`
	Name       string         `json:"name,omitempty"` // omitted ⇒ Analytics records a pageview
	Hostname   string         `json:"hostname,omitempty"`
	URL        string         `json:"url,omitempty"`
	Referrer   string         `json:"referrer,omitempty"`
	DistinctID string         `json:"id,omitempty"` // Analytics keys the session on `id`: sessionId = id ? uuid(website,id) : uuid(website,ip,ua,salt)
	Timestamp  int64          `json:"timestamp,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

// analyticsBuild renders one conversion into the collect envelope. Pure — tests assert the
// pageview-has-no-name rule, the website id, the hostname/url split, and the commerce
// data. A pageview ($pageview / EventPageView) is sent WITHOUT a name so Analytics records
// it as a pageview, not a custom event.
func analyticsBuild(cfg Config, cv Conversion) analyticsEnvelope {
	p := analyticsPayload{
		Website:    cfg.get("websiteId"),
		DistinctID: strings.TrimSpace(cv.User.ExternalID),
		Referrer:   strings.TrimSpace(cv.Referrer),
		Data:       analyticsData(cv),
	}
	if !cv.Time.IsZero() {
		p.Timestamp = cv.Time.Unix()
	}
	if cv.Standard != EventPageView {
		p.Name = analyticsName(cv.Name)
	}
	p.Hostname, p.URL = splitURL(cv.URL)
	return analyticsEnvelope{Type: "event", Payload: p}
}

// analyticsName strips the reserved $ prefix from a canonical name (Analytics event names are
// plain) and bounds it to Analytics's 50-char event-name limit.
func analyticsName(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	if len(s) > 50 {
		return s[:50]
	}
	return s
}

// splitURL splits a URL into (hostname, path+query). A relative or unparseable value
// yields ("", raw) so Analytics still records the path.
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

func (analytics) Send(ctx context.Context, cfg Config, _ string, batch []Conversion) (Result, error) {
	website := cfg.get("websiteId")
	if website == "" {
		return Result{}, fmt.Errorf("analytics: websiteId is required")
	}
	endpoint := analyticsEndpoint(cfg)
	sent := 0
	for _, cv := range batch {
		headers := map[string]string{"User-Agent": analyticsAgent(cv)}
		if ip := strings.TrimSpace(cv.User.IP); ip != "" {
			headers["X-Forwarded-For"] = ip
		}
		// /api/send returns a text token on 200; out=nil discards the body.
		if err := postJSON(ctx, analyticsID, endpoint, headers, analyticsBuild(cfg, cv), nil); err != nil {
			return Result{Sent: sent}, err
		}
		sent++
	}
	return Result{Sent: sent}, nil
}

// analyticsEndpoint is the collect URL: the per-connection host override, else the default
// Hanzo Analytics host, plus the collect path.
func analyticsEndpoint(cfg Config) string {
	host := cfg.get("host")
	if host == "" {
		host = analyticsHost
	}
	return strings.TrimRight(host, "/") + "/api/send"
}

// analyticsAgent is the end-user User-Agent Analytics hashes into the session, falling back to
// the cloud's own agent when the event carried none (Analytics rejects an absent UA).
func analyticsAgent(cv Conversion) string {
	if ua := strings.TrimSpace(cv.User.UserAgent); ua != "" {
		return ua
	}
	return analyticsUA
}
