package destination

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/secret"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/zap-proto/zip"
)

// destinations.go mounts the /v1/destination surface and owns the KMS custody + the
// in-process client. It composes clients/analytics (installs the fan-out sink) and
// clients/integrations (a destination may reuse an OAuth connection's token).
//
// Surface (all org-scoped; /v1 only; mutations require org admin). Four of the
// five are TYPED ops — one registry entry each, which the document, the MCP tool,
// the CLI command and the generated SDK method are all projected from:
//
//	GET    /v1/destination                 platforms + this org's connection status   [typed]
//	GET    /v1/destination/:platform       one platform status                        [typed]
//	POST   /v1/destination/:platform       connect/update (ids + secret)               untyped, see routes()
//	DELETE /v1/destination/:platform       disconnect (forget secrets + row)          [typed]
//	POST   /v1/destination/:platform/test  send ONE synthetic event end-to-end        [typed]
//
// TENANT ISOLATION mirrors clients/ads and clients/integrations: the org is the
// VALIDATED principal's (principal.OrgFrom for a typed op, principal.Org for the
// untyped one — the same value, read through the client each has), never a client
// header and never an In field; every store row is keyed (org,platform) and every
// KMS secret lives under a per-org path.
//
// The card type is DestinationStatus, not Status, and the config-input type is
// DestinationField, not Field: the OpenAPI schema namespace is FLAT across the
// whole fleet, `Status` is already apps/plugins', and openapi.Compose refuses one
// name with two shapes because every generated SDK would bind whichever it read
// last. Typing is what makes a package enter that namespace, so the qualification
// is part of this conversion and not cosmetic.

const (
	// kmsEnv is the stable KMS environment slug destinations secrets are sealed
	// under (destinations are per-org, not per-env — the integrations convention).
	kmsEnv = "default"
	// maxSecret bounds a destination API secret at intake. Real tokens are short;
	// anything over 8 KiB is hostile and refused before it reaches KMS.
	maxSecret = 8192
	// maxField bounds a non-secret config value (a measurement/pixel id is short).
	maxField = 512
	// sendTimeout bounds one destination Send in the fan-out.
	sendTimeout = 10 * time.Second
	// publicFanoutEnv turns the bus listener OFF when falsey. Default ON: a
	// mounted destinations subsystem forwards a connected org's events.
	publicFanoutEnv = "CLOUD_DESTINATIONS_FANOUT"
)

// state is destinations' own data; shared deps live in the embedded cloud.Base.
type state struct {
	store *Store
	// kms is the host's secret plane (deps.KMS): the store in the one process that
	// holds it, and the kms app over the internal plane in every other. nil ⇒
	// secret ops fail closed.
	kms   secret.Store
	dests map[string]Destination
}

// mounted is the active service so Shutdown + the in-process client reach it.
var mounted *cloud.Service[state]

// running is the bus listener, stopped on Shutdown.
var running *listener

// Mount wires /v1/destination/* onto app. Complex flavour (a package global for the
// client + Shutdown, and it installs the analytics fan-out sink), so it constructs the
// Service value directly.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("destination: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("destination.Use:  empty DataDir")
	}
	store, err := openStore(cloud.DataDir(), deps.MasterKey)
	if err != nil {
		return fmt.Errorf("destination.Use:  open store: %w", err)
	}
	// A prune that fails is retried at the next start; it never costs the mount.
	pruned := store.prune(context.Background())
	b := cloud.NewBase(deps, "destination")
	s := &cloud.Service[state]{Base: b, State: state{
		store: store,
		kms:   deps.KMS,
		dests: snapshot(),
	}}
	mounted = s

	routes(app, s)

	// Consume the event plane off the bus (bus.go), unless disabled.
	if fanoutEnabled() {
		running = listen(s)
		b.Log.Info("destinations listening on the event plane", "platforms", len(s.State.dests))
	} else {
		b.Log.Info("destinations fan-out disabled", "flag", publicFanoutEnv)
	}
	if pruned != nil {
		b.Log.Warn("destinations touch prune failed", "err", pruned)
	}
	b.Log.Info("destinations mounted", "platforms", len(s.State.dests), "kms", s.State.kms != nil, "brand", cloud.Brand())
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the /v1/destination surface. Four of the five are TYPED ops
// — one registry entry each, which the document, the MCP tool, the CLI command
// and the generated SDK method are all projected from.
//
// The collection root is the group's own address: an empty leaf declares
// /v1/destination, not /v1/destination/.
//
// POST /:platform stays UNTYPED, deliberately. Its body's property NAMES are
// chosen at request time by the addressed platform's Spec (see connect below) and
// each value may arrive as a JSON string, number or bool, which no Go struct
// describes. It DECLARES its bodies through openapi.Register instead (see the
// init below), so the cost of staying untyped is the three things zip's registry
// supplies — prose, an MCP tool, a CLI command — and not a fourth, a document
// claiming the route takes no body.
//
// Registration order is match order, and it is the order it has always been.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// A typed op receives only a context, so the validated org has to be parked
	// there. cloud.Bridge parks it, and the COMPOSER installs it, not this
	// subsystem: the fused host once at its root (serve.go), and a plugin
	// program's constructor likewise. An install here would only repeat the one
	// the program already carries.

	o := ops{s: s}
	g := app.Group("/v1/destination")
	g.Get("", o.list)
	g.Get("/:platform", o.get)
	g.Raw(http.MethodPost, "/:platform", cloud.Handle(s, connect))
	g.Delete("/:platform", o.disconnect)
	g.Post("/:platform/test", o.test)
	// The browser tag config is GET /v1/project/tags — it must be served by the
	// process that owns the project store (production runs ~25 single-app processes).
}

// The one untyped route DECLARES what it carries. Its request is the map the
// handler binds — an object whose keys are the addressed platform's own config
// and secret names, so `additionalProperties` is open by construction and that is
// the honest schema, not a thin one — and its response is the same
// DestinationStatus card every other route answers with. Without this the
// document said POST /v1/destination/{platform} takes NO body, which is the one
// thing it cannot work without.
// The PROSE goes here for the same reason and by the same rule: a typed op's
// prose is lifted from its doc comment by zipdoc, and this route has no typed op
// to lift from, so without a Describe it publishes an operationId and nothing
// else — an SDK method that cannot explain itself and a CLI command with no help
// text.
func init() {
	spec.Register("/v1/destination/:platform", "POST", map[string]any{}, DestinationStatus{})
	spec.Describe("/v1/destination/:platform", http.MethodPost,
		"Connect one conversion destination for your org, or update the one you have",
		"Stores the addressed platform's non-secret ids (its measurement, pixel or dataset ids) "+
			"and seals its API credential into KMS under a path scoped to the caller's own org, then "+
			"answers the same status card the read routes do — with live telling you whether the "+
			"credential actually resolves right now. The body's property NAMES are the platform's "+
			"own: each field the platform declares, plus each secret under its camelCase name, so "+
			"the accepted keys differ per platform and a missing REQUIRED field is refused. "+
			"Connecting is an ORG ADMIN action — a validated member without the admin bit gets 403 — "+
			"and it fails closed with 503 when the KMS master key is unavailable rather than "+
			"persisting a destination whose secret was never sealed. The secret itself never appears "+
			"in the response, in the store, or in a log line; only its NAME is ever published. Set "+
			"enabled to false to keep the connection but stop the analytics fan-out to it.")
}

// ops binds the service to the typed ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// orgAdmin reports whether the caller is an org admin — the gate every
// destination MUTATION keeps. It needs the REQUEST rather than the tenant because
// org-admin-ness lives in a header (X-User-IsOrgAdmin) that principal.OrgFrom
// does not carry. False off the HTTP path: no request, no attested caller, no
// mutation.
func orgAdmin(ctx context.Context) bool {
	if c, ok := cloud.Request(ctx); ok {
		return principal.IsOrgAdmin(c)
	}
	return false
}

// destinationRef addresses one destination platform. The slug is the path
// segment: the URL is the addressing authority, so it binds from there whatever
// a body says.
type destinationRef struct {
	// Platform is the destination to act on, from the path: ga4 | meta | tiktok |
	// linkedin | x | reddit | openai | insights.
	Platform string `json:"platform"`
}

// destinationList is every destination this deployment can forward to, each with
// the caller org's connection state.
type destinationList struct {
	// Destinations is one card per registered platform, in slug order.
	Destinations []DestinationStatus `json:"destinations"`
}

// destinationDisconnected acknowledges a disconnect.
type destinationDisconnected struct {
	// Disconnected is true when the credentials and the row are gone.
	Disconnected bool `json:"disconnected"`
}

// destinationTest is the outcome of one synthetic send, reported as DATA so the
// console renders a platform rejection rather than an HTTP error.
//
// Sent, Message and Error are POINTERS so each is absent exactly when the wire
// has never carried it: a failure answers {ok,error} and a success {ok,sent,
// message}, and a non-pointer field with `omitempty` would drop a real "sent": 0
// while a non-pointer without it would add "sent": 0 to every failure. The fields
// are in ALPHABETICAL order because the map this replaced marshalled its keys
// sorted, and the bytes on the wire must not move.
type destinationTest struct {
	// Error is the platform's rejection, present only on a failed send.
	Error *string `json:"error,omitempty"`
	// Message is the platform's own note about the send, present only on success.
	Message *string `json:"message,omitempty"`
	// OK is true when the platform accepted the synthetic event.
	OK bool `json:"ok"`
	// Sent is how many events the platform accepted, present only on success.
	Sent *int `json:"sent,omitempty"`
}

// list reports every destination this deployment can forward to, each with the
// caller org's connection state: whether it is connected, whether it is enabled,
// whether a credential resolves right now, and the config fields the console
// renders for it.
func (o ops) list(ctx context.Context, _ *cloud.Unit) (*destinationList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.List(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	byPlatform := make(map[string]Row, len(rows))
	for _, r := range rows {
		byPlatform[r.Platform] = r
	}
	out := make([]DestinationStatus, 0, len(o.s.State.dests))
	for _, id := range slices.Sorted(maps.Keys(o.s.State.dests)) {
		dest := o.s.State.dests[id]
		if r, ok := byPlatform[id]; ok {
			out = append(out, statusOf(o.s, ctx, org, dest, &r))
		} else {
			out = append(out, statusOf(o.s, ctx, org, dest, nil))
		}
	}
	return &destinationList{Destinations: out}, nil
}

// get reports one destination's card for the caller's org — its config fields,
// its connection state, and whether a credential resolves right now. A platform
// this deployment does not carry is not found.
//
// Example: {"platform": "ga4"}
func (o ops) get(ctx context.Context, in *destinationRef) (*DestinationStatus, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	dest, ok := o.s.State.dests[strings.TrimSpace(in.Platform)]
	if !ok {
		return nil, zip.ErrNotFound("unknown destination")
	}
	row, found, err := o.s.State.store.Get(ctx, org, dest.ID())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if found {
		st := statusOf(o.s, ctx, org, dest, &row)
		return &st, nil
	}
	st := statusOf(o.s, ctx, org, dest, nil)
	return &st, nil
}

// disconnect forgets a destination for the caller's org: every credential held in
// KMS, then the stored config. Idempotent, and it requires org admin.
//
// Example: {"platform": "ga4"}
func (o ops) disconnect(ctx context.Context, in *destinationRef) (*destinationDisconnected, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	dest, ok := o.s.State.dests[strings.TrimSpace(in.Platform)]
	if !ok {
		return nil, zip.ErrNotFound("unknown destination")
	}
	if !orgAdmin(ctx) {
		return nil, zip.ErrForbidden("disconnecting a destination requires org admin")
	}
	for _, name := range dest.Spec().Secrets {
		if err := kmsDelete(ctx, o.s, org, dest.ID(), name); err != nil {
			o.s.Log.Warn("destinations kms delete failed (continuing)", "platform", dest.ID(), "org", org, "secret", name, "err", err)
		}
	}
	if _, err := o.s.State.store.Delete(ctx, org, dest.ID()); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	return &destinationDisconnected{Disconnected: true}, nil
}

// test sends ONE synthetic conversion through the connected destination end to
// end and reports what the platform said. Where the platform can check one without
// recording it, it is a lead and nothing is kept: GA4 answers from its validation
// server, Google Ads validates without importing, and Meta files it under the
// connection's Test Event Code (and is refused without one). Anywhere else it is a
// page view. A send the platform refuses is reported as data — {"ok": false,
// "error": …} at 200 — so the console shows the platform's own words rather than
// an error about Hanzo. It requires org admin.
//
// Example: {"platform": "ga4"}
func (o ops) test(ctx context.Context, in *destinationRef) (*destinationTest, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	dest, ok := o.s.State.dests[strings.TrimSpace(in.Platform)]
	if !ok {
		return nil, zip.ErrNotFound("unknown destination")
	}
	if !orgAdmin(ctx) {
		return nil, zip.ErrForbidden("testing a destination requires org admin")
	}
	row, found, err := o.s.State.store.Get(ctx, org, dest.ID())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("destination not connected")
	}
	secret, err := resolveSecret(ctx, o.s, org, dest, row.Config)
	if err != nil {
		return nil, zip.ErrBadRequest("no credential is configured for this destination")
	}
	res, serr := dest.Send(ctx, row.Config, secret, []Conversion{syntheticConversion(org, o.s.Brand, dest.ID())})
	if serr != nil {
		msg := serr.Error()
		return &destinationTest{OK: false, Error: &msg}, nil
	}
	return &destinationTest{OK: true, Sent: &res.Sent, Message: &res.Message}, nil
}

// Shutdown closes the store and clears the sink. Idempotent.
func Shutdown() error {
	if running != nil {
		running.stop()
		running = nil
	}
	if mounted == nil || mounted.State.store == nil {
		mounted = nil
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// fanoutEnabled reports whether the analytics fan-out is on (default ON).
func fanoutEnabled() bool {
	switch strings.ToLower(env.Or(publicFanoutEnv, "")) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// ── shared helpers ───────────────────────────────────────────────────────────

func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

func platformParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("platform")) }

// toStr coerces a decoded JSON value to a trimmed string (numbers → their literal;
// non-scalars → ""). Lets the connect body carry an id as a string or a number.
func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// camelOf converts a snake_case KMS secret name to the camelCase connect-body key
// (api_secret → apiSecret), so the body reads naturally while KMS keeps snake names.
func camelOf(s string) string {
	parts := strings.Split(s, "_")
	if len(parts) == 1 {
		return s
	}
	var b strings.Builder
	b.WriteString(parts[0])
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

// ── KMS custody (per-org, sealed) ────────────────────────────────────────────

// kmsPath is the per-org, per-platform KMS namespace, and "" for an org that names
// no store. platform is a fixed registry slug — the caller looked it up in dests
// before reaching here, so only the org can be unusable.
func kmsPath(org, platform string) string { return kms.OrgPath(org, "destinations", platform) }

// kmsRef is one secret's ref on the secret plane: the platform's namespace and the
// secret's name, in the environment destinations seal under.
func kmsRef(org, platform, name string) string {
	return strings.TrimPrefix(kmsPath(org, platform), "/") + "/" + name + "@" + kmsEnv
}

// custody is ctx acting for org on the secret plane. The store serves
// orgs/<org>/… only to a call acting for that org (apps/kms attest.go). A request
// already acts for the org its principal named, and keeps its caller so the store
// can name who sealed or forgot a credential; the fan-out runs off the bus with no
// request behind it, and states the org.
func custody(ctx context.Context, org string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cloud.For(ctx, org), kmsTimeout)
}

// kmsTimeout bounds one secret-plane call.
const kmsTimeout = 10 * time.Second

func kmsGet(ctx context.Context, s *cloud.Service[state], org, platform, name string) ([]byte, error) {
	if s.State.kms == nil {
		return nil, kms.ErrMasterKeyMissing
	}
	c, cancel := custody(ctx, org)
	defer cancel()
	return s.State.kms.GetSecret(c, kmsRef(org, platform, name))
}

func kmsPut(ctx context.Context, s *cloud.Service[state], org, platform, name string, value []byte) error {
	if s.State.kms == nil {
		return kms.ErrMasterKeyMissing
	}
	c, cancel := custody(ctx, org)
	defer cancel()
	return s.State.kms.PutSecret(c, kmsRef(org, platform, name), value)
}

func kmsDelete(ctx context.Context, s *cloud.Service[state], org, platform, name string) error {
	if s.State.kms == nil {
		return kms.ErrMasterKeyMissing
	}
	c, cancel := custody(ctx, org)
	defer cancel()
	err := s.State.kms.DeleteSecret(c, kmsRef(org, platform, name))
	if errors.Is(err, secret.ErrNone) {
		return nil // idempotent
	}
	return err
}

// sealSecrets seals every provided secret for org's platform. Logs the secret NAME
// on failure, never a value.
func sealSecrets(ctx context.Context, s *cloud.Service[state], org, platform string, secrets map[string]string) error {
	for name, val := range secrets {
		if err := kmsPut(ctx, s, org, platform, name, []byte(val)); err != nil {
			s.Log.Warn("destinations kms seal failed", "org", org, "platform", platform, "secret", name, "err", err)
			return err
		}
	}
	return nil
}

// ── views ────────────────────────────────────────────────────────────────────

// DestinationStatus is a destination's card for an org: its Spec (fields the console renders),
// this org's connection state, and whether a credential is resolvable (live).
type DestinationStatus struct {
	Platform string `json:"platform"` // the platform slug, and the path segment every route addresses it by
	Name     string `json:"name"`     // the platform's display name ("Google Analytics 4")
	Category string `json:"category"` // groups the card: Analytics | Advertising
	// Connected is true when this org has a stored row for the platform — it has
	// been configured here at least once. It says nothing about whether a
	// credential still resolves; that is Live.
	Connected bool `json:"connected"`
	// Enabled is whether the fan-out forwards to this destination. False on a
	// destination that is connected but paused, and on one never connected.
	Enabled bool `json:"enabled"`
	// Live is whether a credential resolves RIGHT NOW: a KMS-sealed secret for this
	// org, else the integrations connection named by the platform's Fallback, else
	// no credential needed at all (a public-ingest sink like Analytics). False on a
	// connected destination whose secret has gone missing — Connected && !Live is
	// exactly the "reconnect me" state.
	Live bool `json:"live"`
	// Account is the operator's own label for the connected account, as supplied on
	// connect. Absent when unset.
	Account string `json:"account,omitempty"`
	// Config is the org's stored NON-SECRET configuration — the measurement/pixel
	// ids keyed by DestinationField.Key. A secret is never in here; secrets live in
	// KMS and only their names are published, in Secrets.
	Config Config `json:"config,omitempty"`
	// Fields are the non-secret inputs this platform needs, which the console card
	// renders and the connect body fills.
	Fields []DestinationField `json:"fields"`
	// Secrets are the KMS secret NAMES this platform custodies for the org — names
	// only, never values. The connect body accepts each under its camelCase form.
	Secrets []string `json:"secrets"`
	// Pixel is whether the hosted tag can inject a browser pixel for this platform,
	// so a console offers a per-SITE pixel input for exactly these. False means the
	// platform receives conversions server-side only, and an input would promise an
	// injection that never happens. Derived from the tag's own map (event.BrowserTags),
	// never restated — a second list is how a console offers a pixel nothing fires.
	Pixel bool `json:"pixel"`
}

// statusOf builds the card for a destination, folding in the org's live row (row may
// be nil when not connected). Live reflects whether a credential resolves NOW.
func statusOf(s *cloud.Service[state], ctx context.Context, org string, dest Destination, row *Row) DestinationStatus {
	spec := dest.Spec()
	st := DestinationStatus{
		Platform: dest.ID(), Name: dest.Name(), Category: dest.Category(),
		Fields: spec.Fields, Secrets: spec.Secrets,
		Pixel: event.HasPixel(dest.ID()),
	}
	if row != nil {
		st.Connected = true
		st.Enabled = row.Enabled
		st.Account = row.AccountLabel
		st.Config = row.Config
		_, err := resolveSecret(ctx, s, org, dest, row.Config)
		st.Live = err == nil
	}
	return st
}

// ── handlers ─────────────────────────────────────────────────────────────────

// connect provisions (or updates) a destination: non-secret ids into the store, API
// secret(s) sealed to KMS (fail-closed). Connecting an ad destination is an org-admin
// action (parity with the integrations AdminOnly discipline). The secret never
// appears in the response, the store, or a log line.
func connect(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	if kms.OrgPath(org) == "" {
		return zip.ErrBadRequest("org names no store")
	}
	dest, ok := s.State.dests[platformParam(c)]
	if !ok {
		return zip.ErrNotFound("unknown destination")
	}
	if !principal.IsOrgAdmin(c) {
		return zip.ErrForbidden("connecting a destination requires org admin")
	}
	if s.State.kms == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "%s", kms.ErrMasterKeyMissing.Error())
	}
	var body map[string]any
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	spec := dest.Spec()
	cfg := Config{}
	for _, f := range spec.Fields {
		v := text.Trim(toStr(body[f.Key]), maxField)
		if v == "" {
			if f.Required {
				return zip.ErrBadRequest(dest.ID() + ": " + f.Key + " is required")
			}
			continue
		}
		cfg[f.Key] = v
	}
	secrets := map[string]string{}
	for _, name := range spec.Secrets {
		v := cmp.Or(toStr(body[camelOf(name)]), toStr(body[name]))
		if v == "" {
			continue
		}
		if len(v) > maxSecret {
			return zip.ErrBadRequest("credential too large")
		}
		secrets[name] = v
	}
	if len(secrets) > 0 {
		if err := sealSecrets(c.Context(), s, org, dest.ID(), secrets); err != nil {
			return zip.Errorf(http.StatusServiceUnavailable, "secret custody failed")
		}
	}
	enabled := true
	if v, ok := body["enabled"].(bool); ok {
		enabled = v
	}
	row := Row{Org: org, Platform: dest.ID(), Enabled: enabled, Config: cfg, AccountLabel: text.Trim(toStr(body["account"]), maxField)}
	if err := s.State.store.Upsert(c.Context(), row); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	s.Log.Info("destination connected", "platform", dest.ID(), "org", org, "enabled", enabled)
	return c.JSON(http.StatusOK, statusOf(s, c.Context(), org, dest, &row))
}

// validating are the platforms with a mode that checks a conversion and records
// nothing: GA4's validation server, Google Ads' validateOnly, Meta's Test Events.
var validating = map[string]bool{ga4ID: true, googleadsID: true, metaID: true, openaiID: true}

// syntheticConversion builds the one test event. On a platform that can validate
// without recording it is a lead from a stable per-org test visitor at the brand's
// own test address (Google Ads validates a lead only when it carries something to
// match). Anywhere else it is the page view it has always been, the least a
// platform can be sent.
func syntheticConversion(org, brand, platform string) Conversion {
	host := strings.TrimSpace(brand)
	if host == "" {
		host = "hanzo"
	}
	cv := Conversion{
		Standard: EventPageView,
		Name:     "$pageview",
		EventID:  "hanzo_test_" + org + "_" + fmt.Sprint(time.Now().Unix()),
		Time:     time.Now(),
		URL:      "https://" + host + ".ai/",
		Consent:  Consent{Analytics: true, Marketing: true, Ads: true},
		User:     UserData{ExternalID: "hanzo-test-" + org},
	}
	if validating[platform] {
		cv.Standard, cv.Name, cv.Test = EventLead, "generate_lead", true
		cv.User.Email, cv.User.UserAgent = "test@"+host+".ai", "hanzo-cloud/1.0"
	}
	return cv
}

// ── in-process client (the guide MCP tool + siblings) ──────────────────────────

// Connect provisions (or updates) a destination's NON-SECRET config for org — the
// client the guide's destinations_connect MCP tool drives. It NEVER accepts a secret
// (secrets flow only via the authenticated HTTP connect body → KMS), so the tool args
// and the guide action ledger never carry one. It requires the destination's REQUIRED
// non-secret fields (the guide cannot fabricate a measurement/pixel id), returning an
// honest error otherwise, and reports whether the destination is now live. Fails
// closed when unmounted / invalid org / unknown platform.
func Connect(ctx context.Context, org, platform string, in map[string]any) (DestinationStatus, error) {
	if mounted == nil {
		return DestinationStatus{}, fmt.Errorf("destinations: not mounted")
	}
	s := mounted
	if kms.OrgPath(org) == "" {
		return DestinationStatus{}, fmt.Errorf("destinations: org names no store")
	}
	dest, ok := s.State.dests[strings.TrimSpace(platform)]
	if !ok {
		return DestinationStatus{}, fmt.Errorf("destinations: unknown platform %q", platform)
	}
	spec := dest.Spec()
	existing, found, err := s.State.store.Get(ctx, org, dest.ID())
	if err != nil {
		return DestinationStatus{}, err
	}
	cfg := Config{}
	if found {
		maps.Copy(cfg, existing.Config)
	}
	for _, f := range spec.Fields {
		if v := text.Trim(toStr(in[f.Key]), maxField); v != "" {
			cfg[f.Key] = v
		}
	}
	for _, f := range spec.Fields {
		if f.Required && cfg[f.Key] == "" {
			return DestinationStatus{}, fmt.Errorf("%s requires %s", dest.ID(), f.Key)
		}
	}
	row := Row{Org: org, Platform: dest.ID(), Enabled: true, Config: cfg}
	if found {
		row.AccountLabel = existing.AccountLabel
	}
	if err := s.State.store.Upsert(ctx, row); err != nil {
		return DestinationStatus{}, err
	}
	return statusOf(s, ctx, org, dest, &row), nil
}

// List returns the org's destination status for every registered platform — the client
// a sibling (the guide) reads to report what is connected. Fails closed when
// unmounted.
func List(ctx context.Context, org string) ([]DestinationStatus, error) {
	if mounted == nil {
		return nil, fmt.Errorf("destinations: not mounted")
	}
	s := mounted
	if kms.OrgPath(org) == "" {
		return nil, fmt.Errorf("destinations: org names no store")
	}
	rows, err := s.State.store.List(ctx, org)
	if err != nil {
		return nil, err
	}
	byPlatform := make(map[string]Row, len(rows))
	for _, r := range rows {
		byPlatform[r.Platform] = r
	}
	out := make([]DestinationStatus, 0, len(s.State.dests))
	for _, id := range slices.Sorted(maps.Keys(s.State.dests)) {
		dest := s.State.dests[id]
		if r, ok := byPlatform[id]; ok {
			out = append(out, statusOf(s, ctx, org, dest, &r))
		} else {
			out = append(out, statusOf(s, ctx, org, dest, nil))
		}
	}
	return out, nil
}
