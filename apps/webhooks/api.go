package webhooks

// api.go — the /v1/webhooks registry surface: an org's CRUD over its OWN webhook
// endpoints. Every handler resolves the caller's org from the VALIDATED principal
// (principal.Org — the gateway-minted X-Org-Id, HIP-0026), exactly like clients/notify,
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

// endpointInput is the create request body. Secret, id and timestamps are
// server-owned and ignored if a client sends them.
type endpointInput struct {
	// URL is the https:// address deliveries are POSTed to. Required; cleartext http is refused.
	URL string `json:"url" validate:"required"`
	// Events is the subject patterns to subscribe to, NATS wildcard syntax. Empty means every event.
	Events []string `json:"events"`
	// Status is active or disabled. Empty means active.
	Status string `json:"status"`
	// Description is a free-text label for the endpoint.
	Description string `json:"description"`
}

// endpointUpdate is the update request body plus the endpoint id the PATH names. It
// carries the same editable fields as a create; the secret, the timestamps and the
// usage counters stay server-owned.
type endpointUpdate struct {
	// ID is the endpoint id from the path.
	ID string `json:"id"`
	// URL is the https:// address deliveries are POSTed to. Required; cleartext http is refused.
	URL string `json:"url" validate:"required"`
	// Events is the subject patterns to subscribe to, NATS wildcard syntax. Empty means every event.
	Events []string `json:"events"`
	// Status is active or disabled. Empty means active.
	Status string `json:"status"`
	// Description is a free-text label for the endpoint.
	Description string `json:"description"`
}

// endpointRef addresses one registered endpoint.
type endpointRef struct {
	// ID is the endpoint id from the path, as returned by create.
	ID string `json:"id"`
}

// deliveryQuery pages one endpoint's delivery log.
type deliveryQuery struct {
	// ID is the endpoint id from the path.
	ID string `json:"id"`
	// Status narrows the log to one outcome: ok, retrying or failed.
	Status string `json:"status"`
	// Limit caps the attempts returned; 0 means 50 and nothing above 200 is honoured.
	Limit int `json:"limit"`
}

// endpointList is an org's registered endpoints.
type endpointList struct {
	// Data is the endpoints, each with its trailing-7-day delivery counters and no secret.
	Data []Endpoint `json:"data"`
}

// deliveryList is one endpoint's delivery log.
type deliveryList struct {
	// Data is the attempts, newest first.
	Data []DeliveryRow `json:"data"`
}

// tenant resolves the caller's org from the validated principal, 401 otherwise —
// the same gate clients/notify applies (Validated ⇒ trusted org, else unauthenticated).
// tenant resolves the caller's org for a typed op, through the request cloud.Bridge
// parked. Off the HTTP path there is no principal at all, which is the same refusal a
// missing one gets.
func (o ops) tenant(ctx context.Context) (string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", zip.ErrUnauthorized("webhooks: authentication required")
	}
	return tenant(c)
}

func tenant(c *zip.Ctx) (string, error) {
	if !principal.Validated(c) {
		return "", zip.ErrUnauthorized("webhooks: authentication required")
	}
	org, ok := principal.Org(c)
	if !ok || org == "" {
		return "", zip.ErrUnauthorized("webhooks: org scope required")
	}
	return org, nil
}

func (s *state) storeFor(org string) (*store, error) { return s.stores.For(org, "") }

// listEndpoints returns the caller org's registered webhook endpoints. Each carries its
// trailing-7-day delivery and failure counts, and signing secrets are never included — a
// secret leaves the server only on create and on rotate.
//
// Response: {"data": [{"id": "wh_4c1e9b7a", "org": "acme", "url": "https://acme.example/hooks", "events": ["commerce.>"], "status": "active", "created": "2026-01-01T00:00:00Z", "updated": "2026-01-01T00:00:00Z", "deliveries7d": 412, "failures7d": 3}]}
func (o ops) listEndpoints(ctx context.Context, _ *struct{}) (*endpointList, error) {
	s := o.s
	org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.State.storeFor(org)
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

// createEndpoint registers a webhook endpoint and reveals its signing secret. This is one of only two responses that ever carry the secret (the
// other is rotate), so a caller that does not store it here must rotate to get another.
//
// Example: {"url": "https://acme.example/hooks", "events": ["commerce.>"], "status": "active", "description": "billing events"}
func (o ops) createEndpoint(ctx context.Context, in *endpointInput) (*Endpoint, error) {
	s := o.s
	org, err := o.tenant(ctx)
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

	st, err := s.State.storeFor(org)
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

// getEndpoint returns one of the caller org's endpoints with its 7-day counters. The
// signing secret is never included, and another org's id is a 404.
//
// Example: {"id": "wh_4c1e9b7a2d6f0538"}
func (o ops) getEndpoint(ctx context.Context, in *endpointRef) (*Endpoint, error) {
	s := o.s
	org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.State.storeFor(org)
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

// updateEndpoint replaces one endpoint's url, events, status and description. It is a
// full write, not a patch: an omitted field is cleared. The signing secret and the
// creation time are immutable, and the endpoint updated is the one the PATH names.
//
// Example: {"id": "wh_4c1e9b7a2d6f0538", "url": "https://acme.example/hooks/v2", "events": ["commerce.invoice.>"], "status": "disabled"}
func (o ops) updateEndpoint(ctx context.Context, in *endpointUpdate) (*Endpoint, error) {
	s := o.s
	org, err := o.tenant(ctx)
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
	st, err := s.State.storeFor(org)
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

// deleteEndpoint unregisters one endpoint and answers 204. Delivery stops immediately;
// the recorded delivery log goes with it.
//
// Example: {"id": "wh_4c1e9b7a2d6f0538"}
func (o ops) deleteEndpoint(ctx context.Context, in *endpointRef) (*struct{}, error) {
	s := o.s
	org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.State.storeFor(org)
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

// listDeliveries returns one endpoint's per-attempt delivery log, newest first. It is
// org-scoped like every other read — another org's id is a 404, never a window onto its
// logs — and each row carries the attempt number, HTTP status, duration and any error.
// Example: {"id": "wh_4c1e9b7a2d6f0538", "status": "failed", "limit": 100}
func (o ops) listDeliveries(ctx context.Context, in *deliveryQuery) (*deliveryList, error) {
	s := o.s
	org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.State.storeFor(org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	id := strings.TrimSpace(in.ID)
	if _, err := st.get(ctx, id); err != nil {
		return nil, notFoundOr(err) // 404 for a missing id (or another org's id)
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	rows, err := st.deliveries(ctx, id, parseLimit(in.Limit), status)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "deliveries: %v", err)
	}
	return &deliveryList{Data: rows}, nil
}

// testEndpoint sends one signed test event and reports the outcome inline. It runs the
// same attempt path the dispatcher uses — single attempt, 10s timeout, no retry ladder —
// and records the delivery row, so the result is exactly what a real event would see. It
// works on a DISABLED endpoint too, since validating an endpoint you have paused is the
// whole point.
// Example: {"id": "wh_4c1e9b7a2d6f0538"}
// Response: {"delivered": true, "httpStatus": 200, "durationMs": 84}
func (o ops) testEndpoint(ctx context.Context, in *endpointRef) (*testResult, error) {
	s := o.s
	org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.State.storeFor(org)
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
	res := s.State.disp.attempt(ctx, job)
	s.State.disp.recordAttempt(ctx, job, 1, statusLabel(res.ok, false), res) // single attempt ⇒ terminal
	return &testResult{
		Delivered:  res.ok,
		HTTPStatus: res.httpStatus,
		DurationMs: res.duration.Milliseconds(),
		Error:      res.err,
	}, nil
}

// rotateSecret mints a new signing secret and reveals it once. The old secret is invalid
// the instant this returns — every subsequent delivery and test signs with the new one,
// with no overlap window — so a subscriber should call this only when it is ready to
// swap the secret on its own side.
// Example: {"id": "wh_4c1e9b7a2d6f0538"}
func (o ops) rotateSecret(ctx context.Context, in *endpointRef) (*Endpoint, error) {
	s := o.s
	org, err := o.tenant(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.State.storeFor(org)
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

// parseLimit clamps the requested page size to (0, maxDeliveryLimit], defaulting a
// missing or unparseable value to defaultDeliveryLimit.
func parseLimit(n int) int {
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
