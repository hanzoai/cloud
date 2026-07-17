package flags

// The waitlist LENS on the ONE flag engine — the launch-control plane folded in from
// the former clients/featuregate. Decomplected into the two orthogonal axes it always
// was, now with a single decision plane:
//
//   - MODE (per service):  waitlist.<svc> IS a platform switch, evaluated through the
//     SAME native engine as every other platform flag. There is no second mode store.
//   - HOST MAP + metadata:  the registry (waitlist_store.go) resolves a request host
//     to the service whose switch governs it, and carries display metadata.
//
// The decide is WaitlistModeForHost(host) → (mode, service, known): resolve host→svc,
// then read waitlist.<svc>. featuregate.Enforce is now a CONSUMER of this decide, and
// /v1/flags/waitlist + the /v1/admin/services board read it too. Per-user approval
// (pending|approved) stays IAM's (featuregate/approval.go) — the second, orthogonal
// axis, unchanged.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

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
func waitlistDef(svc, display string) Def {
	if strings.TrimSpace(display) == "" {
		display = svc
	}
	return Def{
		Key:      waitlistKey(svc),
		Category: "Launch",
		Label:    "Waitlist · " + display,
		Desc:     "Waitlist mode for " + display + ": ON gates the service to APPROVED users; OFF opens it.",
		Type:     TypeBool,
		Default:  "true",
	}
}

// ensureWaitlistDef registers a service's switch if it is not already registered
// (Mount registers the seed set with nicer labels; this covers runtime onboards).
func ensureWaitlistDef(svc, display string) {
	if _, ok := lookupDef(waitlistKey(svc)); !ok {
		Register(waitlistDef(svc, display))
	}
}

// boolDef is the minimal PostHog flag definition for a boolean switch value.
func boolDef(on bool) json.RawMessage {
	if on {
		return json.RawMessage(`{"active":true}`)
	}
	return json.RawMessage(`{"active":false}`)
}

// requireRegistry resolves the platform-tenant registry store, or an error when the
// engine is not mounted (writes need it; the decide fail-opens instead).
func requireRegistry() (*waitlistStore, error) {
	c := mounted
	if c == nil || c.registry == nil {
		return nil, fmt.Errorf("flags: waitlist registry not mounted")
	}
	return c.registry.For(platformOrg, platformProject)
}

// WaitlistModeForHost is THE decide the Enforce consumer, /v1/flags/waitlist, and
// the admin board call: resolve host→service, then read the waitlist.<svc> switch
// through the engine. FAIL-OPEN by construction — an unmounted registry, a store
// error, or an un-governed host all return known=false, so a request is NEVER gated
// pre-boot or on a registry fault (availability over a hard gate, matching the guard).
func WaitlistModeForHost(ctx context.Context, host string) (mode bool, service string, known bool) {
	c := mounted
	if c == nil || c.registry == nil {
		return false, "", false
	}
	st, err := c.registry.For(platformOrg, platformProject)
	if err != nil {
		return false, "", false
	}
	svc, known, err := st.ServiceForHost(ctx, host)
	if err != nil || !known {
		return false, "", false
	}
	return Bool(waitlistKey(svc)), svc, true
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
		out = append(out, ServiceView{ServiceRow: r, WaitlistMode: Bool(waitlistKey(r.Service))})
	}
	return out, nil
}

// SetWaitlistMode flips one service's waitlist switch — the launch lever — and returns
// the updated view. It is the ONE write path (through SetPlatformSwitch, audited in the
// flag activity log); the flip is hot (this pod applies immediately, peers converge
// within the eval TTL). ErrServiceNotFound when the slug is unknown.
func SetWaitlistMode(ctx context.Context, service string, mode bool, actor string) (ServiceView, error) {
	service = strings.ToLower(strings.TrimSpace(service))
	if service == "" {
		return ServiceView{}, fmt.Errorf("flags: service is required")
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
	if err := SetPlatformSwitch(waitlistKey(service), boolDef(mode), actor); err != nil {
		return ServiceView{}, err
	}
	return ServiceView{ServiceRow: row, WaitlistMode: Bool(waitlistKey(service))}, nil
}

// UpsertWaitlistService onboards or edits a hosted service so a new host is governed
// WITHOUT a redeploy. A NEW service takes in.WaitlistMode as its launch mode; a
// re-register PRESERVES the live switch (never silently re-gating an opened service).
func UpsertWaitlistService(ctx context.Context, in ServiceInput, actor string) (ServiceView, error) {
	svc := strings.ToLower(strings.TrimSpace(in.Service))
	if svc == "" {
		return ServiceView{}, fmt.Errorf("flags: service slug is required")
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
		if err := SetPlatformSwitch(waitlistKey(svc), boolDef(in.WaitlistMode), actor); err != nil {
			return ServiceView{}, err
		}
	}
	return ServiceView{ServiceRow: row, WaitlistMode: Bool(waitlistKey(svc))}, nil
}

// mountWaitlist seeds the registry and registers a waitlist.<svc> switch per known
// service. Best-effort + fail-safe: a registry error (e.g. cek master key not yet
// injected) degrades to the in-memory seed switches — the decide then fail-opens,
// exactly the flag engine's own boot posture. Called from Mount.
func mountWaitlist(c *Client, brand string, log luxlog.Logger) {
	seed := seedWaitlist(brand)
	for _, sv := range seed { // in-memory switches — always succeeds
		Register(waitlistDef(sv.Service, sv.DisplayName))
	}
	st, err := c.registry.For(platformOrg, platformProject)
	if err != nil {
		log.Warn("waitlist registry unavailable — modes degrade to seed defaults", "err", err)
		return
	}
	if _, err := st.Seed(context.Background(), seed, time.Now().Unix()); err != nil {
		log.Warn("waitlist registry seed failed", "err", err)
		return
	}
	if rows, err := st.List(context.Background()); err == nil {
		for _, r := range rows { // register any persisted onboard beyond the seed
			ensureWaitlistDef(r.Service, r.DisplayName)
		}
	}
}

// waitlistModeRoute answers GET /v1/flags/waitlist?host=<h> — the runtime lookup the
// @file waitlist-guard caches. Public (in-cluster) read: it returns ONLY the boolean
// mode for the ONE queried host, never an enumeration. Same wire shape as the former
// featuregate route, so the interim guard ports 1:1.
func waitlistModeRoute(_ *cloud.Service[state], c *zip.Ctx) error {
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

// ── brand seed (moved verbatim from the former featuregate/seed.go) ──────────────

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
