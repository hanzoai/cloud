// bridge.go — the ingest→bus half of the platform event spine.
//
// ONE global standard, end to end: every event enters through the ONE door
// (POST /v1/event — product, team, LLM-obs by shape), the write core commits it,
// and the fan-out seam (analytics.AddSink) hands the accepted batch HERE, where
// it is published onto the ONE platform bus (the embedded Hanzo PubSub the
// commerce plane and the Kafka facade already ride) as stream EVENTS, subject
//
//	event.<kind>        kind = the event's canonical name as a NATS-safe token
//	                    ($pageview → event.pageview, $error → event.error,
//	                    signup_completed → event.signup_completed — the
//	                    @hanzo/event grammar: <object>_<verb-past>)
//
// carrying the Envelope below — organization_id first, because the delivery
// engine (dispatch.go orgOf) resolves the tenant from the envelope and an
// event without an org is delivered to nobody. The SAME dispatcher that fans
// commerce.> to org webhooks consumes event.> too, so "subscribe my endpoint
// to signups and errors" is one Endpoint row with patterns like
// ["event.error", "event.signed_up"] — no second delivery system, no second
// bus, no second envelope.
//
// FAIL-SOFT both ways: the sink runs detached (forward.go), and a bus that is
// down publishes nothing while the warehouse copy is already durable — the bus
// is the fan-out spine, not the system of record.
package webhooks

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/analytics"
)

// EventStream is the platform-bus stream carrying the canonical event plane.
const EventStream = "EVENTS"

// EventSubjects is the subject space the stream binds.
var EventSubjects = []string{"event.>"}

// Envelope is THE event envelope on the platform bus. organization_id is
// load-bearing: the delivery engine resolves the tenant from it.
type Envelope struct {
	OrganizationID string         `json:"organization_id"`
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	DistinctID     string         `json:"distinct_id,omitempty"`
	AnonymousID    string         `json:"anonymous_id,omitempty"`
	Time           time.Time      `json:"time"`
	URL            string         `json:"url,omitempty"`
	Path           string         `json:"path,omitempty"`
	Referrer       string         `json:"referrer,omitempty"`
	Revenue        float64        `json:"revenue,omitempty"`
	Currency       string         `json:"currency,omitempty"`
	ProductID      string         `json:"product_id,omitempty"`
	Quantity       uint32         `json:"quantity,omitempty"`
	Properties     map[string]any `json:"properties,omitempty"`
}

// subjectFor maps a canonical event name onto its bus subject. Names are
// caller-chosen strings; a NATS subject token is not — so the name is folded to
// lowercase, runs of anything outside [a-z0-9_] collapse to one '_', the
// canonical '$' prefix drops, and an empty result (or one that would collide
// with wildcard grammar) lands on "custom". Bounded so a hostile name cannot
// mint unbounded subject cardinality on the stream.
func subjectFor(name string) string {
	name = strings.TrimPrefix(strings.TrimSpace(name), "$")
	var b strings.Builder
	pendingSep := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			if pendingSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingSep = false
			b.WriteRune(r)
		default:
			pendingSep = true
		}
	}
	token := b.String()
	if token == "" {
		token = "custom"
	}
	if len(token) > 48 {
		token = token[:48]
	}
	return "event." + token
}

// publishEvents is the installed analytics sink: one bus publish per accepted
// event. The dispatcher's live bus client is borrowed per batch — nil (bus
// down, or not yet connected) publishes nothing, and any publish error stops
// the batch quietly: the warehouse already holds the events, and the next
// batch retries the bus by construction.
func (d *dispatcher) publishEvents(org string, evs []analytics.SinkEvent) {
	d.mu.Lock()
	cl := d.client
	d.mu.Unlock()
	if cl == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, e := range evs {
		body, err := json.Marshal(Envelope{
			OrganizationID: org,
			ID:             e.MessageID,
			Name:           e.Name,
			DistinctID:     e.DistinctID,
			AnonymousID:    e.AnonymousID,
			Time:           e.Time,
			URL:            e.URL,
			Path:           e.Path,
			Referrer:       e.Referrer,
			Revenue:        e.Revenue,
			Currency:       e.Currency,
			ProductID:      e.ProductID,
			Quantity:       e.Quantity,
			Properties:     e.Properties,
		})
		if err != nil {
			continue
		}
		if _, err := cl.PublishToStream(ctx, subjectFor(e.Name), body); err != nil {
			return
		}
	}
}
