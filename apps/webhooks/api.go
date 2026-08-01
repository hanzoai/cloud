package webhooks

// api.go — the /v1/webhooks registry surface: an org's CRUD over its OWN webhook
// endpoints. Every handler resolves the caller's org from the VALIDATED principal
// (principal.Org — the gateway-minted X-Org-Id, HIP-0026), exactly like apps/notify,
// and never from a client-supplied body/header. An unauthenticated caller gets 401; a
// signed-in caller sees and mutates ONLY its own org's endpoints (physical per-org
// SQLite makes cross-tenant access impossible).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

const (
	// maxURL / maxDescription / maxEvents bound the fields a create/update accepts so a
	// hostile body can't amplify the shared store or a delivery.
	maxURL         = 2048
	maxDescription = 1024
	maxEvents      = 64
	maxPattern     = 256

	// deliveries list paging: default and hard cap on ?limit.
	defaultDeliveryLimit = 50
	maxDeliveryLimit     = 200

	// usageWindow is the trailing span the deliveries7d/failures7d counters cover.
	usageWindow = 7 * 24 * time.Hour

	// testSubject is the event subject a /:id/test send carries.
	testSubject = "webhook.test"
)

// Endpoint is one registered webhook subscriber. It is BOTH the wire model and the
// stored row. Secret is returned ONLY on create (json omitempty + cleared elsewhere),
// so the signing key leaves the server exactly once.
type Endpoint struct {
	ID          string   `json:"id"`
	Org         string   `json:"org"`
	URL         string   `json:"url"`
	Events      []string `json:"events"`
	Secret      string   `json:"secret,omitempty"`
	Status      string   `json:"status"`
	Description string   `json:"description,omitempty"`
	CreatedAt   string   `json:"created"`
	UpdatedAt   string   `json:"updated"`

	// Deliveries7d / Failures7d are cheap usage counters computed from the delivery log
	// over usageWindow (not stored columns) and populated ONLY on list/get. They are 0
	// when there is no delivery history — never omitempty, so the console always sees them.
	Deliveries7d int `json:"deliveries7d"`
	Failures7d   int `json:"failures7d"`
}

// DeliveryRow is one recorded delivery attempt: the /:id/deliveries wire model AND the
// stored row. One attempt-group (a single event → one endpoint) writes one row per
// attempt, all sharing a Delivery id; Status is "ok" | "retrying" | "failed". HTTPStatus
// is 0 on a network/timeout error, Error is empty on success.
type DeliveryRow struct {
	EndpointID string `json:"endpoint"`
	DeliveryID string `json:"delivery"`
	Subject    string `json:"subject"`
	Attempt    int    `json:"attempt"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"httpStatus"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
	Created    string `json:"created"`
}

// testResult is the inline outcome of a synchronous /:id/test send.
type testResult struct {
	Delivered  bool   `json:"delivered"`
	HTTPStatus int    `json:"httpStatus"`
	DurationMs int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
}

// endpointRef addresses one of the caller org's endpoints. The id is the path
// segment: the URL is the addressing authority, so it binds from there whatever
// a body says — which is also what the untyped handlers did, reading
// c.Param("id"). `json:"-"` keeps it out of the body entirely.
type endpointRef struct {
	// ID is the webhook endpoint to act on, from the path.
	ID string `json:"-" url:"id"`
}

// createEndpointIn is a new webhook subscription. Secret, id and the timestamps
// are server-owned, so they are absent here rather than present-and-ignored: a
// request property the server always discards is one a generated client should
// never offer.
//
// `url:"-"` on every field is what keeps this a BODY: zip's binder fills an In
// field from the query string as well as the body, and this route has never
// taken an endpoint's fields there — without the opt-out `?url=https://evil`
// would silently redirect where the org's events are delivered.
type createEndpointIn struct {
	// URL is the https:// address each matching event is POSTed to. Required,
	// max 2048 bytes; http:// and every other scheme is refused, because a
	// webhook carries signed event data and must not travel in the clear.
	URL string `json:"url" url:"-"`
	// Events are NATS subject patterns to subscribe to (e.g. "commerce.order.>").
	// An empty or omitted list means EVERY event on the platform bus. Max 64
	// patterns, each max 256 bytes.
	Events []string `json:"events" url:"-"`
	// Status is "active" or "disabled". Empty defaults to active. A disabled
	// endpoint receives no bus deliveries, but can still be exercised with
	// POST /v1/webhooks/{id}/test.
	Status string `json:"status" url:"-"`
	// Description is a free-text label for the console. Optional, clipped to 1024 bytes.
	Description string `json:"description" url:"-"`
}

// updateEndpointIn is createEndpointIn addressed at one existing endpoint. The
// fields are spelled out rather than embedded because zipdoc keys a field's prose
// on the OUTER type's name, so an embedded carrier publishes its shape with no
// prose on any field of it.
type updateEndpointIn struct {
	// ID is the webhook endpoint to update, from the path.
	ID string `json:"-" url:"id"`
	// URL is the https:// address each matching event is POSTed to. Required,
	// max 2048 bytes; http:// and every other scheme is refused.
	URL string `json:"url" url:"-"`
	// Events are NATS subject patterns to subscribe to. An empty or omitted list
	// means EVERY event. Max 64 patterns, each max 256 bytes.
	Events []string `json:"events" url:"-"`
	// Status is "active" or "disabled". Empty defaults to active.
	Status string `json:"status" url:"-"`
	// Description is a free-text label for the console. Optional, clipped to 1024 bytes.
	Description string `json:"description" url:"-"`
}

// listDeliveriesIn addresses one endpoint's delivery log and pages it.
type listDeliveriesIn struct {
	// ID is the webhook endpoint whose log to read, from the path.
	ID string `json:"-" url:"id"`
	// Limit caps how many attempts come back: default 50, maximum 200. A value
	// that is not a positive integer reads as the default.
	Limit int `json:"limit"`
	// Status narrows the log to one outcome: "ok", "retrying" or "failed".
	// Empty returns every attempt.
	Status string `json:"status"`
}

// endpointList is the org's registered endpoints as one listing answers them.
type endpointList struct {
	// Data is the org's endpoints, newest first, each with its signing secret
	// REDACTED — the secret leaves the server only on create and on rotate.
	Data []Endpoint `json:"data"`
}

// deliveryList is one endpoint's delivery log as the log read answers it.
type deliveryList struct {
	// Data is the matching attempts, newest first.
	Data []DeliveryRow `json:"data"`
}

// noContent is the Out of an op that answers 204 with an empty body. It is an
// ALIAS for the unnamed empty struct, not a definition: zip keys the response on
// 204 only when the Out type has no name.
type noContent = struct{}

// noInput is the In of an op addressed entirely by the caller's own validated
// principal: it takes nothing off the wire.
type noInput struct{}

// tenant resolves the caller's org for a TYPED op — the VALIDATED org
// cloud.Bridge parked on the context, 401 otherwise. It is the same gate
// apps/notify applies and the same decision principal.Org makes (a validated
// principal, then a non-empty bounded org); OrgFrom is only how that one answer
// reaches a handler whose signature has no request in it.
//
// The org is NEVER an In field: an In field is caller-supplied, so a tenant key
// read from one is a cross-tenant read the caller asserted for itself. Fails
// closed off the HTTP path, where there is no principal and therefore no tenant.
//
// One 401 where the untyped gate had two. principal.Org already folds "no
// validated principal" and "no usable org" into one answer, so a typed op can
// see the refusal but not which half of it fired; the status, the body shape and
// the ordering are unchanged, and the message names both halves.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrUnauthorized("webhooks: a validated principal with an org scope is required")
	}
	return org, nil
}

// storeFor is the ONE way this package reaches a store: it names the database
// through cloud.OrgNamespace — the single door a validated org walks through —
// and asks the registry for that name. Nothing else here resolves a store, so
// "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process seam.
func (s *state) storeFor(org string) (*store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.stores.For(ns)
}

// listEndpoints returns every webhook endpoint the caller's org has registered,
// newest first, each with its 7-day delivery and failure counts. Signing secrets
// are redacted here — a secret leaves the server only on create and on rotate.
// The listing is physically org-scoped, so another tenant's endpoints are not
// reachable from this route at all.
func (o ops) listEndpoints(ctx context.Context, _ *noInput) (*endpointList, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.storeFor(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	eps, err := st.list(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	usage, err := st.usage(ctx, windowStart())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "usage: %v", err)
	}
	for i := range eps {
		eps[i].Secret = ""
		u := usage[eps[i].ID] // zero value when the endpoint has no delivery history
		eps[i].Deliveries7d, eps[i].Failures7d = u.Deliveries, u.Failures
	}
	return &endpointList{Data: eps}, nil
}

// createEndpoint registers a new webhook subscription for the caller's org and
// answers 201 with the endpoint INCLUDING its freshly minted signing secret.
// This is one of only two responses that ever carry that secret (the other is
// rotate) — store it now, because no later read returns it. The org is stamped by
// the server from the validated principal, so a body can never register an
// endpoint in another tenant.
//
// Example: {"url": "https://acme.example/hooks/hanzo", "events": ["commerce.order.>"], "description": "order pipeline"}
func (o ops) createEndpoint(ctx context.Context, in *createEndpointIn) (*Endpoint, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	url, verr := validateURL(in.URL)
	if verr != nil {
		return nil, verr
	}
	events, verr := validateEvents(in.Events)
	if verr != nil {
		return nil, verr
	}
	status, verr := validateStatus(in.Status, "active")
	if verr != nil {
		return nil, verr
	}

	st, err := o.s.State.storeFor(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	e := Endpoint{
		ID:          newID("wh"),
		Org:         org,
		URL:         url,
		Events:      events,
		Secret:      newSecret(),
		Status:      status,
		Description: clip(in.Description, maxDescription),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := st.create(ctx, e); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "create: %v", err)
	}
	return &e, nil
}

// getEndpoint returns one of the caller org's webhook endpoints with its 7-day
// delivery and failure counts, signing secret redacted. An id another org owns
// reads as not found, so the response cannot confirm that it exists.
//
// Example: {"id": "wh_9f8c1d2e"}
func (o ops) getEndpoint(ctx context.Context, in *endpointRef) (*Endpoint, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.storeFor(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	e, err := st.get(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, notFoundOr(err)
	}
	e.Secret = ""
	usage, err := st.usage(ctx, windowStart())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "usage: %v", err)
	}
	u := usage[e.ID]
	e.Deliveries7d, e.Failures7d = u.Deliveries, u.Failures
	return &e, nil
}

// updateEndpoint replaces the editable fields of one of the caller org's
// endpoints — url, events, status and description — and answers the stored row
// with its secret redacted. It is a full replace, not a patch: an omitted field
// is written as its empty value, and an omitted or empty events list resubscribes
// the endpoint to EVERY event. The signing secret and the creation time are
// immutable here; rotate the secret with POST /v1/webhooks/{id}/secret.
//
// Example: {"id": "wh_9f8c1d2e", "url": "https://acme.example/hooks/v2", "events": ["commerce.order.paid"], "status": "disabled"}
func (o ops) updateEndpoint(ctx context.Context, in *updateEndpointIn) (*Endpoint, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	url, verr := validateURL(in.URL)
	if verr != nil {
		return nil, verr
	}
	events, verr := validateEvents(in.Events)
	if verr != nil {
		return nil, verr
	}
	status, verr := validateStatus(in.Status, "active")
	if verr != nil {
		return nil, verr
	}
	st, err := o.s.State.storeFor(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	e, err := st.update(ctx, strings.TrimSpace(in.ID), url, events, status, clip(in.Description, maxDescription), now)
	if err != nil {
		return nil, notFoundOr(err)
	}
	e.Secret = ""
	return &e, nil
}

// deleteEndpoint removes one of the caller org's webhook endpoints and answers
// 204 with no body. Delivery stops immediately and the endpoint's signing secret
// is gone with it; its recorded delivery history goes too. An id another org owns
// reads as not found.
//
// Example: {"id": "wh_9f8c1d2e"}
func (o ops) deleteEndpoint(ctx context.Context, in *endpointRef) (*noContent, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.storeFor(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	ok, err := st.del(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !ok {
		return nil, zip.ErrNotFound("endpoint not found")
	}
	return nil, nil
}

// listDeliveries returns one endpoint's per-attempt delivery log, newest first —
// the record of what was sent, what the subscriber answered, and how long it
// took. One event that retried three times appears as three rows sharing a
// delivery id. It is org-scoped exactly like every other route here: the endpoint
// lookup only ever finds THIS org's endpoint, so another org's id is a 404 and
// never a window onto its logs.
//
// Example: {"id": "wh_9f8c1d2e", "status": "failed", "limit": 100}
func (o ops) listDeliveries(ctx context.Context, in *listDeliveriesIn) (*deliveryList, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.storeFor(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id := strings.TrimSpace(in.ID)
	if _, err := st.get(ctx, id); err != nil {
		return nil, notFoundOr(err) // 404 for a missing id (or another org's id)
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	rows, err := st.deliveries(ctx, id, clampLimit(in.Limit), status)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "deliveries: %v", err)
	}
	return &deliveryList{Data: rows}, nil
}

// testEndpoint sends ONE signed test event to the endpoint right now and answers
// the outcome inline, so the console can show whether the subscriber is reachable
// without waiting for real traffic. It takes the same attempt path the bus
// dispatcher takes — one attempt, 10s timeout, no retry ladder — and records the
// result in the endpoint's delivery log. It works on a DISABLED endpoint too:
// validating one you have paused is the whole point.
//
// Example: {"id": "wh_9f8c1d2e"}
func (o ops) testEndpoint(ctx context.Context, in *endpointRef) (*testResult, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.storeFor(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	e, err := st.get(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, notFoundOr(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"type":     testSubject,
		"org":      org,
		"endpoint": e.ID,
		"ts":       time.Now().Unix(),
		"note":     "test delivery from /v1/webhooks",
	})
	job := deliveryJob{org: org, endpointID: e.ID, url: e.URL, secret: e.Secret, subject: testSubject, delivery: newUUID(), body: payload}
	res := o.s.State.disp.attempt(ctx, job)
	o.s.State.disp.recordAttempt(ctx, job, 1, statusLabel(res.ok, false), res) // single attempt ⇒ terminal
	return &testResult{
		Delivered:  res.ok,
		HTTPStatus: res.httpStatus,
		DurationMs: res.duration.Milliseconds(),
		Error:      res.err,
	}, nil
}

// rotateSecret mints a NEW HMAC signing secret for the endpoint and answers the
// endpoint WITH it — the only other response besides create that ever carries a
// secret. The old secret stops working the instant this returns: every subsequent
// delivery signs with the new one, with no overlap window. Call it when the
// subscriber is ready to swap the value on its side, not before.
//
// Example: {"id": "wh_9f8c1d2e"}
func (o ops) rotateSecret(ctx context.Context, in *endpointRef) (*Endpoint, error) {
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.storeFor(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	e, err := st.setSecret(ctx, strings.TrimSpace(in.ID), newSecret(), now)
	if err != nil {
		return nil, notFoundOr(err)
	}
	// e.Secret is intentionally NOT redacted — this IS the reveal-once response.
	return &e, nil
}

// ---- validation + helpers ----

// validateURL enforces the model's "https required" rule: an https:// absolute URL,
// bounded in length. http:// (cleartext) and any other scheme are rejected — a webhook
// carries signed event data and must not be sent in the clear.
func validateURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", zip.ErrBadRequest("url is required")
	}
	if len(u) > maxURL {
		return "", zip.ErrBadRequest("url too long")
	}
	if !strings.HasPrefix(strings.ToLower(u), "https://") || len(u) <= len("https://") {
		return "", zip.ErrBadRequest("url must be an https:// URL")
	}
	return u, nil
}

// validateEvents bounds the subject-pattern list. An empty list is allowed and means
// "all events". Each pattern is trimmed + length-bounded; empties are dropped.
func validateEvents(in []string) ([]string, error) {
	if len(in) > maxEvents {
		return nil, zip.ErrBadRequest("too many event patterns")
	}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > maxPattern {
			return nil, zip.ErrBadRequest("event pattern too long")
		}
		out = append(out, p)
	}
	return out, nil
}

// validateStatus admits only active|disabled, defaulting an empty value.
func validateStatus(raw, def string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		s = def
	}
	if s != "active" && s != "disabled" {
		return "", zip.ErrBadRequest("status must be active or disabled")
	}
	return s, nil
}

func notFoundOr(err error) error {
	if err == errNotFound {
		return zip.ErrNotFound("endpoint not found")
	}
	return zip.Errorf(http.StatusInternalServerError, "%v", err)
}

// clampLimit bounds a requested page size to (0, maxDeliveryLimit], defaulting
// anything that is not a positive integer. A `?limit=` value zip could not parse
// as an int arrives here as 0, which is exactly the "absent or unusable" case the
// untyped strconv.Atoi branch answered with the default — so the wire is unchanged.
func clampLimit(n int) int {
	if n <= 0 {
		return defaultDeliveryLimit
	}
	if n > maxDeliveryLimit {
		return maxDeliveryLimit
	}
	return n
}

// windowStart is the RFC3339-UTC lower bound of the usage window (now - usageWindow), the
// value the delivery table's sortable `created` column is compared against.
func windowStart() string {
	return time.Now().UTC().Add(-usageWindow).Format(time.RFC3339)
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}

// newID mints a prefixed, collision-resistant id (128 random bits).
func newID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// newSecret mints the HMAC signing secret returned once on create (256 random bits,
// `whsec_`-prefixed so a leaked value is greppable).
func newSecret() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return "whsec_" + hex.EncodeToString(b[:])
}
