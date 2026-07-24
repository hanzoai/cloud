package destinations

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// posthog.go forwards conversions to Hanzo Insights (insights.hanzo.ai) — our PostHog
// fork — via its capture contract. Config: an optional host override; the project write
// key (api_key, e.g. phc_…) is the Secret, resolved from KMS and carried in the BODY
// (never the URL/header/log). The batch endpoint /batch sends the whole batch in one
// request: {api_key, batch:[{event, distinct_id, properties, timestamp}, …]}.
//
// PostHog keys every event on distinct_id; a conversion with no resolvable visitor id is
// dropped (PostHog rejects an empty distinct_id), mirroring GA4's client_id rule. Event
// names are the RAW canonical Hanzo names (order_completed, …): a product-analytics sink
// records the native vocabulary, unlike the ad adapters' conversion taxonomy.

const posthogID = "posthog"

// posthogHost is the default Hanzo Insights host. A package var so a test points it at a
// mock and an org may override per-connection; never mutated in production.
var posthogHost = "https://insights.hanzo.ai"

type posthog struct{}

func init() { register(posthog{}) }

func (posthog) ID() string       { return posthogID }
func (posthog) Name() string     { return "Hanzo Insights (PostHog)" }
func (posthog) Category() string { return categoryAnalytics }

func (posthog) Spec() Spec {
	return Spec{
		Fields: []Field{
			{Key: "host", Label: "Host (optional, self-hosted)", Required: false, Example: posthogHost},
		},
		Secrets: []string{"api_key"},
	}
}

// posthogBatch is the /batch capture body: the project key + the events. Exposed to
// tests as the pure render of a batch.
type posthogBatch struct {
	APIKey string         `json:"api_key"`
	Batch  []posthogEvent `json:"batch"`
}

type posthogEvent struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties,omitempty"`
	Timestamp  string         `json:"timestamp,omitempty"`
}

// posthogBuild renders the batch into the capture body. Pure — tests assert the api_key
// placement, distinct_id, event name, and commerce properties. A conversion with no
// visitor id is dropped (PostHog needs a distinct_id).
func posthogBuild(apiKey string, batch []Conversion) posthogBatch {
	events := make([]posthogEvent, 0, len(batch))
	for _, cv := range batch {
		distinct := strings.TrimSpace(cv.User.ExternalID)
		if distinct == "" {
			continue
		}
		e := posthogEvent{
			Event:      posthogName(cv.Name),
			DistinctID: distinct,
			Properties: posthogProps(cv),
		}
		if !cv.Time.IsZero() {
			e.Timestamp = cv.Time.UTC().Format(time.RFC3339)
		}
		events = append(events, e)
	}
	return posthogBatch{APIKey: apiKey, Batch: events}
}

// posthogName is the raw canonical event name; a $pageview stays $pageview (PostHog's
// own reserved pageview name — the two vocabularies already agree).
func posthogName(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return "$event"
}

// posthogProps builds the event properties: the normalized commerce data plus the
// PostHog-native $current_url. Non-PII by construction (the match keys the translator
// lifted for ad matching are NOT forwarded to a first-party sink). nil when neither is
// present.
func posthogProps(cv Conversion) map[string]any {
	props := analyticsData(cv)
	if props == nil {
		props = map[string]any{}
	}
	if cv.URL != "" {
		props["$current_url"] = cv.URL
	}
	if len(props) == 0 {
		return nil
	}
	return props
}

func (d posthog) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	if strings.TrimSpace(secret) == "" {
		return Result{}, fmt.Errorf("posthog: api_key is required")
	}
	body := posthogBuild(secret, batch)
	if len(body.Batch) == 0 {
		return Result{}, nil
	}
	host := cfg.get("host")
	if host == "" {
		host = posthogHost
	}
	endpoint := strings.TrimRight(host, "/") + "/batch"
	// PostHog capture returns {status:1} (or 1) on 200; out=nil discards the body.
	if err := postJSON(ctx, posthogID, endpoint, nil, body, nil); err != nil {
		return Result{}, err
	}
	return Result{Sent: len(body.Batch)}, nil
}
