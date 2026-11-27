package campaign

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/text"
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
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("campaign: nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("campaign.Use:  empty DataDir")
	}
	store, err := openStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("campaign.Use:  open store: %w", err)
	}
	b := cloud.NewBase(deps, "campaign")
	s := &cloud.Service[state]{Base: b, State: state{store: store}}
	mounted = s

	routes(app, s)

	b.Log.Info("campaign mounted", "brand", cloud.Brand(), "channels", registeredKinds())
	return nil
}

// routes registers the campaign surface. Registration order IS match order (zip
// is first-match): the static /summary is registered before /:id so it is never
// captured by the id param, and the deeper /:id/… routes have a distinct segment
// count so none shadows another.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	// A typed op receives only a context, so the validated org it reads is parked
	// there by cloud.Bridge — never carried as an In field, which is
	// caller-supplied and would be a cross-tenant read the caller asserted for
	// itself. This subsystem does not install the bridge: the program's composer
	// does, once at the root, after the identity check that mints the org and
	// before any subsystem registers a route — an order only the composer can
	// hold.

	g := app.Group("/v1/campaign")
	g.Get("/summary", o.summary)

	// The empty leaf is the group's own address, so these two are /v1/campaign.
	g.Get("", o.list)
	g.Post("", o.create, zip.WithStatus(http.StatusCreated))

	g.Get("/:id", o.get)
	g.Put("/:id", o.update)
	g.Delete("/:id", o.del)

	// UNTYPED BY DESIGN — neither has ever read a request body, and zip's invoke
	// refuses one it cannot parse before the handler runs, so typing them would
	// turn today's 200 into a 400 for a caller that posts junk to a route that
	// ignores it. typed.go states the measurement and the zip-side fix.
	g.Raw(http.MethodPost, "/:id/launch", cloud.Handle(s, launchCampaign))
	g.Raw(http.MethodPost, "/:id/pause", cloud.Handle(s, pauseCampaign))
	g.Get("/:id/metrics", o.metrics)

	g.Post("/:id/channels", o.addChannel)
	g.Delete("/:id/channels/:kind", o.removeChannel)
}

// ---- shared helpers (mirror clients/ads) ----

// tenant resolves the org — the tenant-isolation KEY — for a request, EXACTLY as
// SanitizeIdentity minted it from the validated IAM owner claim (HIP-0026).
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

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
		v = text.Trim(v, maxField)
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
		Platform: strings.ToLower(text.Trim(in.Platform, maxField)),
		Account:  text.Trim(in.Account, maxField),
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
