// Package campaign mounts the Hanzo Cloud /v1/campaign/* surface: the top-level
// go-to-market orchestration plane. A Campaign is a VALUE — {name, audience,
// content[], schedule, budget, channels[], status} — that SPANS channels and fans
// out to orthogonal executors. It is the capability layer that CONSUMES the
// connector plane: the campaign object never touches a credential; each channel
// executor resolves the org's connector token itself through the
// integrations.TokenFor custody seam.
//
// THE DECOMPLECT (HIP-0126 — Integrations, Connectors & the Extension Runtime): a
// Connector is a connection (credential custody + auth); a capability is what you
// DO with it. /v1/campaign is a CONSUMER of connectors — the role HIP-0126 gives
// Flows — never a second credential path. "campaign" used to be braided across
// three packages — an ad campaign (clients/ads), an email campaign
// (clients/marketing), social posts (clients/social). This plane lifts the GTM
// campaign to the ONE value it is and makes the channels orthogonal EXECUTORS it
// fans out to (channel.go):
//
//	paid    → /v1/ads       (the ad connectors: meta_ads/google_ads/tiktok_ads/…)
//	organic → /v1/publish   (the social connectors)
//	email   → /v1/marketing (the email connectors: sendgrid/mailchimp/…)
//
// Each channel is ALSO usable standalone at its own surface; this plane composes
// them. Metrics are NOT stored here — a campaign's results are read at query time
// from the ONE analytics plane (metrics.go: analytics.CampaignMetrics over the
// utm_campaign-tagged events) plus each channel connector's reported spend. A
// creative A/B is an experiment whose variant = creative and whose metric = the
// campaign result from analytics; it composes the experiment seam (experiment.go),
// never a second assignment or evidence store.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED bearer
// owner claim (HIP-0026) — and NEVER a client-supplied header. Every store query
// filters WHERE org=?, and the org is the value passed to every channel executor,
// so a campaign can only ever resolve its OWN org's connector token.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/campaign/summary               per-org roll-up + wired channels
//	GET    /v1/campaign                        list campaigns (?status=)   -> {data:[…]}
//	POST   /v1/campaign                        create a campaign (draft)    -> Campaign (201)
//	GET    /v1/campaign/:id                    campaign detail              -> Campaign
//	PUT    /v1/campaign/:id                    update a draft campaign      -> Campaign
//	DELETE /v1/campaign/:id                    delete a campaign
//	POST   /v1/campaign/:id/launch             fan out to channels          -> Campaign
//	POST   /v1/campaign/:id/pause              pause every live channel     -> Campaign
//	GET    /v1/campaign/:id/metrics            analytics results + spend    -> Metrics
//	POST   /v1/campaign/:id/channels           add a channel                -> Campaign
//	DELETE /v1/campaign/:id/channels/:kind     remove a channel             -> Campaign
//
// serve.go auto-registers GET /v1/campaign/health (no OwnsHealth here).
package campaign

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	// maxField caps a single text field so an unbounded body can't amplify the
	// shared DB or a list response.
	maxField = 2048
	// maxContent caps how many creatives one campaign carries (A/B variant set).
	maxContent = 32
	// maxChannels caps the fan-out breadth of one campaign.
	maxChannels = 12
	// defaultLimit / maxLimit bound list responses.
	defaultLimit = 200
	maxLimit     = 1000
)

// kinds is the channel-kind vocabulary. A channel with an unknown kind is
// rejected at add time; the executor for a valid-but-unwired kind is resolved
// (and honestly recorded "unavailable") only at launch.
var kinds = map[string]bool{
	KindPaid: true, KindOrganic: true, KindEmail: true,
}

// state is campaign's own data; shared deps (logger, brand) live in cloud.Base.
type state struct {
	store *Store
}

// mounted is the active service so Shutdown can release the store.
var mounted *cloud.Service[state]

// Mount wires the campaign surface onto app per HIP-0106. It keeps a package
// global (mounted) for Shutdown, so it constructs the Service value directly —
// the same "complex flavour" clients/ads uses.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("campaign.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("campaign.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("campaign.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("campaign.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "campaign.db"))
	if err != nil {
		return fmt.Errorf("campaign.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "campaign")
	s := &cloud.Service[state]{Base: b, State: state{store: store}}
	mounted = s

	routes(app, s)

	b.Log.Info("campaign mounted", "brand", deps.Brand, "channels", registeredKinds())
	return nil
}

// routes registers the campaign surface. Registration order IS match order (zip
// is first-match): the static /summary is registered before /:id so it is never
// captured by the id param, and the deeper /:id/… routes have a distinct segment
// count so none shadows another.
func routes(app cloud.Router, s *cloud.Service[state]) {
	app.Get("/v1/campaign/summary", cloud.Handle(s, summary))

	app.Get("/v1/campaign", cloud.Handle(s, listCampaigns))
	app.Post("/v1/campaign", cloud.Handle(s, createCampaign))
	app.Get("/v1/campaign/:id", cloud.Handle(s, getCampaign))
	app.Put("/v1/campaign/:id", cloud.Handle(s, updateCampaign))
	app.Delete("/v1/campaign/:id", cloud.Handle(s, deleteCampaign))

	app.Post("/v1/campaign/:id/launch", cloud.Handle(s, launchCampaign))
	app.Post("/v1/campaign/:id/pause", cloud.Handle(s, pauseCampaign))
	app.Get("/v1/campaign/:id/metrics", cloud.Handle(s, metricsCampaign))

	app.Post("/v1/campaign/:id/channels", cloud.Handle(s, addChannel))
	app.Delete("/v1/campaign/:id/channels/:kind", cloud.Handle(s, removeChannel))
}

// ---- shared helpers (mirror clients/ads) ----

// tenant resolves the org — the tenant-isolation KEY — for a request, EXACTLY as
// SanitizeIdentity minted it from the validated IAM owner claim (HIP-0026).
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// clip trims and bounds a text field to maxField.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxField {
		return s[:maxField]
	}
	return s
}

func limitOf(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

func nonNeg(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// clipContent bounds + trims a creative set.
func clipContent(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = clip(v)
		if v == "" {
			continue
		}
		out = append(out, v)
		if len(out) >= maxContent {
			break
		}
	}
	return out
}

// normChannel validates + normalizes one ChannelSpec at add/create time. Status
// is always reset to pending here (a client can never assert a launched state);
// ExternalID/Detail are server-owned and cleared.
func normChannel(in ChannelSpec) (ChannelSpec, bool) {
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if !kinds[kind] {
		return ChannelSpec{}, false
	}
	return ChannelSpec{
		Kind:     kind,
		Platform: strings.ToLower(clip(in.Platform)),
		Account:  clip(in.Account),
		Status:   chanPending,
	}, true
}

// normChannels validates + de-duplicates a channel set by kind (one executor per
// kind per campaign — the fan-out is over kinds).
func normChannels(in []ChannelSpec) ([]ChannelSpec, bool) {
	out := make([]ChannelSpec, 0, len(in))
	seen := map[string]bool{}
	for _, ch := range in {
		n, ok := normChannel(ch)
		if !ok {
			return nil, false
		}
		if seen[n.Kind] {
			continue
		}
		seen[n.Kind] = true
		out = append(out, n)
		if len(out) >= maxChannels {
			break
		}
	}
	return out, true
}

// mapErr maps a store sentinel error to the right HTTP error.
func mapErr(err error, notFoundMsg string) error {
	switch err {
	case errNotFound:
		return zip.ErrNotFound(notFoundMsg)
	case errConflict:
		return zip.ErrConflict("already exists")
	default:
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
}

// shortErr renders a one-line, secret-free failure reason for a channel Detail.
func shortErr(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ---- CRUD ----

func createCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	var body Campaign
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := clip(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	channels, okCh := normChannels(body.Channels)
	if !okCh {
		return zip.ErrBadRequest("each channel kind must be one of paid, organic, email")
	}
	id, err := genID("cmp")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	camp := Campaign{
		ID: id, Org: org, Name: name, Audience: clip(body.Audience),
		Content: clipContent(body.Content), Channels: channels,
		ScheduleAt: nonNeg(body.ScheduleAt), Budget: nonNeg(body.Budget),
		Status: StatusDraft, CreatedAt: now, UpdatedAt: now,
	}
	saved, err := s.State.store.CreateCampaign(c.Context(), camp)
	if err != nil {
		return mapErr(err, "")
	}
	return c.JSON(http.StatusCreated, saved)
}

func listCampaigns(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	rows, err := s.State.store.ListCampaigns(c.Context(), org, status, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func getCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	camp, err := s.State.store.GetCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	return c.JSON(http.StatusOK, camp)
}

// updateCampaign edits the campaign's core fields. Channels are replaced only
// when the campaign is a draft — once launched, channels carry provider state
// (add/remove them explicitly via the channels sub-resource so a live launch is
// never silently orphaned).
func updateCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	current, err := s.State.store.GetCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	var body Campaign
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := clip(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	current.Name = name
	current.Audience = clip(body.Audience)
	current.Content = clipContent(body.Content)
	current.ScheduleAt = nonNeg(body.ScheduleAt)
	current.Budget = nonNeg(body.Budget)
	if current.Status == StatusDraft {
		channels, okCh := normChannels(body.Channels)
		if !okCh {
			return zip.ErrBadRequest("each channel kind must be one of paid, organic, email")
		}
		current.Channels = channels
	}
	current.UpdatedAt = time.Now().Unix()
	saved, err := s.State.store.Save(c.Context(), current)
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func deleteCampaign(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	deleted, err := s.State.store.DeleteCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("campaign not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- channels sub-resource ----

func addChannel(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	camp, err := s.State.store.GetCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	var body ChannelSpec
	if err := c.Bind(&body); err != nil {
		return err
	}
	n, okCh := normChannel(body)
	if !okCh {
		return zip.ErrBadRequest("channel kind must be one of paid, organic, email")
	}
	// Replace an existing channel of the same kind, else append (one per kind).
	replaced := false
	for i := range camp.Channels {
		if camp.Channels[i].Kind == n.Kind {
			camp.Channels[i] = n
			replaced = true
			break
		}
	}
	if !replaced {
		if len(camp.Channels) >= maxChannels {
			return zip.ErrBadRequest("too many channels")
		}
		camp.Channels = append(camp.Channels, n)
	}
	camp.UpdatedAt = time.Now().Unix()
	saved, err := s.State.store.Save(c.Context(), camp)
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	return c.JSON(http.StatusOK, saved)
}

func removeChannel(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	camp, err := s.State.store.GetCampaign(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	kind := strings.ToLower(strings.TrimSpace(c.Param("kind")))
	out := make([]ChannelSpec, 0, len(camp.Channels))
	for _, ch := range camp.Channels {
		if ch.Kind != kind {
			out = append(out, ch)
		}
	}
	if len(out) == len(camp.Channels) {
		return zip.ErrNotFound("channel not found")
	}
	camp.Channels = out
	camp.UpdatedAt = time.Now().Unix()
	saved, err := s.State.store.Save(c.Context(), camp)
	if err != nil {
		return mapErr(err, "campaign not found")
	}
	return c.JSON(http.StatusOK, saved)
}

// ---- summary ----

func summary(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("valid bearer required")
	}
	total, live, budget, err := s.State.store.Counts(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"campaigns": total, "live": live, "budget": budget,
		"channels": registeredKinds(),
	})
}

// Shutdown closes the campaign store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
