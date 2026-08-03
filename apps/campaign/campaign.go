// Package campaign is one go-to-market push across paid, organic and email at once.
//
// A campaign — audience, creatives, schedule, budget — launches to every
// channel and reads back as one funnel with each channel's spend.
//
// A Campaign is a VALUE — {name, audience, content[], schedule, budget,
// channels[], status} — that SPANS channels and fans out to orthogonal executors.
// It is the capability layer that CONSUMES the connector plane: the campaign
// object never touches a credential; each channel executor resolves the org's
// connector token itself through the integrations.TokenFor custody seam.
//
// THE DECOMPLECT (HIP-0126 — Integrations, Connectors & the Extension Runtime): a
// Connector is a connection (credential custody + auth); a capability is what you
// DO with it. /v1/campaign is a CONSUMER of connectors — the role HIP-0126 gives
// Flows — never a second credential path. "campaign" used to be braided across
// three packages — an ad campaign (apps/ads), an email campaign
// (apps/marketing), social posts (apps/social). This plane lifts the GTM
// campaign to the ONE value it is and makes the channels orthogonal EXECUTORS it
// fans out to (channel.go):
//
//	paid    → apps/ads       (meta_ads/google_ads/tiktok_ads/… — REGISTERED)
//	organic → apps/social    (the social connectors — NO executor registered yet)
//	email   → apps/marketing (sendgrid/mailchimp/… — NO executor registered yet)
//
// Only the paid executor is wired today (plugin/campaign/seams.go). A campaign
// carrying an organic or email channel launches its paid channels and records the
// others "unavailable" — honest, never a faked launch. Until those two executors
// exist, /v1/social and /v1/marketing are the ONLY way to run those channels, and
// each is ALSO usable standalone once they are; this plane composes them. Metrics are NOT stored here — a campaign's results are read at query time
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
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
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
	// A typed op is a route PLUS a registry entry, and the registry lives on the
	// App. A router that cannot reach it would serve every route with no schema,
	// no prose, no MCP tool and no SDK method — so the mount FAILS rather than
	// quietly publishing a surface no projection knows about.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("campaign.Mount: router is not a zip app, so the typed ops have no registry")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("campaign.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "campaign")
	s := &cloud.Service[state]{Base: b, State: state{store: store}}
	mounted = s

	routes(app, zapp, s)

	b.Log.Info("campaign mounted", "brand", deps.Brand, "channels", registeredKinds())
	return nil
}

// routes registers the campaign surface. Registration order IS match order (zip
// is first-match): the static /summary is registered before /:id so it is never
// captured by the id param, and the deeper /:id/… routes have a distinct segment
// count so none shadows another.
func routes(app cloud.Router, zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/campaign")
	// Bridge FIRST: a typed op receives only a context, so the validated org
	// reaches it by being parked there — never as an In field, which is
	// caller-supplied and would be a cross-tenant read the caller asserted for
	// itself. fiber runs middleware in registration order, so this must precede
	// every leaf below; nesting under Serve's own Bridge is harmless (the inner
	// one is what the handler sees).
	g.Use(cloud.Bridge())

	zip.Get(g, "/summary", o.summary)

	// The root of the surface, declared on the App with its whole path rather than
	// on the group with an empty leaf: joining "/v1/campaign" with "" yields
	// "/v1/campaign/", a DIFFERENT path from the one these two have always served.
	zip.Get(zapp, "/v1/campaign", o.list)
	zip.Post(zapp, "/v1/campaign", o.create, zip.WithStatus(http.StatusCreated))

	zip.Get(g, "/:id", o.get)
	zip.Put(g, "/:id", o.update)
	zip.Delete(g, "/:id", o.del)

	// UNTYPED BY DESIGN — neither has ever read a request body, and zip's invoke
	// refuses one it cannot parse before the handler runs, so typing them would
	// turn today's 200 into a 400 for a caller that posts junk to a route that
	// ignores it. typed.go states the measurement and the zip-side fix.
	g.Post("/:id/launch", cloud.Handle(s, launchCampaign))
	g.Post("/:id/pause", cloud.Handle(s, pauseCampaign))
	zip.Get(g, "/:id/metrics", o.metrics)

	zip.Post(g, "/:id/channels", o.addChannel)
	zip.Delete(g, "/:id/channels/:kind", o.removeChannel)
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

// Shutdown closes the campaign store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
