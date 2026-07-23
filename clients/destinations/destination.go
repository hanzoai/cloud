// Package destinations is the native fan-out plane: it TRANSLATES the canonical
// /v1/event stream (clients/analytics) into each connected ad/analytics platform's
// own conversion schema and forwards it server-side — Google Analytics 4
// (Measurement Protocol) + Meta (Conversions API) end-to-end, with X/LinkedIn/
// TikTok/Reddit against the same interface.
//
// The plane is four decomplected concerns, one per file group:
//
//   - Destination interface + per-platform adapters (this file + ga4/meta/… .go):
//     an adapter renders the normalized Conversion into its platform's wire shape
//     and delivers it. Adapters self-register from init() — a new platform is a new
//     file, never a change to the fan-out.
//   - translator (translate.go): maps the canonical EVENTS vocabulary once onto the
//     normalized StandardEvent taxonomy + lifts match keys — the ONE interlingua
//     every adapter renders from.
//   - per-org registry (store.go + KMS custody in destinations.go): the connected
//     destinations + their non-secret ids (measurement/pixel), with the API secrets
//     KMS-sealed per org.
//   - fan-out consumer (fanout.go): the clients/analytics sink — for each of an
//     org's enabled destinations, translate + Send, bounded and fail-soft.
//
// SECRET CUSTODY mirrors clients/integrations: a destination's API secret lives ONLY
// in KMS (sealed, per org, at /orgs/{org}/destinations/{platform}); the store holds
// only the non-secret ids. A destination may instead ride an existing integrations
// connection's token (Meta CAPI reuses the meta_ads OAuth token) — Spec.Fallback.
package destinations

import (
	"context"
	"sort"
	"strings"
	"time"
)

// Category groups a destination on the console card.
const (
	categoryAnalytics   = "Analytics"
	categoryAdvertising = "Advertising"
)

// StandardEvent is Hanzo's normalized conversion taxonomy — the interlingua between
// the canonical EVENTS vocabulary (@hanzo/event) and each platform's own
// standard-event names. The translator maps canonical → StandardEvent ONCE
// (translate.go); each adapter maps StandardEvent → its platform's name. The empty
// value means "no standard mapping": the event is forwarded under its raw canonical
// name as a custom event.
type StandardEvent string

const (
	EventPageView      StandardEvent = "page_view"
	EventViewContent   StandardEvent = "view_content"
	EventSearch        StandardEvent = "search"
	EventLead          StandardEvent = "lead"
	EventSignUp        StandardEvent = "sign_up"
	EventStartCheckout StandardEvent = "start_checkout"
	EventAddToCart     StandardEvent = "add_to_cart"
	EventPurchase      StandardEvent = "purchase"
	EventContact       StandardEvent = "contact"
	EventCustom        StandardEvent = "" // forwarded under the raw canonical name
)

// Field is one NON-SECRET config input a destination needs (a measurement or pixel
// id). It drives the connect contract and the console card. Key is the camelCase
// key on both the connect body and the stored config.
type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Example  string `json:"example,omitempty"`
}

// Spec is a destination's declared shape: the non-secret Fields it needs, the KMS
// secret names it custodies, and an optional integrations provider id to source the
// primary secret from when none is sealed locally (Meta CAPI → meta_ads token). The
// connect body key for a secret is the camelCase of its KMS name (api_secret →
// apiSecret); both forms are accepted.
type Spec struct {
	Fields   []Field  `json:"fields"`
	Secrets  []string `json:"secrets"`
	Fallback string   `json:"fallback,omitempty"`
}

// Config is an org's non-secret destination configuration — the stored ids the
// connect body fills and the fan-out passes to Send. It never contains a secret.
type Config map[string]string

func (c Config) get(k string) string { return strings.TrimSpace(c[k]) }

// UserData is the raw match-key set the translator lifts from an event. Adapters
// SHA-256 the PII fields (email/phone/externalId) before send (advanced matching);
// click ids, ip, and user agent ride per each platform's contract. NOTHING here is
// stored — it is built per batch, used to render the outbound payload, and dropped.
type UserData struct {
	Email      string
	Phone      string
	ExternalID string
	IP         string
	UserAgent  string
	FBP        string            // Meta browser-id cookie (_fbp), if present
	Clicks     map[string]string // fbclid|gclid|ttclid|twclid|rdt_cid|li_fat_id|msclkid → value
}

// click reads a click-id by key ("" if absent).
func (u UserData) click(key string) string {
	if u.Clicks == nil {
		return ""
	}
	return strings.TrimSpace(u.Clicks[key])
}

// Conversion is one canonical event translated to the normalized model every adapter
// renders. Standard is the normalized type (EventCustom ⇒ forward Name raw); EventID
// is the dedup id shared between a browser pixel and the server CAPI event.
type Conversion struct {
	Standard   StandardEvent
	Name       string // raw canonical event name (order_completed, …)
	EventID    string // dedup id (pixel <-> CAPI)
	Time       time.Time
	Value      float64
	Currency   string
	User       UserData
	URL        string
	Properties map[string]any
}

// Result is a Send outcome: how many events the platform accepted. Some APIs do not
// report a count; on a 2xx those set Sent to the batch length.
type Result struct {
	Sent    int    `json:"sent"`
	Message string `json:"message,omitempty"`
}

// Destination is one external ad/analytics platform Hanzo forwards to. Adapters are
// pure over (Config, secret, batch): given an org's resolved non-secret config, its
// resolved credential, and the normalized conversions, Send renders the platform's
// wire shape and delivers it. An adapter reads no global state and no other tenant's
// data — the fan-out hands it exactly one org's config + credential + batch.
type Destination interface {
	ID() string       // stable slug: ga4|meta|tiktok|linkedin|x|reddit
	Name() string     // display name
	Category() string // Analytics | Advertising
	Spec() Spec
	// Send delivers batch for one org. secret is the resolved primary credential
	// (its own KMS secret, else the Spec.Fallback integrations token).
	Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error)
}

// registry is populated by each adapter file's register() from its init(). Go
// initializes the map before any init() runs, so every adapter is present by the
// time Mount snapshots it.
var registry = map[string]Destination{}

// register adds an adapter. A nil/empty/duplicate id is a programming error and
// panics at init — two adapters cannot own the same platform slug.
func register(d Destination) {
	if d == nil || d.ID() == "" {
		panic("destinations: register nil/empty destination")
	}
	if _, dup := registry[d.ID()]; dup {
		panic("destinations: duplicate destination id " + d.ID())
	}
	registry[d.ID()] = d
}

// snapshot copies the global registry into a per-mount map so a mounted service reads
// a stable set without aliasing package state.
func snapshot() map[string]Destination {
	out := make(map[string]Destination, len(registry))
	for id, d := range registry {
		out[id] = d
	}
	return out
}

// sortedIDs returns the registered platform ids in stable order.
func sortedIDs(m map[string]Destination) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
