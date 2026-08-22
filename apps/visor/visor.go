// Package visor is the compute you rent from Hanzo: machines, GPUs and clusters
// — launch one, resize it, tear it down.
//
// It serves the tenant's machines, GPUs and DOKS clusters as clean REST off the
// unified cloud binary, fronting Visor (the cloud OS at visor.hanzo.svc that
// OWNS compute). It exists so
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
// THE SURFACE DESCRIBES ITSELF. Every route with a request/response shape is a
// TYPED op (zip.Get/Post/Delete[In, Out]) registered in Mount, so the route, its
// In/Out schema, the prose on its handler and the doc comment on every field are
// ONE declaration projected into REST, /.well-known/openapi.json, the /mcp tool
// list and the CLI. There is deliberately no route table in this comment: a
// hand-kept list is a second copy, and the one that used to live here had already
// drifted — it was missing the whole /v1/visor/fleet and /v1/visor/k8s
// surface. Read Mount, or ask the running deployment.
//
// Five routes stay raw, each because it has no shape to state rather than because
// nobody got to it: the two launches are polymorphic on the wire (a dryRun quote
// or a created resource), the two catalog reads pass Visor's payload through
// verbatim, and the bot action verb streams the agent's answer back untouched.
// Each carries a note at its registration.
//
// The tenant (principal.Org) is passed to Visor as ?owner=<org>, so a caller
// can only ever read or mutate their OWN tenant's compute; the org is taken from
// the validated IAM owner claim, never a client field.
package visor

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/fleet"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// state is visor's own data; the shared deps (logger, brand) live in the embedded
// cloud.Base (reached as s.Log / s.Brand). bill is KEPT here on purpose: it is the
// "compute"-provider meter (the commerce attribution + spend-cap scope key), which
// is deliberately distinct from the subsystem's own Base.Bill, so it is NOT lifted.
type state struct {
	cl *client
	// fleet is the shared per-org BYO-cluster registry (KMS-sealed kubeconfigs);
	// bill meters the nominal management fee. BYO clusters are MERGED into the
	// managed clusters on /v1/visor/clusters — one fleet surface, two sources.
	fleet *fleet.Registry
	bill  *cloud.ResourceMeter
}

// ops carries the mounted Service into a TYPED op. zip fixes a typed handler's
// signature at (context.Context, *In) → (*Out, error), so the Service cannot
// arrive as a parameter the way cloud.Handle passes it to a raw handler — it
// arrives on the receiver. Embedding costs nothing and re-plumbs nothing: o.Log
// and o.State reach exactly what s.Log and s.State reach.
//
// A METHOD, not a wrapped free function, is also what makes the surface
// self-documenting: cmd/zipdoc lifts the doc comment off the function NAMED at
// the registration, and an adapter call in that argument position would leave it
// with nothing to read.
type ops struct{ *cloud.Service[state] }

// noArgs is the input of an op that takes nothing — a collection read scoped
// entirely by the validated principal. It is one type because "no input" is one
// thing, and it never reaches the spec: a bodyless method's In is projected only
// as its query parameters, and this has none.
type noArgs struct{}

// scope is the two facts every op here opens with: the REQUEST behind the typed
// context and the VALIDATED tenant org.
//
// The request is needed, not merely convenient — this subsystem is a tenant-scoped
// PROXY, and client.go forwards the caller's own identity headers (and, with no
// service credential configured, their bearer) upstream to Visor, so the op that
// drops the request drops the caller's identity on the far side of the hop.
//
// Off the HTTP path (an MCP or CLI invoke with no request) there is no principal
// to validate, so it refuses with the same 403 a raw handler answers. Fail-closed
// with one gate, not two.
func scope(ctx context.Context) (*zip.Ctx, string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", principal.RefusedFrom(ctx)
	}
	org, ok := tenant(c)
	if !ok {
		return nil, "", principal.Refused(c)
	}
	return c, org, nil
}

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Mount wires the compute surface onto app per HIP-0106. visor is a "complex" mount
// (it keeps a "compute"-provider meter and a fleet-scoped sub-logger that both need
// deps at construction), so it builds the Service value directly.
//
// Routes are TYPED ops (zip.Get/Post/Delete[In, Out]) wherever the route has a
// request/response shape: a typed op is the entry in the ONE registry that REST,
// OpenAPI, MCP and the CLI all project from, so a route registered any other way
// is invisible to three of the four. The handful that stay raw are the ones with
// no shape to state — see the note at each.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("visor.Mount: nil app")
	}
	s := &cloud.Service[state]{
		Base: cloud.NewBase(deps, "visor"),
		State: state{
			cl:    newClient(),
			fleet: fleet.New(deps.Brand, luxlog.Default().New("subsystem", "fleet")),
			bill:  cloud.NewResourceMeter(deps, "compute"),
		},
	}
	o := ops{s}
	// reg is the typed-op registry every projection reads. A Router that cannot
	// reach it fails the mount rather than serving routes no projection knows.
	reg := cloud.ZipApp(app)
	if reg == nil {
		return fmt.Errorf("visor.Mount: router carries no typed-op registry")
	}

	// Static routes register before their :param siblings so Fiber's
	// registration-order match never lets a machine/cluster id capture a literal.
	zip.Get(reg, "/v1/visor/machines", o.listMachines,
		zip.WithOperationID("listMachines"), zip.WithTags("compute"))
	// RAW: launch is polymorphic on the wire — a dryRun answers 200 with Visor's
	// price quote verbatim, a real launch answers 201 with a machineView. One typed
	// Out cannot state both, and re-shaping either is a wire break.
	app.Post("/v1/visor/machines", cloud.Handle(s, launchMachine))
	zip.Get(reg, "/v1/visor/machines/:id", o.getMachine,
		zip.WithOperationID("getMachine"), zip.WithTags("compute"))
	zip.Delete(reg, "/v1/visor/machines/:id", o.deleteMachine,
		zip.WithOperationID("deleteMachine"), zip.WithTags("compute"))

	zip.Get(reg, "/v1/visor/gpus/alerts", o.gpuAlerts,
		zip.WithOperationID("listGpuAlerts"), zip.WithTags("compute"))
	zip.Get(reg, "/v1/visor/gpus", o.listGPUs,
		zip.WithOperationID("listGpus"), zip.WithTags("compute"))

	// BYO fleet: the org's bring-your-own machines that dialed in via
	// `hanzo link`. Raw list here; the same workers are folded into
	// /v1/visor/machines and /v1/visor/gpus above (provider="byo") so the
	// console's existing pages show them alongside Visor-provisioned compute.
	zip.Get(reg, "/v1/visor/fleet/workers", o.listFleetWorkers,
		zip.WithOperationID("listFleetWorkers"), zip.WithTags("fleet"))
	// The org's gpu-jobs render queue: per-GPU depth + running workflow (GET), and a
	// manage verb — cancel a queued/running render (POST). Deeper literals register
	// before bare /v1/visor/fleet so neither shadows the other.
	zip.Get(reg, "/v1/visor/fleet/jobs", o.listFleetJobs,
		zip.WithOperationID("listFleetJobs"), zip.WithTags("fleet"))
	zip.Post(reg, "/v1/visor/fleet/jobs/:id/cancel", o.cancelFleetJob,
		zip.WithOperationID("cancelFleetJob"), zip.WithTags("fleet"))
	// BYO workers self-report GPU utilization here (POST); the GET on the same path
	// (listFleetSamples, below) reads the org's series back.
	zip.Post(reg, "/v1/visor/fleet/samples", o.ingestSample,
		zip.WithOperationID("recordFleetSample"), zip.WithTags("fleet"))
	// The unified board: every compute source the org has, each with its latest
	// utilization, plus the series behind it (board.go). The deeper literals
	// register before the bare /v1/visor/fleet so neither can shadow the other.
	zip.Get(reg, "/v1/visor/fleet/samples", o.listFleetSamples,
		zip.WithOperationID("listFleetSamples"), zip.WithTags("fleet"))
	zip.Get(reg, "/v1/visor/fleet", o.listFleet,
		zip.WithOperationID("listFleet"), zip.WithTags("fleet"))

	zip.Get(reg, "/v1/visor/clusters", o.listClusters,
		zip.WithOperationID("listClusters"), zip.WithTags("compute"))
	// BYO: attach an existing cluster (kubeconfig) or detach one. Managed
	// clusters (Visor-provisioned DOKS/AWS/…) + BYO ones surface together on
	// GET /v1/visor/clusters.
	zip.Post(reg, "/v1/visor/clusters", o.attachCluster,
		zip.WithOperationID("attachCluster"), zip.WithTags("compute"))
	zip.Delete(reg, "/v1/visor/clusters/:id", o.detachCluster,
		zip.WithOperationID("detachCluster"), zip.WithTags("compute"))
	zip.Post(reg, "/v1/visor/clusters/:clusterId/pools", o.createPool,
		zip.WithOperationID("createNodePool"), zip.WithTags("compute"))
	zip.Post(reg, "/v1/visor/clusters/:clusterId/pools/:poolId/scale", o.scalePool,
		zip.WithOperationID("scaleNodePool"), zip.WithTags("compute"))
	zip.Delete(reg, "/v1/visor/clusters/:clusterId/pools/:poolId", o.deletePool,
		zip.WithOperationID("deleteNodePool"), zip.WithTags("compute"))

	// Unified /v1/visor/k8s — the ONE Kubernetes noun (k8s.go): DOKS cluster lifecycle
	// (list / detail+nodes / create / delete) plus the fleet-wide worker NODES,
	// proxied to Visor. Reads are org-scoped; create/delete are admin-gated (real
	// house-account infra spend). Static /clusters registers before its :id sibling
	// so a cluster id never captures the literal.
	zip.Get(reg, "/v1/visor/k8s/clusters", o.listK8sClusters,
		zip.WithOperationID("listKubernetesClusters"), zip.WithTags("kubernetes"))
	zip.Post(reg, "/v1/visor/k8s/clusters", o.createK8sCluster,
		zip.WithOperationID("createKubernetesCluster"), zip.WithTags("kubernetes"))
	zip.Get(reg, "/v1/visor/k8s/clusters/:id", o.getK8sCluster,
		zip.WithOperationID("getKubernetesCluster"), zip.WithTags("kubernetes"))
	zip.Delete(reg, "/v1/visor/k8s/clusters/:id", o.deleteK8sCluster,
		zip.WithOperationID("deleteKubernetesCluster"), zip.WithTags("kubernetes"))
	zip.Get(reg, "/v1/visor/k8s/nodes", o.listK8sNodes,
		zip.WithOperationID("listKubernetesNodes"), zip.WithTags("kubernetes"))

	// Compute catalog: the global region + size lists that back the Machines/GPUs
	// launch drawer. Namespaced under /v1/visor/compute — "sizes"/"regions" are
	// catalog dimensions shared by machines AND gpus, not owned nouns, so they
	// nest under compute rather than sitting beside them. Org-gated but not
	// org-scoped — the catalog is identical for every tenant.
	//
	// RAW: both pass Visor's catalog payload through VERBATIM, so the shape is
	// Visor's and this package does not know it. A typed Out would have to invent
	// one — the opposite of what the passthrough is for.
	app.Get("/v1/visor/compute/regions", cloud.Handle(s, listRegions))
	app.Get("/v1/visor/compute/sizes", cloud.Handle(s, listSizes))

	// A machine's AGENT — thin proxy over vm's binding surface (mark a machine as
	// running the @hanzo/bot runtime for a cloud Agent).
	//
	// One noun, one address, and the METHOD carries the verb. This surface used to
	// spell one resource three ways — POST .../bind-agent to create, GET and DELETE
	// .../agent-binding to read and remove, GET /v1/agent-bindings to list — so a
	// caller had to learn that create lives at a different path from its own read,
	// and every projection published three names for one thing. A machine hosts at
	// most one agent, so it is a to-one sub-resource: singular at the member,
	// plural at the collection, which is the same rule everywhere else.
	//
	// PUT, not POST, because binding is idempotent: re-binding the same agent to
	// the same machine is the state the caller asked for, not a second binding.
	//
	// vm now answers on these SAME four addresses, so the translation bots.go used
	// to keep is gone rather than moved — one spelling end to end.
	zip.Get(reg, "/v1/visor/machines/agents", o.listAgents,
		zip.WithOperationID("listMachineAgents"), zip.WithTags("compute"))
	zip.Put(reg, "/v1/visor/machines/:id/agent", o.bindAgent,
		zip.WithOperationID("bindMachineAgent"), zip.WithTags("compute"))
	zip.Get(reg, "/v1/visor/machines/:id/agent", o.getAgent,
		zip.WithOperationID("getMachineAgent"), zip.WithTags("compute"))
	zip.Delete(reg, "/v1/visor/machines/:id/agent", o.unbindAgent,
		zip.WithOperationID("unbindMachineAgent"), zip.WithTags("compute"))

	// Bot machines — a kind=bot machine + an agent binding, composed from the vm
	// compute + binding surface (bots.go). The value is a MACHINE that hosts a bot
	// runtime, not the bot itself: /v1/bot is the bot RUN (clients/bots), a
	// different noun, and it belongs to bots. This one nests under
	// /v1/visor/compute, so the two never share a route namespace. launch is an
	// explicit literal, registered before :id so it never binds as an id.
	zip.Get(reg, "/v1/visor/compute/bots", o.listBots,
		zip.WithOperationID("listBots"), zip.WithTags("bots"))
	// RAW: launch is polymorphic exactly like the machine launch above — 200 + the
	// quote for a dryRun, 201 + the botView for a real launch.
	app.Post("/v1/visor/compute/bots/launch", cloud.Handle(s, launchBot))
	zip.Get(reg, "/v1/visor/compute/bots/:id", o.getBot,
		zip.WithOperationID("getBot"), zip.WithTags("bots"))
	zip.Delete(reg, "/v1/visor/compute/bots/:id", o.deleteBot,
		zip.WithOperationID("deleteBot"), zip.WithTags("bots"))
	// RAW: /:action is a verb dispatch, not a resource, and `message` streams the
	// bound agent's answer back VERBATIM — the upstream body, its Content-Type and
	// its status. There is no Out to state, and stating one would buffer a stream.
	app.Post("/v1/visor/compute/bots/:id/:action", cloud.Handle(s, botAction))

	s.Log.Info("visor compute surface mounted", "target", s.State.cl.target,
		"serviceAuth", serviceClientID() != "", "brand", deps.Brand)
	return nil
}

// tenant resolves the org — the tenant-isolation KEY, taken verbatim from the
// validated IAM owner claim (principal.Org). It is what this client sends to
// Visor as ?owner, so a caller can never read or mutate another tenant's compute.
func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// project resolves the org SUB-SCOPE (principal.Project) that shards the BYO fleet
// registry within an org. The default project keeps the legacy org-only shard, so
// single-project callers are unchanged. Managed clusters (Visor node pools) stay
// org-scoped; only the BYO fleet registry is project-sharded.
func project(c *zip.Ctx) string { return principal.Project(c) }

// ---- machines ----

// managedMachines is the org's Visor-provisioned machines: the deduped UNION of
// Visor's THREE machine sources, so every real machine appears exactly once.
//
//   - REGISTRY (GET /v1/machines, source=registry): Visor's DB-backed, masked
//     machine records — they carry whatever Visor has synced/enriched.
//   - LIVE provider list (GET /v1/machines, source=live →
//     service.ListOrgMachines): every droplet currently tagged to the org in
//     Hanzo's house DO account, straight from the live DO API.
//   - DOKS worker NODES (GET /v1/k8s/nodes → ListComputeKubernetesNodes):
//     each managed-Kubernetes node as a Machine (Id=droplet id), unioned across the
//     house account (hanzo-org cluster tag) and BYOC providers (Provider.ClusterID)
//     so the fleet shows cluster NODES, not just standalone droplets.
//
// A droplet that was provisioned but is NOT (yet) in the registry lives ONLY in
// the live list; sourcing the registry alone made it invisible on the world admin
// fleet (the bug this fixes). A DOKS node's droplet carries a k8s tag (not a
// hanzo-org droplet tag), so it is absent from BOTH registry and live and would be
// invisible without the third source. Dedup is by provider id OR name; the REGISTRY
// entry WINS a collision so any enrichment/masking it carries is preserved, and a
// DOKS node whose droplet also appears live is deduped by droplet id (never twice).
// Each source is independently resilient — an outage of any is logged and skipped,
// never hiding the others (or the caller's BYO fold). Nothing is fabricated: only
// machines Visor actually returns are surfaced.
func managedMachines(s *cloud.Service[state], c *zip.Ctx, org string) []visorMachine {
	// ONE call. Visor's /v1/machines answers from the organization's own provider
	// credentials AND from the house account, keyed (owner, name), with Source
	// saying which — so the join that used to live here lives where the data is.
	// Asking twice and merging was how a caller who asked once got a partial
	// answer without being told.
	var registry, live, nodes []visorMachine
	if err := s.State.cl.call(c, http.MethodGet, "/v1/machines", q("owner", org), nil, &registry); err != nil {
		// A Visor blip must not hide the org's OTHER machine source (its BYO fold).
		s.Log.Warn("visor machines failed; visor machines omitted", "org", org, "err", err)
		registry = nil
	}
	// THIRD source: DOKS worker NODES (GET /v1/k8s/nodes → visor unions the
	// house-account hanzo-org-tagged clusters + BYOC Provider.ClusterID clusters).
	// A cluster's node is a real droplet, so the world fleet should show the NODES,
	// not just standalone droplets. A DOKS node whose droplet is ALSO in the live
	// list dedupes by droplet id below (Machine.Id == DropletID), so it never lists
	// twice. Independently resilient like the other two.
	//
	// This one is a TYPED op upstream, so it answers its Out directly rather than
	// the envelope the two above still use — cl.op, not cl.call. A nil list is not
	// an empty answer here: the op always writes the key, so nil means the Visor on
	// the other end does not serve it, and this fold is degraded rather than
	// complete. It is LOGGED for the same reason the other two are — a silently
	// smaller fleet is the failure this whole source exists to prevent.
	var res visorNodes
	if err := s.State.cl.op(c, http.MethodGet, "/v1/k8s/nodes", q("owner", org), nil, &res); err != nil {
		s.Log.Warn("visor k8s nodes failed; DOKS nodes omitted", "org", org, "err", err)
	} else if res.Nodes == nil {
		s.Log.Warn("visor k8s nodes answered without a nodes list; DOKS nodes omitted", "org", org)
	} else {
		nodes = res.Nodes
	}

	out := make([]visorMachine, 0, len(registry)+len(live)+len(nodes))
	seen := make(map[string]struct{}, 2*(len(registry)+len(live)+len(nodes)))
	claim := func(m visorMachine) {
		if m.Id != "" {
			seen["id:"+m.Id] = struct{}{}
		}
		if m.Name != "" {
			seen["name:"+m.Name] = struct{}{}
		}
	}
	claimed := func(m visorMachine) bool {
		if m.Id != "" {
			if _, ok := seen["id:"+m.Id]; ok {
				return true
			}
		}
		if m.Name != "" {
			if _, ok := seen["name:"+m.Name]; ok {
				return true
			}
		}
		return false
	}
	// Registry first so it WINS every id/name collision; the live list then
	// contributes only the droplets the registry has not already surfaced (and is
	// deduped against itself).
	for _, m := range registry {
		out = append(out, m)
		claim(m)
	}
	for _, m := range live {
		if claimed(m) {
			continue
		}
		out = append(out, m)
		claim(m)
	}
	// DOKS nodes last: a node whose droplet already surfaced (registry or live)
	// dedupes by droplet id/name; only cluster nodes not otherwise listed are added.
	for _, m := range nodes {
		if claimed(m) {
			continue
		}
		out = append(out, m)
		claim(m)
	}
	return out
}

// machineRef addresses ONE machine. It is flat on purpose: zip binds a path
// segment onto the TOP-LEVEL field whose json name matches, so an id nested in
// an embedded struct would silently never bind.
type machineRef struct {
	// ID is the machine's org-scoped NAME — the stable key Visor addresses a
	// machine by (owner/name), not the ephemeral provider id.
	ID string `json:"id"`
}

// machineList is the org's compute inventory, one row per machine.
type machineList struct {
	// Machines is every machine the org has: Visor-provisioned and BYO together.
	Machines []machineView `json:"machines"`
}

// listMachines returns every machine the caller's org has — Visor's registry, the
// live DigitalOcean droplets and the DOKS worker nodes (deduped into one union),
// plus the BYO machines that dialed in via `hanzo link` (provider "byo").
//
// A source Visor cannot answer for is logged and skipped, never an error: one
// wedged upstream must not hide the machines the other sources can see.
//
// Response: {"machines":[{"id":"web-1","name":"Web 1","region":"sfo3","type":"s-2vcpu-4gb","status":"running","provider":"digitalocean","publicIp":"1.2.3.4","vcpu":2,"mem":"4 GB"}]}
func (o ops) listMachines(ctx context.Context, _ *noArgs) (*machineList, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	machines := managedMachines(o.Service, c, org)
	out := make([]machineView, 0, len(machines))
	for _, m := range machines {
		out = append(out, toMachineView(m))
	}
	// Fold in the org's BYO machines (provider="byo") so the console's Machines
	// page shows dialed-in GPUs next to Visor-provisioned ones.
	for _, w := range byoWorkers(ctx, org) {
		out = append(out, byoMachineView(w))
	}
	return &machineList{Machines: out}, nil
}

// getMachine returns one of the caller org's machines by its org-scoped name.
// Visor keys the lookup by owner/name, so an id belonging to another tenant
// resolves to not-found rather than another org's machine.
//
// Response: {"id":"web-1","name":"Web 1","region":"sfo3","type":"s-2vcpu-4gb","status":"running","publicIp":"1.2.3.4","vcpu":2}
func (o ops) getMachine(ctx context.Context, in *machineRef) (*machineView, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.ID)
	if name == "" {
		return nil, zip.ErrBadRequest("machine id required")
	}
	var m visorMachine
	// Visor keys a machine by owner/name; the REST :id is the org-scoped name.
	if err := o.State.cl.call(c, http.MethodGet, machine(org, name), "", nil, &m); err != nil {
		return nil, err
	}
	if m.Name == "" && m.Id == "" {
		return nil, zip.ErrNotFound("machine not found")
	}
	v := toMachineView(m)
	return &v, nil
}

// ---- compute catalog (regions / sizes) ----

// listRegions and listSizes expose the global compute catalog that backs the launch
// drawer. Both delegate to catalog: DRY, one org-gated passthrough of Visor's
// authoritative list. GET /v1/visor/compute/regions, GET /v1/visor/compute/sizes.
func listRegions(s *cloud.Service[state], c *zip.Ctx) error { return catalog(s, c, "/v1/regions") }
func listSizes(s *cloud.Service[state], c *zip.Ctx) error   { return catalog(s, c, "/v1/sizes") }

// catalog proxies a global (non-org-scoped) Visor catalog list. Org-gated so only a
// validated principal reaches it, but no ?owner is forwarded — the region/size catalog
// is identical for every tenant. The Visor payload is passed through verbatim so the
// wire shape stays the single source of truth.
func catalog(s *cloud.Service[state], c *zip.Ctx, upstream string) error {
	if _, ok := tenant(c); !ok {
		return principal.Refused(c)
	}
	var data json.RawMessage
	if err := s.State.cl.call(c, http.MethodGet, upstream, "", nil, &data); err != nil {
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

// The prose for the five operations here that cannot be typed ops. Every other route
// in visor is typed and zipdoc lifts its doc comment into zipdoc_gen.go; these five
// stay raw handlers for the reasons Mount states beside each — two launches whose
// response shape depends on `dryRun`, two catalog passthroughs whose shape is Visor's
// and not this package's, and one verb dispatch that streams an agent's answer back
// verbatim. So there is no comment for anything to lift and the published document
// would carry an operationId and nothing else. Two of the five SPEND REAL MONEY, so a
// caller reading only the document has to be told where the quote is. Declared
// through the same registry Register uses, so a description renders only while the
// router actually serves the route.
func init() {
	openapi.Describe("/v1/visor/machines", http.MethodPost,
		"Launch a metered machine for your org, or price one first with dryRun",
		"Provisions a machine owned by the caller's org and answers 201 with the machine. "+
			"Send `dryRun: true` to get a PRICE QUOTE instead: 200 with the upstream quote passed "+
			"through verbatim, nothing launched and nothing spent. Two response shapes on one "+
			"address is the rule to know, and it is why this is not a typed op.\n\n"+
			"Metering is not this plane's: the launch fronts the compute provider's resell "+
			"endpoint, which owns the balance gate and the per-hour meter, and cloud only forwards "+
			"the tenant. Ownership is the validated principal's org and is never read from the "+
			"body, so a launch always lands in the caller's OWN tenant and the machine it creates "+
			"is only ever visible to that tenant. Fails closed: a validated principal is required "+
			"(403 without one) and `size` (or its `instanceType` alias) is required (400).")
	openapi.Describe("/v1/visor/compute/regions", http.MethodGet,
		"The regions a machine or GPU can be launched into",
		"Lists the launch regions the compute catalog offers, passed through verbatim from the "+
			"provider so the shape stays the provider's single source of truth. The catalog is "+
			"GLOBAL, not per-tenant: no owner is forwarded and every org sees the same list. It "+
			"is still gated — a validated principal is required, 403 without one — because the "+
			"catalog is what backs the launch drawer, not public marketing copy.")
	openapi.Describe("/v1/visor/compute/sizes", http.MethodGet,
		"The machine and GPU sizes that can be launched",
		"Lists the instance sizes the compute catalog offers, passed through verbatim from the "+
			"provider so the shape stays the provider's single source of truth. These are the "+
			"values `size` accepts on a launch. The catalog is GLOBAL, not per-tenant: no owner "+
			"is forwarded and every org sees the same list. It is still gated — a validated "+
			"principal is required, 403 without one.")
	openapi.Describe("/v1/visor/compute/bots/launch", http.MethodPost,
		"Launch a bot machine — an agent plus the machine that runs it — or price one",
		"Creates BOTH halves of a bot in one call and answers 201 with the bot: the cloud agent "+
			"it runs, then a bot-kind machine bootstrapped with the bot runtime, then the binding "+
			"between them, so a launched bot is immediately messageable. Send `dryRun: true` for a "+
			"price quote instead — 200 with the upstream quote verbatim, no agent created, no "+
			"machine launched, nothing spent.\n\n"+
			"The agent is created FIRST and on purpose: it is create-if-absent (an agent that "+
			"already exists is reused, so a relaunch is fine and several bots may share one "+
			"explicit `agent`), and doing it before the machine means a bad request — a model that "+
			"is not in the catalog, say — fails with the real reason BEFORE any metered machine is "+
			"provisioned. `agent` defaults to the bot's name and an empty `model` takes the "+
			"deployment default.\n\n"+
			"Org-scoped and fails closed: a validated principal is required (403 without one), the "+
			"owning org is that principal's and never a body field, `size` is required (400), and "+
			"`name` is required for a real launch though not for a quote.")
	openapi.Describe("/v1/visor/compute/bots/:id/:action", http.MethodPost,
		"Message a bot, or stop it, by naming the action in the path",
		"Dispatches one verb against a bot the caller's org owns. `message` runs the bot's bound "+
			"agent with the request body as the message and streams the agent's answer back "+
			"VERBATIM — the upstream body, its content type and its status — so a message is a "+
			"real agent run, recorded, billed and traced exactly like any other, under the "+
			"caller's own identity rather than a fabricated one. `stop` and `pause` are the same "+
			"single honest capability: they halt the runtime by unbinding the agent while LEAVING "+
			"THE MACHINE UP, so the bot stops answering but keeps costing — rebind to resume, or "+
			"delete the bot to tear it down. Stopping is idempotent; a bot with no binding still "+
			"reports stopped.\n\n"+
			"Org-scoped and fails closed: a validated principal is required (403 without one) and "+
			"the bot is addressed under the caller's OWN org, so another tenant's id is not "+
			"reachable. An unknown action is a clean 400 naming the three it accepts, never a "+
			"silent no-op, and messaging a bot with no bound agent is a 400.")
}

// launchMachine quotes (dryRun) or launches a metered, per-org machine. It fronts
// Visor's resell launch (POST /v1/machines), which owns the balance gate and
// per-hour metering — cloud never bills compute itself; it forwards the tenant.
// A dryRun returns Visor's price quote verbatim (spends nothing); a real launch
// returns the launched machine as a clean machineView.
func launchMachine(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return principal.Refused(c)
	}
	var body launchReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	// Blank is blank. zip's Bind does not trim, and these four are free text that
	// Visor echoes back into the machine we render — a name of "   " came back and
	// won the choice against the real id.
	body.Name = strings.TrimSpace(body.Name)
	body.Size = strings.TrimSpace(body.Size)
	body.InstanceType = strings.TrimSpace(body.InstanceType)
	body.Region = strings.TrimSpace(body.Region)
	if cmp.Or(body.Size, body.InstanceType) == "" {
		return zip.ErrBadRequest("size is required")
	}
	var data json.RawMessage
	if err := s.State.cl.call(c, http.MethodPost, "/v1/machines", q("owner", org), body, &data); err != nil {
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

// Every delete here answers 204 and returns *struct{} to say so: zip writes 204
// for a nil Out, and an ANONYMOUS struct has no name for the spec to $ref — which
// is what makes the document state "204 no content" instead of promising a body.
// (This paragraph is deliberately NOT part of the doc comment below: zipdoc lifts
// that one into the published spec, and how the Go type spells "no content" is
// not something an API reader needs to know.)

// machine addresses ONE machine on Visor. The tenant is in the ADDRESS, so a
// call cannot be scoped to one org and aimed at another — which is what an
// ?owner= query beside a bare id allowed. Collections still take ?owner=,
// because a collection is scoped and an item is named.
func machine(org, name string) string {
	return "/v1/machines/" + url.PathEscape(org) + "/" + url.PathEscape(name)
}

// pool addresses ONE node pool on Visor, by the (owner, name) its row is keyed
// by. A cluster read reports the provider's id for a pool alongside its name;
// the delete below has always sent that string as the name, so it is the name
// this addresses with.
func pool(org, name string) string {
	return "/v1/pools/" + url.PathEscape(org) + "/" + url.PathEscape(name)
}

// deleteMachine terminates one of the caller org's machines. Visor takes the
// machine identity as owner+name, and the owner is the validated principal, so a
// caller can only ever terminate its own tenant's machine. Answers 204.
func (o ops) deleteMachine(ctx context.Context, in *machineRef) (*struct{}, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.ID)
	if name == "" {
		return nil, zip.ErrBadRequest("machine id required")
	}
	if err := o.State.cl.call(c, http.MethodDelete, machine(org, name), "", nil, nil); err != nil {
		return nil, err
	}
	return nil, nil
}

// ---- GPUs (derived from the org's real GPU machines) ----

// gpuList is the per-accelerator inventory: one row per physical card, not per
// machine — a gpu-h100x8 node contributes eight rows.
type gpuList struct {
	// GPUs is every accelerator the org has, from Visor GPU droplets and from BYO
	// workers alike.
	GPUs []gpuView `json:"gpus"`
}

// gpuAlertList is the GPU alert inventory.
type gpuAlertList struct {
	// Alerts is always empty, and typed as a raw list because Visor exposes no
	// alert inventory for this surface to shape: there is nothing to describe
	// until there is something to return.
	Alerts []any `json:"alerts"`
}

// listGPUs returns one row per physical accelerator the caller's org has, derived
// from its real GPU machines (the size slug says how many cards a node holds) and
// from the accelerators BYO workers report through nvidia-smi.
//
// Live telemetry is absent on Visor rows because Visor's machine object carries
// none — an honest omission the console renders as "—", never a fabricated 0.
//
// Response: {"gpus":[{"id":"gpu-1#0","name":"gpu-1","model":"H100","region":"nyc2","status":"running","machine":"gpu-1","provider":"digitalocean"}]}
func (o ops) listGPUs(ctx context.Context, _ *noArgs) (*gpuList, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]gpuView, 0)
	// Same managed-machine union as listMachines (registry + live DO, deduped), so
	// a GPU droplet present only in the live DO list is not hidden from the GPUs page.
	for _, m := range managedMachines(o.Service, c, org) {
		out = append(out, gpusFromMachine(m)...)
	}
	// Fold in the org's BYO accelerators (provider="byo") so the console's GPUs
	// page lists dialed-in cards with their real model + VRAM.
	for _, w := range byoWorkers(ctx, org) {
		out = append(out, byoGPUViews(w)...)
	}
	return &gpuList{GPUs: out}, nil
}

// gpuAlerts is an HONEST empty surface: Visor exposes no GPU alert inventory, so
// this returns [] rather than fabricating alerts. It stays a real, tenant-gated
// route so the console's alerts fetch resolves (200 [], not a 404) — an honest
// "no alerts", the same discipline the rest of the surface follows.
//
// Response: {"alerts":[]}
func (o ops) gpuAlerts(ctx context.Context, _ *noArgs) (*gpuAlertList, error) {
	if _, _, err := scope(ctx); err != nil {
		return nil, err
	}
	return &gpuAlertList{Alerts: []any{}}, nil
}

// ---- clusters (DOKS clusters, projected from Visor node pools) ----

// clusterList is the org's ONE fleet of clusters: Visor-managed node pools and
// attached BYO clusters in a single list, each row saying which it is (`kind`).
type clusterList struct {
	// Clusters is the merged fleet — kind "managed" for Visor-provisioned, "byo"
	// for an attached kubeconfig.
	Clusters []clusterView `json:"clusters"`
	// Degraded names any source that did not answer, so an empty Clusters means
	// "you have none" only when this is absent. Omitted when everything answered,
	// so a healthy response is unchanged. See degraded.go.
	Degraded []sourceFailure `json:"degraded,omitempty"`
}

// listClusters returns the caller org's clusters from both sources: the managed
// clusters projected from Visor's node pools, and the BYO clusters attached to the
// caller's project. A Visor outage costs the managed half only — the BYO half
// still lists, because a page that 502s on an optional provider is worse than a
// page that shows what it can.
//
// Response: {"clusters":[{"doksClusterId":"cl-1","name":"prod","status":"running","nodePools":[{"poolId":"p-1","name":"gpu","size":"gpu-h100x8-640gb","count":2}],"nodeSize":"gpu-h100x8-640gb","nodeCount":2,"kind":"managed"}]}
func (o ops) listClusters(ctx context.Context, _ *noArgs) (*clusterList, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	var pools []visorNodePool
	var down []sourceFailure
	if err := o.State.cl.call(c, http.MethodGet, "/v1/pools", q("owner", org), nil, &pools); err != nil {
		down = visorDown(err)
		// Visor unreachable — same graceful fold as listMachines/listGpus: a down
		// optional provider must NOT 502 the Clusters/GPUs page (it surfaced as a
		// console error on every load where Visor isn't deployed). Log and fall
		// through with no managed pools; the org's BYO clusters below still list.
		o.Log.Warn("visor pools failed; returning BYO-only cluster list", "org", org, "err", err)
		pools = nil
	}
	// ONE fleet surface: managed clusters (Visor node pools) + the org's BYO ones,
	// the latter sharded by the caller's project sub-scope.
	clusters := clustersFromPools(pools)
	clusters = append(clusters, byoClusters(o.Service, org, project(c))...)
	return &clusterList{Clusters: clusters, Degraded: down}, nil
}

// poolCreate is the add-a-node-pool request. clusterId comes from the URL and
// provider may come from either the body or ?provider=, which is why both are
// ordinary fields: the URL simply binds over the body when it carries them.
type poolCreate struct {
	// ClusterID is the cluster to add the pool to, from the URL path.
	ClusterID string `json:"clusterId"`
	// Provider is the cloud the cluster lives on (e.g. "digitalocean"). Required —
	// Visor routes the create by it. Accepted from the body or ?provider=.
	Provider string `json:"provider"`
	// Name is the pool's name.
	Name string `json:"name"`
	// Size is the provider size slug for each node (e.g. "s-4vcpu-8gb").
	Size string `json:"size"`
	// Count is how many nodes the pool starts with.
	Count int `json:"count"`
	// MinNodes is the floor the autoscaler may not shrink the pool below. Ignored
	// unless AutoScale is set.
	MinNodes int `json:"minNodes"`
	// MaxNodes is the ceiling the autoscaler may not grow the pool past, and so the
	// bound on what this pool can spend. Ignored unless AutoScale is set.
	MaxNodes int `json:"maxNodes"`
	// AutoScale turns the provider's cluster autoscaler on for this pool.
	AutoScale bool `json:"autoScale"`
}

// createPool adds a node pool to one of the caller org's clusters and answers 201
// with the created pool. Only the CreateNodePoolSpec fields are forwarded;
// owner/provider/clusterId ride in the query exactly as Visor expects them.
//
// Example: {"provider":"digitalocean","name":"gpu","size":"gpu-h100x8-640gb","count":2,"autoScale":false}
// Response: {"poolId":"p-1","name":"gpu","size":"gpu-h100x8-640gb","count":2}
func (o ops) createPool(ctx context.Context, in *poolCreate) (*nodePoolView, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	clusterID := strings.TrimSpace(in.ClusterID)
	provider := strings.TrimSpace(in.Provider)
	if provider == "" {
		return nil, zip.ErrBadRequest("provider is required")
	}
	// Forward only the CreateNodePoolSpec fields; provider/clusterId/owner ride in
	// the query exactly as Visor's pool collection expects.
	spec := map[string]any{
		"name": strings.TrimSpace(in.Name), "size": strings.TrimSpace(in.Size), "count": in.Count,
		"minNodes": in.MinNodes, "maxNodes": in.MaxNodes, "autoScale": in.AutoScale,
	}
	var created visorNodePool
	if err := o.State.cl.call(c, http.MethodPost, "/v1/pools",
		q("owner", org, "provider", provider, "clusterId", clusterID), spec, &created); err != nil {
		return nil, err
	}
	cloud.Created(ctx)
	v := toNodePoolView(created)
	return &v, nil
}

// poolScale is the resize request: which pool, and how many nodes it should have.
type poolScale struct {
	// ClusterID is the cluster holding the pool, from the URL path.
	ClusterID string `json:"clusterId"`
	// PoolID is the pool to resize, from the URL path — the `poolId` a cluster read
	// reports for it. Required.
	PoolID string `json:"poolId"`
	// Provider is the cloud the cluster lives on. Required; body or ?provider=.
	Provider string `json:"provider"`
	// Count is the node count to scale TO — an absolute target, not a delta, and
	// never negative.
	Count int `json:"count"`
}

// scalePool resizes a node pool to an absolute node count and returns the pool as
// Visor reports it after the change.
//
// Example: {"provider":"digitalocean","count":4}
// Response: {"poolId":"p-1","name":"gpu","size":"gpu-h100x8-640gb","count":4}
func (o ops) scalePool(ctx context.Context, in *poolScale) (*nodePoolView, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	poolID := strings.TrimSpace(in.PoolID)
	if poolID == "" {
		return nil, zip.ErrBadRequest("poolId required")
	}
	if in.Count < 0 {
		return nil, zip.ErrBadRequest("count must be non-negative")
	}
	var scaled visorNodePool
	// The pool is in the address; Visor reads the provider, the cluster and the
	// upstream id from its own row, so there is nothing left to repeat.
	if err := o.State.cl.call(c, http.MethodPut, pool(org, poolID)+"/size",
		q("count", strconv.Itoa(in.Count)), nil, &scaled); err != nil {
		return nil, err
	}
	v := toNodePoolView(scaled)
	return &v, nil
}

// poolRef addresses ONE node pool for deletion.
type poolRef struct {
	// ClusterID and PoolID address the pool, from the URL path.
	ClusterID string `json:"clusterId"`
	PoolID    string `json:"poolId"`
	// Provider is the cloud the cluster lives on, from ?provider=. Required.
	Provider string `json:"provider"`
}

// deletePool removes a node pool from one of the caller org's clusters. The owner
// scopes the delete to the caller's tenant; provider+clusterId drive the
// provider-side removal. Answers 204.
func (o ops) deletePool(ctx context.Context, in *poolRef) (*struct{}, error) {
	c, org, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	poolID := strings.TrimSpace(in.PoolID)
	if poolID == "" {
		return nil, zip.ErrBadRequest("poolId required")
	}
	if err := o.State.cl.call(c, http.MethodDelete, pool(org, poolID), "", nil, nil); err != nil {
		return nil, err
	}
	return nil, nil
}
