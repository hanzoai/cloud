package webhooks

// api.go — the /v1/webhooks registry surface: an org's CRUD over its OWN webhook
// endpoints. Every handler resolves the caller's org from the VALIDATED principal
// (principal.Org — the gateway-minted X-Org-Id, HIP-0026), exactly like clients/notify,
// and never from a client-supplied body/header. An unauthenticated caller gets 401; a
// signed-in caller sees and mutates ONLY its own org's endpoints (physical per-org
// SQLite makes cross-tenant access impossible).

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	// maxURL / maxDescription / maxEvents bound the fields a create/update accepts so a
	// hostile body can't amplify the shared store or a delivery.
	maxURL         = 2048
	maxDescription = 1024
	maxEvents      = 64
	maxPattern     = 256
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
}

// endpointInput is the create/update request body. Secret/id/timestamps are
// server-owned and ignored if a client sends them.
type endpointInput struct {
	URL         string   `json:"url"`
	Events      []string `json:"events"`
	Status      string   `json:"status"`
	Description string   `json:"description"`
}

// tenant resolves the caller's org from the validated principal, 401 otherwise —
// the same gate clients/notify applies (Validated ⇒ trusted org, else unauthenticated).
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

// listEndpoints returns the org's endpoints (secret redacted).
func listEndpoints(s *cloud.Service[*state], c *zip.Ctx) error {
	org, err := tenant(c)
	if err != nil {
		return err
	}
	st, err := s.State.storeFor(org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	eps, err := st.list(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	for i := range eps {
		eps[i].Secret = ""
	}
	return c.JSON(http.StatusOK, map[string]any{"data": eps})
}

// createEndpoint registers a new endpoint and returns it WITH the freshly-minted
// signing secret — the only response that ever carries it.
func createEndpoint(s *cloud.Service[*state], c *zip.Ctx) error {
	org, err := tenant(c)
	if err != nil {
		return err
	}
	var in endpointInput
	if err := c.Bind(&in); err != nil {
		return zip.ErrBadRequest("malformed request body")
	}
	url, verr := validateURL(in.URL)
	if verr != nil {
		return verr
	}
	events, verr := validateEvents(in.Events)
	if verr != nil {
		return verr
	}
	status, verr := validateStatus(in.Status, "active")
	if verr != nil {
		return verr
	}

	st, err := s.State.storeFor(org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
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
	if err := st.create(c.Context(), e); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "create: %v", err)
	}
	return c.JSON(http.StatusCreated, e)
}

// getEndpoint returns one endpoint (secret redacted).
func getEndpoint(s *cloud.Service[*state], c *zip.Ctx) error {
	org, err := tenant(c)
	if err != nil {
		return err
	}
	st, err := s.State.storeFor(org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	e, err := st.get(c.Context(), idParam(c))
	if err != nil {
		return notFoundOr(err)
	}
	e.Secret = ""
	return c.JSON(http.StatusOK, e)
}

// updateEndpoint edits url/events/status/description (secret + created immutable).
func updateEndpoint(s *cloud.Service[*state], c *zip.Ctx) error {
	org, err := tenant(c)
	if err != nil {
		return err
	}
	var in endpointInput
	if err := c.Bind(&in); err != nil {
		return zip.ErrBadRequest("malformed request body")
	}
	url, verr := validateURL(in.URL)
	if verr != nil {
		return verr
	}
	events, verr := validateEvents(in.Events)
	if verr != nil {
		return verr
	}
	status, verr := validateStatus(in.Status, "active")
	if verr != nil {
		return verr
	}
	st, err := s.State.storeFor(org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	e, err := st.update(c.Context(), idParam(c), url, events, status, clip(in.Description, maxDescription), now)
	if err != nil {
		return notFoundOr(err)
	}
	e.Secret = ""
	return c.JSON(http.StatusOK, e)
}

// deleteEndpoint removes an endpoint.
func deleteEndpoint(s *cloud.Service[*state], c *zip.Ctx) error {
	org, err := tenant(c)
	if err != nil {
		return err
	}
	st, err := s.State.storeFor(org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	ok, err := st.del(c.Context(), idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !ok {
		return zip.ErrNotFound("endpoint not found")
	}
	return c.NoContent(http.StatusNoContent)
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

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

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
