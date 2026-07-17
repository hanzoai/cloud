package featuregate

// The launch-control gate — the COMPLETE waitlist feature, COMPOSING the ONE flag
// engine (clients/flags) one-way. Decomplected into the two orthogonal axes it always
// was, with a single decision plane:
//
//   - MODE (per service):  waitlist.<svc> IS a platform switch, evaluated through the
//     flag engine (flags.Bool / flags.SetPlatformSwitch / flags.Register). There is no
//     second mode store.
//   - HOST MAP + metadata:  the registry (registry.go) resolves a request host to the
//     service whose switch governs it, and carries display metadata.
//
// The decide is WaitlistModeForHost(host) → (mode, service, known): resolve host→svc,
// then read waitlist.<svc>. Enforce (middleware.go) consumes this decide; the admin
// board (/v1/admin/services) and the guard's runtime mode read (/v1/flags/waitlist,
// served here) read it too. Per-user approval (pending|approved) is the second,
// orthogonal axis — IAM's, in approval.go.
//
// flags NEVER imports this package; this package imports flags. That one-way arrow is
// the whole point of the decomplection: the engine is pure, the feature composes it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/flags"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The reserved platform tenant the launch registry rides in — the SAME reserved
// (org, project) the flag engine uses for its platform switches, so the registry and
// the waitlist.<svc> switches co-locate. One waitlist.db for the deployment.
const (
	platformOrg     = "platform"
	platformProject = "platform"
)

// registryState is featuregate's process-wide launch state: the platform-tenant
// host→service registry store + the deployment brand it was seeded for. Installed by
// Mount, torn down by Shutdown.
type registryState struct {
	store *cloud.OrgStore[*waitlistStore]
	brand string
}

var mounted *registryState

// SeedService is one row of the launch registry (a hosted service + its hosts). Mode
// is intentionally absent — the launch posture (gated) is waitlistDef's Default "true".
type SeedService struct {
	Service     string
	DisplayName string
	Description string
	Hosts       []string
}

// ServiceInput is the admin onboard/edit payload for /v1/admin/services. WaitlistMode
// sets the launch switch for a NEW service; a re-register PRESERVES the live switch.
type ServiceInput struct {
	Service      string   `json:"service"`
	DisplayName  string   `json:"displayName"`
	Description  string   `json:"description"`
	Hosts        []string `json:"hosts"`
	WaitlistMode bool     `json:"waitlistMode"`
}

// ServiceView is one service as the admin board renders it: the registry row plus its
// LIVE waitlist mode (the waitlist.<svc> switch evaluated through the engine).
type ServiceView struct {
	ServiceRow
	WaitlistMode bool `json:"waitlistMode"`
}

// waitlistKey is the ONE naming rule: a service's mode is the switch waitlist.<svc>.
func waitlistKey(svc string) string { return "waitlist." + strings.ToLower(strings.TrimSpace(svc)) }

// waitlistDef is the platform switch for one service's mode. Default "true" = the
// launch posture (gated until an admin opens it), so a deployment with no stored flag
// behaves exactly as the old featuregate seed (waitlistMode ON).
func waitlistDef(svc, display string) flags.Def {
	if strings.TrimSpace(display) == "" {
		display = svc
	}
	return flags.Def{
		Key:      waitlistKey(svc),
		Category: "Launch",
		Label:    "Waitlist · " + display,
		Desc:     "Waitlist mode for " + display + ": ON gates the service to APPROVED users; OFF opens it.",
		Type:     flags.TypeBool,
		Default:  "true",
	}
}

// ensureWaitlistDef registers a service's switch if it is not already registered
// (Mount registers the seed set with nicer labels; this covers runtime onboards).
func ensureWaitlistDef(svc, display string) {
	key := waitlistKey(svc)
	for _, d := range flags.Defs() {
		if d.Key == key {
			return
		}
	}
	flags.Register(waitlistDef(svc, display))
}

// boolDef is the minimal PostHog flag definition for a boolean switch value.
func boolDef(on bool) json.RawMessage {
	if on {
		return json.RawMessage(`{"active":true}`)
	}
	return json.RawMessage(`{"active":false}`)
}

// requireRegistry resolves the platform-tenant registry store, or an error when the
// gate is not mounted (writes need it; the decide fail-opens instead).
func requireRegistry() (*waitlistStore, error) {
	if mounted == nil || mounted.store == nil {
		return nil, fmt.Errorf("featuregate: waitlist registry not mounted")
	}
	return mounted.store.For(platformOrg, platformProject)
}

// WaitlistModeForHost is THE decide the Enforce consumer, /v1/flags/waitlist, and
// the admin board call: resolve host→service, then read the waitlist.<svc> switch
// through the flag engine. FAIL-OPEN by construction — an unmounted registry, a store
// error, or an un-governed host all return known=false, so a request is NEVER gated
// pre-boot or on a registry fault (availability over a hard gate, matching the guard).
func WaitlistModeForHost(ctx context.Context, host string) (mode bool, service string, known bool) {
	if mounted == nil || mounted.store == nil {
		return false, "", false
	}
	st, err := mounted.store.For(platformOrg, platformProject)
	if err != nil {
		return false, "", false
	}
	svc, known, err := st.ServiceForHost(ctx, host)
	if err != nil || !known {
		return false, "", false
	}
	return flags.Bool(waitlistKey(svc)), svc, true
}

// ListWaitlistServices returns the admin board: every registered service with its LIVE
// mode (the waitlist.<svc> switch). SuperAdmin surface (the caller gates).
func ListWaitlistServices(ctx context.Context) ([]ServiceView, error) {
	st, err := requireRegistry()
	if err != nil {
		return nil, err
	}
	rows, err := st.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ServiceView, 0, len(rows))
	for _, r := range rows {
		out = append(out, ServiceView{ServiceRow: r, WaitlistMode: flags.Bool(waitlistKey(r.Service))})
	}
	return out, nil
}

// SetWaitlistMode flips one service's waitlist switch — the launch lever — and returns
// the updated view. It is the ONE write path (through flags.SetPlatformSwitch, audited
// in the flag activity log); the flip is hot (this pod applies immediately, peers
// converge within the eval TTL). ErrServiceNotFound when the slug is unknown.
func SetWaitlistMode(ctx context.Context, service string, mode bool, actor string) (ServiceView, error) {
	service = strings.ToLower(strings.TrimSpace(service))
	if service == "" {
		return ServiceView{}, fmt.Errorf("featuregate: service is required")
	}
	st, err := requireRegistry()
	if err != nil {
		return ServiceView{}, err
	}
	row, err := st.Get(ctx, service) // ErrServiceNotFound → 404 upstream
	if err != nil {
		return ServiceView{}, err
	}
	ensureWaitlistDef(service, row.DisplayName)
	if err := flags.SetPlatformSwitch(waitlistKey(service), boolDef(mode), actor); err != nil {
		return ServiceView{}, err
	}
	return ServiceView{ServiceRow: row, WaitlistMode: flags.Bool(waitlistKey(service))}, nil
}

// UpsertWaitlistService onboards or edits a hosted service so a new host is governed
// WITHOUT a redeploy. A NEW service takes in.WaitlistMode as its launch mode; a
// re-register PRESERVES the live switch (never silently re-gating an opened service).
func UpsertWaitlistService(ctx context.Context, in ServiceInput, actor string) (ServiceView, error) {
	svc := strings.ToLower(strings.TrimSpace(in.Service))
	if svc == "" {
		return ServiceView{}, fmt.Errorf("featuregate: service slug is required")
	}
	st, err := requireRegistry()
	if err != nil {
		return ServiceView{}, err
	}
	_, getErr := st.Get(ctx, svc)
	isNew := errors.Is(getErr, ErrServiceNotFound)
	if getErr != nil && !isNew {
		return ServiceView{}, getErr
	}
	row, err := st.Upsert(ctx, ServiceRow{
		Service:     svc,
		DisplayName: in.DisplayName,
		Description: in.Description,
		Hosts:       in.Hosts,
	}, actor, time.Now().Unix())
	if err != nil {
		return ServiceView{}, err
	}
	ensureWaitlistDef(svc, row.DisplayName)
	if isNew {
		if err := flags.SetPlatformSwitch(waitlistKey(svc), boolDef(in.WaitlistMode), actor); err != nil {
			return ServiceView{}, err
		}
	}
	return ServiceView{ServiceRow: row, WaitlistMode: flags.Bool(waitlistKey(svc))}, nil
}

// seedRegistry seeds the registry and registers a waitlist.<svc> switch per known
// service, COMPOSING the flag engine (flags.Register). Best-effort + fail-safe: a
// registry error (e.g. cek master key not yet injected) degrades to the in-memory seed
// switches — the decide then fail-opens, exactly the flag engine's own boot posture.
// Returns the number of seeded services (for the mount log). Called from Mount.
func seedRegistry(brand string, log luxlog.Logger) int {
	seed := seedWaitlist(brand)
	for _, sv := range seed { // in-memory switches — always succeeds
		flags.Register(waitlistDef(sv.Service, sv.DisplayName))
	}
	st, err := mounted.store.For(platformOrg, platformProject)
	if err != nil {
		log.Warn("waitlist registry unavailable — modes degrade to seed defaults", "err", err)
		return len(seed)
	}
	if _, err := st.Seed(context.Background(), seed, time.Now().Unix()); err != nil {
		log.Warn("waitlist registry seed failed", "err", err)
		return len(seed)
	}
	if rows, err := st.List(context.Background()); err == nil {
		for _, r := range rows { // register any persisted onboard beyond the seed
			ensureWaitlistDef(r.Service, r.DisplayName)
		}
	}
	return len(seed)
}

// waitlistModeRoute answers GET /v1/flags/waitlist?host=<h> — the runtime lookup the
// @file waitlist-guard caches. Public (in-cluster) read: it returns ONLY the boolean
// mode for the ONE queried host, never an enumeration.
func waitlistModeRoute(c *zip.Ctx) error {
	host := strings.TrimSpace(c.Query("host"))
	if host == "" {
		host = c.Fiber().Hostname()
	}
	mode, service, known := WaitlistModeForHost(c.Context(), host)
	return c.JSON(http.StatusOK, map[string]any{
		"host":         NormalizeHost(host),
		"service":      service,
		"waitlistMode": mode,
		"known":        known,
	})
}

// ── lifecycle ────────────────────────────────────────────────────────────────

// Mount installs the launch-control gate: it opens the platform-tenant host→service
// registry, seeds it for the deployment brand, registers a waitlist.<svc> switch per
// service in the flag engine (flags.Register), and serves the guard's public mode read
// at the CURRENT frozen path (/v1/flags/waitlist) plus the /v1/featuregate/mode compat
// alias. Fail-safe: a registry error (e.g. cek master key not yet injected) degrades to
// the in-memory seed switches — WaitlistModeForHost then fail-opens. Mounts AFTER flags
// so the engine's platform-switch plane is installed first.
func Mount(app *zip.App, deps cloud.Deps) error {
	if deps.Logger == nil {
		return fmt.Errorf("featuregate.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("featuregate.Mount: empty deps.DataDir")
	}
	log := deps.Logger.New("subsystem", "featuregate")
	mounted = &registryState{
		store: cloud.NewOrgStore[*waitlistStore](deps.DataDir, "waitlist", openWaitlistStore),
		brand: deps.Brand,
	}
	n := seedRegistry(deps.Brand, log)
	// The guard's public runtime mode read (host→service→waitlist.<svc>), one namespace
	// under /v1/flags. Exempt from the Enforce gate (see defaultExemptPrefixes) so a
	// gated user can still resolve mode.
	app.Get("/v1/flags/waitlist", waitlistModeRoute)
	// Compat alias (TEMPORARY): the former /v1/featuregate/mode path, same handler — kept
	// only so an unverified external caller can't 404 while the namespace collapse rolls
	// out. Delete this (and its exempt entry in middleware.go) once every caller is
	// confirmed on /v1/flags/waitlist.
	app.Get("/v1/featuregate/mode", waitlistModeRoute)
	log.Info("featuregate launch gate ready", "services", n)
	return nil
}

// Shutdown closes the launch registry's per-org store handles.
func Shutdown() error {
	if mounted == nil || mounted.store == nil {
		return nil
	}
	return mounted.store.CloseAll()
}

// ── brand seed (moved verbatim from the former flags/waitlist.go) ────────────────

// seedWaitlist returns the launch registry for a brand. White-labeled so a Lux/Zoo/Pars
// deployment governs its OWN hosts. New hosted services onboard at runtime via
// POST /v1/admin/services (no redeploy). admin.<brand> is deliberately NOT seeded (it
// is admin-guarded, not a waitlist surface).
func seedWaitlist(brand string) []SeedService {
	d := domainFor(brand)
	return []SeedService{
		{Service: "studio", DisplayName: "Studio", Description: "AI app studio", Hosts: []string{"studio." + d}},
		{Service: "chat", DisplayName: "Chat", Description: "AI chat", Hosts: hostsFor(brand, "chat", "chat."+d)},
		{Service: "console", DisplayName: "Console", Description: "Cloud console", Hosts: []string{"console." + d}},
		{Service: "app", DisplayName: "App", Description: "App builder", Hosts: hostsFor(brand, "app", "app."+d)},
		{Service: "api", DisplayName: "API", Description: "Inference API gateway", Hosts: []string{"api." + d}},
		{Service: "team", DisplayName: "Team", Description: "Team workspace", Hosts: hostsFor(brand, "team", "team."+d)},
	}
}

// domainFor maps a brand to its primary domain. Defaults to hanzo.ai.
func domainFor(brand string) string {
	switch strings.ToLower(strings.TrimSpace(brand)) {
	case "lux":
		return "lux.network"
	case "zoo":
		return "zoo.ngo"
	case "pars":
		return "pars.network"
	default:
		return "hanzo.ai"
	}
}

// hostsFor returns the apex-brand host (hanzo.chat / zoo.chat style) plus the
// <label>.<domain> alias when the brand ships an apex-label domain; else just the alias.
func hostsFor(brand, label, alias string) []string {
	switch strings.ToLower(strings.TrimSpace(brand)) {
	case "", "hanzo":
		return []string{"hanzo." + label, alias}
	case "zoo":
		return []string{"zoo." + label, alias}
	default:
		return []string{alias}
	}
}
