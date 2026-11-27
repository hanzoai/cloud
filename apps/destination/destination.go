package destination

import (
	"context"
	"maps"
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
	EventViewList      StandardEvent = "view_item_list"
	EventSearch        StandardEvent = "search"
	EventLead          StandardEvent = "lead"
	EventSignUp        StandardEvent = "signup"
	EventStartCheckout StandardEvent = "start_checkout"
	EventAddToCart     StandardEvent = "add_to_cart"
	EventPurchase      StandardEvent = "purchase"
	EventContact       StandardEvent = "contact"
	EventSelect        StandardEvent = "select_item"
	EventLogin         StandardEvent = "login"
	EventCustom        StandardEvent = "" // no platform has a name for it
)

// DestinationField is one NON-SECRET config input a destination needs (a measurement or pixel
// id). It drives the connect contract and the console card. Key is the camelCase
// key on both the connect body and the stored config.
type DestinationField struct {
	Key      string `json:"key"`               // the camelCase key on both the connect body and the stored config
	Label    string `json:"label"`             // human label for the console card's input
	Required bool   `json:"required"`          // when true, a connect that leaves it empty is refused 400
	Example  string `json:"example,omitempty"` // a sample value of the right shape ("G-XXXXXXX"), when one helps
}

// Spec is a destination's declared shape: the non-secret Fields it needs, the KMS
// secret names it custodies, and an optional integrations provider id to source the
// primary secret from when none is sealed locally (Meta CAPI → meta_ads token). The
// connect body key for a secret is the camelCase of its KMS name (api_secret →
// apiSecret); both forms are accepted.
type Spec struct {
	Fields   []DestinationField `json:"fields"`
	Secrets  []string           `json:"secrets"`
	Fallback string             `json:"fallback,omitempty"`
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
	FBC        string            // Meta click cookie (_fbc): fb.1.<ms>.<fbclid>
	ClientID   string            // GA4 client id, the last two fields of the _ga cookie
	Hashed     string            // SHA-256 of the normalized email, when only the hash is known
	Clicks     map[string]string // fbclid|gclid|gbraid|wbraid|ttclid|twclid|rdt_cid|li_fat_id|msclkid → value
	Clicked    time.Time         // when the page captured the click ids; zero when it did not say
}

// em is the hashed email every platform matches on: the address hashed here, or
// the hash a touch kept when the address itself was never stored.
func (u UserData) em() string {
	if h := hashEmail(u.Email); h != "" {
		return h
	}
	return u.Hashed
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
	Source     string // the wire it arrived on; "plane" means a peer app stated it
	Person     string // the identified subject (IAM sub); "" for an anonymous visitor
	Consent    Consent
	Tags       map[string]bool // the browser tags that already saw it: "ga", "meta"
	Test       bool            // a synthetic send: validate, never record
	Sandbox    bool            // a test-mode order: sent as test traffic where a platform has one, else not sent, never as revenue
	Recurring  bool            // a renewal or auto-reload: charged with nobody present
	Vouched    bool            // admitted by the org's own secret key, which no browser holds
	Time       time.Time
	Value      float64
	Currency   string
	User       UserData
	URL        string
	Referrer   string // referring URL (a first-party analytics dimension)
	Items      []Item // ecommerce line items (nil for non-commerce events)
	Properties map[string]any
}

// Item is one normalized ecommerce line item — the interlingua between an event's raw
// items/products array (or a first-class product id) and each platform's product
// schema (GA4 items[], Meta contents[]/content_ids[]). The translator lifts it ONCE
// (liftItems); every adapter renders it into its platform's shape. An empty field is
// omitted by each renderer, so a sink only ever sees what the event actually carried.
type Item struct {
	ID       string  // SKU / product id → GA4 item_id, Meta content id
	Name     string  // → GA4 item_name
	Category string  // → GA4 item_category
	Brand    string  // → GA4 item_brand
	Variant  string  // → GA4 item_variant
	Price    float64 // unit price → GA4 price, Meta item_price
	Quantity float64 // → GA4 quantity, Meta quantity
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
	maps.Copy(out, registry)
	return out
}
