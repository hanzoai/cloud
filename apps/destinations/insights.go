package destinations

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// insights.go forwards conversions to Hanzo Insights (insights.hanzo.ai). Config: an
// optional host override; the project write key (api_key, hi_…) is the Secret, resolved
// from KMS and carried in the BODY, never the URL, a header or a log. The ingest door
// /v1/e takes the whole batch in one request:
//
//	{api_key, batch: [{event, distinct_id, properties, timestamp}, …]}
//
// Every event is keyed on distinct_id, so a conversion with no resolvable visitor id is
// dropped rather than filed against nobody — the same rule GA4's client_id follows. Event
// names stay the RAW canonical Hanzo names (order_completed, …): a product-analytics sink
// records the native vocabulary, where an ad platform gets its own conversion taxonomy.

const insightsID = "insights"

// insightsHost is the default Hanzo Insights host. A package var so a test points it at a
// mock and an org may override per-connection; never mutated in production.
var insightsHost = "https://insights.hanzo.ai"

type insights struct{}

func init() { register(insights{}) }

func (insights) ID() string       { return insightsID }
func (insights) Name() string     { return "Hanzo Insights" }
func (insights) Category() string { return categoryAnalytics }

func (insights) Spec() Spec {
	return Spec{
		Fields: []DestinationField{
			{Key: "host", Label: "Host (optional, self-hosted)", Required: false, Example: insightsHost},
		},
		Secrets: []string{"api_key"},
	}
}

// insightsBatch is the /v1/e capture body: the project key + the events. Exposed to
// tests as the pure render of a batch.
type insightsBatch struct {
	APIKey string          `json:"api_key"`
	Batch  []insightsEvent `json:"batch"`
}

type insightsEvent struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties,omitempty"`
	Timestamp  string         `json:"timestamp,omitempty"`
}

// insightsBuild renders the batch into the capture body. Pure — tests assert the api_key
// placement, distinct_id, event name, and commerce properties. A conversion with no
// visitor id is dropped (Insights needs a distinct_id).
func insightsBuild(apiKey string, batch []Conversion) insightsBatch {
	events := make([]insightsEvent, 0, len(batch))
	for _, cv := range batch {
		distinct := strings.TrimSpace(cv.User.ExternalID)
		if distinct == "" {
			continue
		}
		e := insightsEvent{
			Event:      insightsName(cv.Name),
			DistinctID: distinct,
			Properties: insightsProps(cv),
		}
		if !cv.Time.IsZero() {
			e.Timestamp = cv.Time.UTC().Format(time.RFC3339)
		}
		events = append(events, e)
	}
	return insightsBatch{APIKey: apiKey, Batch: events}
}

// insightsName is the raw canonical event name; a $pageview stays $pageview (Insights's
// own reserved pageview name — the two vocabularies already agree).
func insightsName(s string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return "$event"
}

// insightsProps builds the event properties: the normalized commerce data plus the
// Insights-native $current_url. Non-PII by construction (the match keys the translator
// lifted for ad matching are NOT forwarded to a first-party sink). nil when neither is
// present.
func insightsProps(cv Conversion) map[string]any {
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

func (d insights) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	if strings.TrimSpace(secret) == "" {
		return Result{}, fmt.Errorf("insights: api_key is required")
	}
	body := insightsBuild(secret, batch)
	if len(body.Batch) == 0 {
		return Result{}, nil
	}
	host := cfg.get("host")
	if host == "" {
		host = insightsHost
	}
	endpoint := strings.TrimRight(host, "/") + "/v1/e"
	// Insights capture returns {status:1} (or 1) on 200; out=nil discards the body.
	if err := postJSON(ctx, insightsID, endpoint, nil, body, nil); err != nil {
		return Result{}, err
	}
	return Result{Sent: len(body.Batch)}, nil
}
