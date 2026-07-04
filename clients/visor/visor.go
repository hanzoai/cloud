// Package visor mounts the Hanzo Cloud COMPUTE surface: the tenant's machines,
// GPUs and DOKS clusters, served as clean REST off the unified cloud binary and
// fronting Visor (the cloud OS at visor.hanzo.svc that OWNS compute). It exists so
// the console's Machines / GPUs / Clusters pages read real per-org compute from
// ONE place (api.hanzo.ai/v1/*) instead of the god-mode /paas admin proxy that
// 501s until a service token is wired.
//
// This subsystem OWNS no compute state — Visor does. It is a thin, tenant-scoped
// translator: it maps Visor's verb-style + resell endpoints to the clean REST the
// console already speaks, and re-shapes Visor's objects into the exact JSON the
// console normalizers consume (see types.go). It never fabricates: a GPU row is a
// real GPU machine's accelerator, a cluster is real node pools, and telemetry
// Visor does not carry is honestly omitted (renders "—"), not invented.
//
// Surface (every route org-scoped by the validated principal; HIP-0026):
//
//	GET    /v1/machines                          list the org's machines        -> {machines:[machineView]}
//	POST   /v1/machines                          launch (or dryRun quote)       -> machineView | quote
//	GET    /v1/machines/:id                       one machine by name            -> machineView (404 if absent)
//	DELETE /v1/machines/:id                       terminate a machine            -> 204
//	GET    /v1/gpus                              per-accelerator inventory      -> {gpus:[gpuView]}
//	GET    /v1/gpus/alerts                       GPU alerts (honest empty)      -> {alerts:[]}
//	GET    /v1/clusters                          DOKS clusters (from pools)     -> {clusters:[clusterView]}
//	POST   /v1/clusters/:clusterId/pools          add a node pool                -> nodePoolView
//	POST   /v1/clusters/:clusterId/pools/:poolId/scale  scale a node pool        -> nodePoolView
//	DELETE /v1/clusters/:clusterId/pools/:poolId   delete a node pool             -> 204
//	POST   /v1/machines/:id/bind-agent            bind a cloud Agent to a machine -> agentBinding
//	GET    /v1/machines/:id/agent-binding          the machine's agent binding    -> agentBinding (404 if none)
//	DELETE /v1/machines/:id/agent-binding          unbind the agent               -> 204
//	GET    /v1/agent-bindings                     the org's agent bindings        -> {agentBindings:[agentBinding]}
//	GET    /v1/bots                               the org's bots (kind=bot)       -> {bots:[botView]}
//	POST   /v1/bots/launch                        launch a bot (machine+bind)     -> botView | quote
//	GET    /v1/bots/:id                            one bot by id                  -> botView (404 if not a bot)
//	DELETE /v1/bots/:id                            terminate a bot (unbind+delete) -> 204
//	POST   /v1/bots/:id/:action                   stop|pause|message the bot      -> action result
//
// The tenant (principal.Tenant) is passed to Visor as ?owner=<org>, so a caller
// can only ever read or mutate their OWN tenant's compute; the org is taken from
// the validated IAM owner claim, never a client field.
package visor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type svc struct {
	cl  *client
	log luxlog.Logger
}

// Mount wires the compute surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("visor.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("visor.Mount: nil deps.Logger")
	}
	s := &svc{cl: newClient(), log: deps.Logger.New("subsystem", "visor")}

	// Static routes register before their :param siblings so Fiber's
	// registration-order match never lets a machine/cluster id capture a literal.
	app.Get("/v1/machines", s.listMachines)
	app.Post("/v1/machines", s.launchMachine)
	app.Get("/v1/machines/:id", s.getMachine)
	app.Delete("/v1/machines/:id", s.deleteMachine)

	app.Get("/v1/gpus/alerts", s.gpuAlerts)
	app.Get("/v1/gpus", s.listGPUs)

	// BYO fleet: the org's bring-your-own machines that dialed in via
	// `hanzo gpu connect`. Raw list here; the same workers are folded into
	// /v1/machines and /v1/gpus above (provider="byo") so the console's existing
	// pages show them alongside Visor-provisioned compute.
	app.Get("/v1/fleet/workers", s.listFleetWorkers)

	app.Get("/v1/clusters", s.listClusters)
	app.Post("/v1/clusters/:clusterId/pools", s.createPool)
	app.Post("/v1/clusters/:clusterId/pools/:poolId/scale", s.scalePool)
	app.Delete("/v1/clusters/:clusterId/pools/:poolId", s.deletePool)

	// Compute catalog: the global region + size lists that back the Machines/GPUs
	// launch drawer. Namespaced under /v1/compute (visor's domain) — "sizes"/"regions"
	// are catalog dimensions shared by machines AND gpus, not owned nouns, so they
	// nest under compute rather than sitting bare at top level. Org-gated but not
	// org-scoped — the catalog is identical for every tenant.
	app.Get("/v1/compute/regions", s.listRegions)
	app.Get("/v1/compute/sizes", s.listSizes)

	// Agent↔machine binding — thin proxy over vm's binding surface (mark a machine
	// as running the @hanzo/bot runtime for a cloud Agent). Deeper than
	// /v1/machines/:id so no machine id captures these literals. See bots.go.
	app.Post("/v1/machines/:id/bind-agent", s.bindMachineAgent)
	app.Get("/v1/machines/:id/agent-binding", s.getMachineAgentBinding)
	app.Delete("/v1/machines/:id/agent-binding", s.unbindMachineAgent)
	app.Get("/v1/agent-bindings", s.listAgentBindings)

	// Bots — a Bot is a kind=bot machine + an agent binding, composed from the vm
	// compute + binding surface (bots.go). The sibling of /v1/machines. launch is
	// an explicit literal, registered before :id so it never binds as an id.
	app.Get("/v1/bots", s.listBots)
	app.Post("/v1/bots/launch", s.launchBot)
	app.Get("/v1/bots/:id", s.getBot)
	app.Delete("/v1/bots/:id", s.deleteBot)
	app.Post("/v1/bots/:id/:action", s.botAction)

	s.log.Info("visor compute surface mounted", "target", s.cl.target,
		"serviceAuth", serviceClientID() != "", "brand", deps.Brand)
	return nil
}

func init() {
	cloud.Register("visor", 133, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("visor.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// tenant resolves the org — the tenant-isolation KEY, taken verbatim from the
// validated IAM owner claim (principal.Tenant). It is what this client sends to
// Visor as ?owner, so a caller can never read or mutate another tenant's compute.
func tenant(c *zip.Ctx) (string, bool) { return principal.Tenant(c) }

// ---- machines ----

func (s *svc) listMachines(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var machines []visorMachine
	if err := s.cl.call(c, http.MethodGet, "/v1/get-machines", q("owner", org), nil, &machines); err != nil {
		// Visor (the DOKS/reseller provider) is unreachable — do not hide the
		// tenant's OWN bring-your-own compute behind an upstream blip. Log and
		// fall through with no Visor rows; the BYO fold-in below still lists the
		// dialed-in GPUs. In production Visor is up and both sets return.
		s.log.Warn("visor get-machines failed; returning BYO-only machine list", "org", org, "err", err)
		machines = nil
	}
	out := make([]machineView, 0, len(machines))
	for _, m := range machines {
		out = append(out, toMachineView(m))
	}
	// Fold in the org's BYO machines (provider="byo") so the console's Machines
	// page shows dialed-in GPUs next to Visor-provisioned ones.
	for _, w := range byoWorkers(org) {
		out = append(out, byoMachineView(w))
	}
	return c.JSON(http.StatusOK, map[string]any{"machines": out})
}

func (s *svc) getMachine(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := strings.TrimSpace(c.Param("id"))
	if name == "" {
		return zip.ErrBadRequest("machine id required")
	}
	var m visorMachine
	// Visor keys a machine by owner/name; the REST :id is the org-scoped name.
	if err := s.cl.call(c, http.MethodGet, "/v1/get-machine", q("id", org+"/"+name), nil, &m); err != nil {
		return err
	}
	if m.Name == "" && m.Id == "" {
		return zip.ErrNotFound("machine not found")
	}
	return c.JSON(http.StatusOK, toMachineView(m))
}

// ---- compute catalog (regions / sizes) ----

// listRegions and listSizes expose the global compute catalog that backs the launch
// drawer. Both delegate to catalog: DRY, one org-gated passthrough of Visor's
// authoritative list. GET /v1/compute/regions, GET /v1/compute/sizes.
func (s *svc) listRegions(c *zip.Ctx) error { return s.catalog(c, "/v1/regions") }
func (s *svc) listSizes(c *zip.Ctx) error   { return s.catalog(c, "/v1/sizes") }

// catalog proxies a global (non-org-scoped) Visor catalog list. Org-gated so only a
// validated principal reaches it, but no ?owner is forwarded — the region/size catalog
// is identical for every tenant. The Visor payload is passed through verbatim so the
// wire shape stays the single source of truth.
func (s *svc) catalog(c *zip.Ctx, upstream string) error {
	if _, ok := tenant(c); !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var data json.RawMessage
	if err := s.cl.call(c, http.MethodGet, upstream, "", nil, &data); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, data)
}

type launchReq struct {
	Name         string `json:"name"`
	Size         string `json:"size"`
	InstanceType string `json:"instanceType"`
	Region       string `json:"region"`
	DryRun       bool   `json:"dryRun"`
}

// launchMachine quotes (dryRun) or launches a metered, per-org machine. It fronts
// Visor's resell launch (/v1/machines/launch), which owns the balance gate and
// per-hour metering — cloud never bills compute itself; it forwards the tenant.
// A dryRun returns Visor's price quote verbatim (spends nothing); a real launch
// returns the launched machine as a clean machineView.
func (s *svc) launchMachine(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body launchReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if strings.TrimSpace(firstNonEmpty(body.Size, body.InstanceType)) == "" {
		return zip.ErrBadRequest("size is required")
	}
	var data json.RawMessage
	if err := s.cl.call(c, http.MethodPost, "/v1/machines/launch", q("owner", org), body, &data); err != nil {
		return err
	}
	// dryRun: pass Visor's quote through unchanged (it is the authoritative price).
	if body.DryRun {
		var quote any
		if len(data) > 0 {
			_ = json.Unmarshal(data, &quote)
		}
		return c.JSON(http.StatusOK, quote)
	}
	// Real launch: Visor returns {machine, quote[, meteringError]} — extract the
	// machine and emit the clean view (fall back to data-as-machine if unwrapped).
	var wrap struct {
		Machine visorMachine `json:"machine"`
	}
	_ = json.Unmarshal(data, &wrap)
	if wrap.Machine.Name == "" && wrap.Machine.Id == "" {
		_ = json.Unmarshal(data, &wrap.Machine)
	}
	return c.JSON(http.StatusCreated, toMachineView(wrap.Machine))
}

func (s *svc) deleteMachine(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name := strings.TrimSpace(c.Param("id"))
	if name == "" {
		return zip.ErrBadRequest("machine id required")
	}
	// Visor delete-machine takes the machine identity in the body (owner+name).
	body := map[string]string{"owner": org, "name": name}
	if err := s.cl.call(c, http.MethodPost, "/v1/delete-machine", "", body, nil); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- GPUs (derived from the org's real GPU machines) ----

func (s *svc) listGPUs(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var machines []visorMachine
	if err := s.cl.call(c, http.MethodGet, "/v1/get-machines", q("owner", org), nil, &machines); err != nil {
		// Resilient union (see listMachines): a Visor outage must not hide the
		// tenant's BYO accelerators. Log and continue with the BYO fold-in.
		s.log.Warn("visor get-machines failed; returning BYO-only gpu list", "org", org, "err", err)
		machines = nil
	}
	out := make([]gpuView, 0)
	for _, m := range machines {
		out = append(out, gpusFromMachine(m)...)
	}
	// Fold in the org's BYO accelerators (provider="byo") so the console's GPUs
	// page lists dialed-in cards with their real model + VRAM.
	for _, w := range byoWorkers(org) {
		out = append(out, byoGPUViews(w)...)
	}
	return c.JSON(http.StatusOK, map[string]any{"gpus": out})
}

// gpuAlerts is an HONEST empty surface: Visor exposes no GPU alert inventory, so
// this returns [] rather than fabricating alerts. It stays a real, tenant-gated
// route so the console's alerts fetch resolves (200 [], not a 404) — an honest
// "no alerts", the same discipline the rest of the surface follows.
func (s *svc) gpuAlerts(c *zip.Ctx) error {
	if _, ok := tenant(c); !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	return c.JSON(http.StatusOK, map[string]any{"alerts": []any{}})
}

// ---- clusters (DOKS clusters, projected from Visor node pools) ----

func (s *svc) listClusters(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var pools []visorNodePool
	if err := s.cl.call(c, http.MethodGet, "/v1/get-node-pools", q("owner", org), nil, &pools); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"clusters": clustersFromPools(pools)})
}

type poolReq struct {
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	Size      string `json:"size"`
	Count     int    `json:"count"`
	MinNodes  int    `json:"minNodes"`
	MaxNodes  int    `json:"maxNodes"`
	AutoScale bool   `json:"autoScale"`
}

func (s *svc) createPool(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	clusterID := strings.TrimSpace(c.Param("clusterId"))
	var body poolReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	provider := firstNonEmpty(body.Provider, c.Query("provider"))
	if provider == "" {
		return zip.ErrBadRequest("provider is required")
	}
	// Forward only the CreateNodePoolSpec fields; provider/clusterId/owner ride in
	// the query exactly as Visor's create-node-pool expects.
	spec := map[string]any{
		"name": body.Name, "size": body.Size, "count": body.Count,
		"minNodes": body.MinNodes, "maxNodes": body.MaxNodes, "autoScale": body.AutoScale,
	}
	var pool visorNodePool
	if err := s.cl.call(c, http.MethodPost, "/v1/create-node-pool",
		q("owner", org, "provider", provider, "clusterId", clusterID), spec, &pool); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, toNodePoolView(pool))
}

type scaleReq struct {
	Provider string `json:"provider"`
	Count    int    `json:"count"`
}

func (s *svc) scalePool(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	clusterID := strings.TrimSpace(c.Param("clusterId"))
	poolID := strings.TrimSpace(c.Param("poolId"))
	if poolID == "" {
		return zip.ErrBadRequest("poolId required")
	}
	var body scaleReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	provider := firstNonEmpty(body.Provider, c.Query("provider"))
	if provider == "" {
		return zip.ErrBadRequest("provider is required")
	}
	if body.Count < 0 {
		return zip.ErrBadRequest("count must be non-negative")
	}
	var pool visorNodePool
	if err := s.cl.call(c, http.MethodPost, "/v1/scale-node-pool",
		q("owner", org, "provider", provider, "clusterId", clusterID, "poolId", poolID, "count", strconv.Itoa(body.Count)),
		nil, &pool); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, toNodePoolView(pool))
}

func (s *svc) deletePool(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	clusterID := strings.TrimSpace(c.Param("clusterId"))
	poolID := strings.TrimSpace(c.Param("poolId"))
	if poolID == "" {
		return zip.ErrBadRequest("poolId required")
	}
	provider := firstNonEmpty(c.Query("provider"))
	if provider == "" {
		return zip.ErrBadRequest("provider is required")
	}
	// delete-node-pool takes the pool identity in the body; owner scopes it to the
	// caller's tenant and provider+clusterId drive the DOKS-side delete.
	body := map[string]any{
		"owner": org, "name": poolID, "poolId": poolID,
		"provider": provider, "clusterId": clusterID,
	}
	if err := s.cl.call(c, http.MethodPost, "/v1/delete-node-pool", "", body, nil); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}
