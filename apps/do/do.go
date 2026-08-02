// Package do is the org-scoped private-network surface — /v1/vpcs and
// /v1/balancers — carved out of Hanzo's OWN house DigitalOcean account.
//
// It is the house-account facade over digitalocean/godo's VPCs + LoadBalancers.
// An org's OWN cloud accounts (DigitalOcean, AWS, GCP, Azure) are a different
// plane: apps/venue links those and folds their clusters into the fleet.
//
//	GET    /v1/vpcs                 list the caller's VPCs            -> {vpcs:[...]}
//	POST   /v1/vpcs                 create {name,region,ip_range}     -> Vpc
//	GET    /v1/vpcs/:id             one VPC (owned)                   -> Vpc
//	DELETE /v1/vpcs/:id             delete one VPC (owned)
//	GET    /v1/balancers       list the caller's LBs             -> {loadBalancers:[...]}
//	POST   /v1/balancers       create {name,region,...}          -> LoadBalancer
//	GET    /v1/balancers/:id   one LB (owned)                    -> LoadBalancer
//	DELETE /v1/balancers/:id   delete one LB (owned)
//
// TENANT ISOLATION — DigitalOcean is a SINGLE account, so the org boundary is
// enforced by this subsystem, not by DO. A resource's PHYSICAL DO name is derived
// from the caller's validated org as "o"<orgHash>-<friendly> — the SAME org-hash,
// DNS-safe convention apps/storage + apps/provisioning use for shared backends
// (provisioning.BucketName). The client speaks FRIENDLY names ("web"); the server
// maps friendly↔physical and never trusts a client-supplied physical name. LIST
// filters DO's account-wide inventory to the caller's "o"<orgHash>- prefix and
// strips it; GET/DELETE re-derive nothing from the request beyond the resource id,
// fetch the resource, and confirm its physical name carries the CALLER's prefix
// before returning or deleting it — a resource in another org's namespace is
// reported 404 (an existence-oracle guard, never 403), so one tenant can neither
// see, read, nor delete another's. The boundary is by construction. VPCs carry no
// DO tags, so the name prefix (not a tag) is the ONE convention that isolates both
// resource kinds uniformly.
//
// FAIL-CLOSED — absent DO_API_TOKEN the subsystem mounts its full route space but
// every op returns an honest 503; it NEVER fabricates a VPC or load balancer. The
// token is the SAME single personal-access token the finance client reads
// (DO_API_TOKEN, sourced from a KMSSecret on the cloud env) — never hard-coded.
package do

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/digitalocean/godo"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/provisioning"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// tokenEnv is the single DO personal-access token, shared with clients/admin's
// finance reader. Sourced from a KMSSecret on the cloud env; never hard-coded.
const tokenEnv = "DO_API_TOKEN"

// perPage/maxPages bound DO list pagination: page through the account inventory
// 200 at a time, capped so a pathological account can neither exhaust memory nor
// spin forever. 200*50 = 10k resources of one kind — far above any real org.
const (
	perPage  = 200
	maxPages = 50
)

// nameRE is the FRIENDLY resource name a tenant supplies. Identical to the shape
// clients/s3 accepts (DNS/identifier-safe slug, ≤40 chars) so the friendly↔
// physical map round-trips: "o"<orgHash>-<name> stays inside DO's name limits and
// the prefix strip recovers exactly the friendly name.
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// idRE bounds a DO resource id path param (a UUID for both VPCs and LBs). A
// malformed id is a clean 400 before any DO call.
var idRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// vpcAPI / lbAPI are the NARROW godo seams this subsystem uses — a subset of
// godo.VPCsService / godo.LoadBalancersService. The real client's *.VPCs and
// *.LoadBalancers satisfy them; tests inject fakes so no test ever reaches DO.
type vpcAPI interface {
	List(context.Context, *godo.ListOptions) ([]*godo.VPC, *godo.Response, error)
	Get(context.Context, string) (*godo.VPC, *godo.Response, error)
	Create(context.Context, *godo.VPCCreateRequest) (*godo.VPC, *godo.Response, error)
	Delete(context.Context, string) (*godo.Response, error)
}

type lbAPI interface {
	List(context.Context, *godo.ListOptions) ([]godo.LoadBalancer, *godo.Response, error)
	Get(context.Context, string) (*godo.LoadBalancer, *godo.Response, error)
	Create(context.Context, *godo.LoadBalancerRequest) (*godo.LoadBalancer, *godo.Response, error)
	Delete(context.Context, string) (*godo.Response, error)
}

// state is do's own data; shared deps live in the embedded cloud.Base.
type state struct {
	vpcs vpcAPI
	lbs  lbAPI
}

// configured reports whether a DO token was present at Mount. Unconfigured → the
// godo seams are nil and every op fails closed 503.
func configured(s *cloud.Service[state]) bool { return s.State.vpcs != nil && s.State.lbs != nil }

// Mount wires /v1/vpcs/* and /v1/balancers/* onto app — one line over the
// generic subsystem entrypoint. Routes register unconditionally (even when
// unconfigured) so the surface owns its space and fails closed under its own name
// rather than 404-ing to a fallthrough.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "do", build, routes)
}

// build constructs the do state from DO_API_TOKEN (env; sourced from a KMSSecret,
// never hard-coded). Absent the token the godo seams stay nil → every op fails
// closed 503; it records that posture in the mount log.
func build(b cloud.Base) (state, error) {
	var st state
	if token := strings.TrimSpace(os.Getenv(tokenEnv)); token != "" {
		client := godo.NewFromToken(token)
		st.vpcs = client.VPCs
		st.lbs = client.LoadBalancers
	}
	if st.vpcs == nil || st.lbs == nil {
		b.Log.Warn("digitalocean subsystem mounted fail-closed: DO_API_TOKEN not set (all ops 503 until configured)")
	} else {
		b.Log.Info("digitalocean subsystem mounted", "prefix", "/v1/vpcs,/v1/balancers", "brand", b.Brand, "env", b.Env)
	}
	return st, nil
}

// zipdoc lifts the doc comment off each typed op — and off each field of its In
// and Out — into zipdoc_gen.go, which hands them to zip.Describe at init. Go
// drops comments at compile time, so this build-time pass is the ONLY way that
// prose reaches the published document, the MCP tool list and the generated
// SDKs. Run by `make -C apps/do generate` (a prerequisite of build).
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops carries the subsystem's state onto every typed op. A typed handler takes a
// context and its decoded In and nothing else, so the state rides on the
// receiver rather than through a closure per route.
type ops struct{ s *cloud.Service[state] }

// routes is the ONE place the surface is wired — shared by Mount (real godo) and
// the test (injected fakes). Static list/create register before the :id param
// route so an id can never shadow the collection handler.
//
// Every route is a TYPED op: ONE registry entry that is at once the REST route,
// the OpenAPI operation with its schemas, the MCP tool, the CLI command and the
// generated SDK method. Declared on the App with WHOLE paths rather than on two
// groups, because joining a "/v1/vpcs" prefix with an empty leaf yields
// "/v1/vpcs/" — a different path from the one this surface has always served.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// cloud.Bridge carries into a typed op the request its signature drops — this
	// subsystem resolves its tenant through tenant(), which reads the validated
	// principal AND the SuperAdmin bit, so it needs the request itself and not
	// only the org. On the scoped Router this installs once per DECLARED prefix
	// (/v1/vpcs, /v1/balancers) and nowhere else. It must precede the leaves
	// below: fiber runs middleware in registration order, so one installed after
	// them never runs for them. Serve installs one app-wide too — nesting is
	// harmless (the inner one is what the handler sees) and the tests mount this
	// subsystem on a bare app with no Serve, so this install is what makes them pass.
	app.Use(cloud.Bridge())
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		s.Log.Error("do: router exposes no op registry; the DigitalOcean surface would serve routes no projection knows")
		return
	}
	o := ops{s: s}
	zip.Get(zapp, "/v1/vpcs", o.listVPCs)
	zip.Post(zapp, "/v1/vpcs", o.createVPC, zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/vpcs/:id", o.getVPC)
	zip.Delete(zapp, "/v1/vpcs/:id", o.deleteVPC)

	zip.Get(zapp, "/v1/balancers", o.listLBs)
	zip.Post(zapp, "/v1/balancers", o.createLB, zip.WithStatus(http.StatusCreated))
	zip.Get(zapp, "/v1/balancers/:id", o.getLB)
	zip.Delete(zapp, "/v1/balancers/:id", o.deleteLB)
}

// ── request/response shapes (console VpcModule / LoadBalancerModule contract) ──

type vpcView struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	CIDR    string   `json:"cidr"`
	Region  string   `json:"region"`
	Subnets []string `json:"subnets"`
	Status  string   `json:"status"`
}

type lbView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Targets int    `json:"targets"`
	IP      string `json:"ip"`
	Status  string `json:"status"`
}

// vpcStatusActive — DO VPCs have NO lifecycle status field (they are created
// synchronously and have no pending/errored state). A VPC the API returns exists
// and is usable, so "active" is the accurate model, not a fabricated value.
const vpcStatusActive = "active"

// toVPCView maps a godo.VPC to the FE shape under its recovered friendly name.
// Subnets is honestly empty: a DO VPC is a single IP range with no sub-network
// objects in the API, so the FE renders a subnet count of 0 rather than an
// invented list.
func toVPCView(friendly string, v *godo.VPC) vpcView {
	return vpcView{
		ID:      v.ID,
		Name:    friendly,
		CIDR:    v.IPRange,
		Region:  v.RegionSlug,
		Subnets: []string{},
		Status:  vpcStatusActive,
	}
}

// toLBView maps a godo.LoadBalancer to the FE shape. targets is the real count of
// backend droplets attached to the LB; status/type/ip are DO's own live values.
func toLBView(friendly string, lb *godo.LoadBalancer) lbView {
	return lbView{
		ID:      lb.ID,
		Name:    friendly,
		Type:    lb.Type,
		Targets: len(lb.DropletIDs),
		IP:      lb.IP,
		Status:  lb.Status,
	}
}

// ── typed op inputs and outputs ─────────────────────────────────────────────

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire. ONE of these for the whole package.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an
// ALIAS for the unnamed empty struct, not a definition: zip keys the response on
// 204 only when the Out type has NO NAME, so a defined type here would publish
// "200 with a body" about a route that has always answered 204 with none — and
// every SDK generated from that document would expect a status the service never
// sends.
type noContent = struct{}

// idIn addresses ONE DigitalOcean resource by its id. A DELETE and a GET take
// their input from the URL and carry no request body, so this is the whole input.
type idIn struct {
	// ID is the DigitalOcean resource id (a UUID), from the path.
	ID string `json:"id"`
}

// vpcList is the VPC collection as the console's VpcModule reads it.
type vpcList struct {
	// VPCs are the caller org's VPCs under their friendly names.
	VPCs []vpcView `json:"vpcs"`
}

// lbList is the load-balancer collection as the console's LoadBalancerModule
// reads it.
type lbList struct {
	// LoadBalancers are the caller org's load balancers under their friendly names.
	LoadBalancers []lbView `json:"loadBalancers"`
}

// ── VPC handlers ────────────────────────────────────────────────────────────

// ListVpcs returns every VPC the caller's org owns, under the friendly names the
// org created them with. DigitalOcean is one account for the whole deployment, so
// the account-wide inventory is filtered to the caller's own "o"<orgHash>- name
// prefix and the prefix is stripped — another org's VPC is not merely hidden, it
// is never in the answer.
//
// Response: {"vpcs": [{"id": "vpc-1", "name": "web", "cidr": "10.10.0.0/16", "region": "nyc3", "subnets": [], "status": "active"}]}
func (o ops) listVPCs(ctx context.Context, _ *noInput) (*vpcList, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	all, err := allVPCs(o.s, ctx)
	if err != nil {
		return nil, gatewayErr(err)
	}
	pfx := orgPrefix(org)
	out := make([]vpcView, 0, len(all))
	for _, v := range all {
		name, ok := friendlyName(pfx, v.Name)
		if !ok {
			continue // another org's VPC — invisible
		}
		out = append(out, toVPCView(name, v))
	}
	return &vpcList{VPCs: out}, nil
}

type createVPCReq struct {
	// Name is the FRIENDLY name, a DNS-safe slug of at most 40 characters. The
	// physical DigitalOcean name is derived from it and the caller's org.
	Name string `json:"name"`
	// Region is the DigitalOcean region slug (nyc3, sfo3, …). Required.
	Region string `json:"region"`
	// IPRange is the VPC's private CIDR. Empty lets DigitalOcean assign one.
	IPRange string `json:"ip_range"`
}

// CreateVpc creates a VPC in the caller's org namespace and answers 201 with it.
// The physical DigitalOcean name is derived server-side from the validated org,
// so a tenant can only ever create inside its own namespace; a name that already
// exists there is a 409.
//
// Example: {"name": "web", "region": "nyc3", "ip_range": "10.10.0.0/16"}
func (o ops) createVPC(ctx context.Context, in *createVPCReq) (*vpcView, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	region := strings.TrimSpace(in.Region)
	if region == "" {
		return nil, zip.ErrBadRequest("region is required")
	}
	v, _, err := o.s.State.vpcs.Create(ctx, &godo.VPCCreateRequest{
		Name:        physicalName(org, name),
		RegionSlug:  region,
		IPRange:     strings.TrimSpace(in.IPRange), // empty → DO auto-assigns
		Description: "managed by Hanzo Cloud",
	})
	if err != nil {
		if s := doStatus(err); s == http.StatusConflict || s == http.StatusUnprocessableEntity {
			return nil, zip.ErrConflict("a vpc with that name already exists")
		}
		return nil, gatewayErr(err)
	}
	view := toVPCView(name, v)
	return &view, nil
}

// GetVpc returns one of the caller org's VPCs by id. A VPC that exists but sits
// in another org's namespace is reported 404, never 403 — the answer must not
// tell one tenant that another tenant's resource exists.
func (o ops) getVPC(ctx context.Context, in *idIn) (*vpcView, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := validID(in.ID)
	if !ok {
		return nil, zip.ErrBadRequest("invalid id")
	}
	v, _, err := o.s.State.vpcs.Get(ctx, id)
	if err != nil {
		return nil, notFoundOr(err, "vpc not found")
	}
	name, ok := friendlyName(orgPrefix(org), v.Name)
	if !ok {
		return nil, zip.ErrNotFound("vpc not found") // not the caller's — existence-oracle guard
	}
	view := toVPCView(name, v)
	return &view, nil
}

// DeleteVpc removes one of the caller org's VPCs and answers 204. Ownership is
// confirmed by re-fetching the resource and checking its physical name carries
// the caller's org prefix BEFORE anything is deleted, so a cross-tenant id is a
// 404 rather than a delete of another org's VPC.
func (o ops) deleteVPC(ctx context.Context, in *idIn) (*noContent, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := validID(in.ID)
	if !ok {
		return nil, zip.ErrBadRequest("invalid id")
	}
	// Confirm ownership by name prefix BEFORE deleting — a cross-tenant id is 404,
	// never a delete of another org's VPC.
	v, _, err := o.s.State.vpcs.Get(ctx, id)
	if err != nil {
		return nil, notFoundOr(err, "vpc not found")
	}
	if _, ok := friendlyName(orgPrefix(org), v.Name); !ok {
		return nil, zip.ErrNotFound("vpc not found")
	}
	if _, err := o.s.State.vpcs.Delete(ctx, id); err != nil {
		return nil, notFoundOr(err, "vpc not found")
	}
	return nil, nil
}

// ── Load Balancer handlers ──────────────────────────────────────────────────

// ListLoadBalancers returns every load balancer the caller's org owns, under the
// friendly names the org created them with. Same account-wide filter as the VPC
// listing: a load balancer outside the caller's "o"<orgHash>- namespace is never
// in the answer.
//
// Response: {"loadBalancers": [{"id": "lb-1", "name": "edge", "type": "REGIONAL", "targets": 3, "ip": "10.0.0.1", "status": "active"}]}
func (o ops) listLBs(ctx context.Context, _ *noInput) (*lbList, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	all, err := allLBs(o.s, ctx)
	if err != nil {
		return nil, gatewayErr(err)
	}
	pfx := orgPrefix(org)
	out := make([]lbView, 0, len(all))
	for i := range all {
		lb := all[i]
		name, ok := friendlyName(pfx, lb.Name)
		if !ok {
			continue // another org's LB — invisible
		}
		out = append(out, toLBView(name, &lb))
	}
	return &lbList{LoadBalancers: out}, nil
}

type fwdRule struct {
	// EntryProtocol is the protocol the load balancer listens with (http, https, tcp).
	EntryProtocol string `json:"entry_protocol"`
	// EntryPort is the port the load balancer listens on.
	EntryPort int `json:"entry_port"`
	// TargetProtocol is the protocol used to reach the backend droplets.
	TargetProtocol string `json:"target_protocol"`
	// TargetPort is the backend port traffic is forwarded to.
	TargetPort int `json:"target_port"`
}

type createLBReq struct {
	// Name is the FRIENDLY name, a DNS-safe slug of at most 40 characters. The
	// physical DigitalOcean name is derived from it and the caller's org.
	Name string `json:"name"`
	// Region is the DigitalOcean region slug (nyc3, sfo3, …). Required.
	Region string `json:"region"`
	// Type is the DigitalOcean load-balancer type. Empty takes DO's default (REGIONAL).
	Type string `json:"type"`
	// Size is the DigitalOcean size slug. Empty takes DO's default.
	Size string `json:"size"`
	// ForwardingRules are the listen→backend port mappings. Empty defaults to
	// plain HTTP 80→80, the same default DigitalOcean's own console applies.
	ForwardingRules []fwdRule `json:"forwarding_rules"`
}

// CreateLoadBalancer creates a load balancer in the caller's org namespace and
// answers 201 with it. The physical DigitalOcean name is derived server-side from
// the validated org; a name that already exists there is a 409. Omitting
// forwarding rules yields a usable HTTP 80→80 load balancer rather than a 422.
//
// Example: {"name": "edge", "region": "nyc3", "forwarding_rules": [{"entry_protocol": "https", "entry_port": 443, "target_protocol": "http", "target_port": 8080}]}
func (o ops) createLB(ctx context.Context, in *createLBReq) (*lbView, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	region := strings.TrimSpace(in.Region)
	if region == "" {
		return nil, zip.ErrBadRequest("region is required")
	}
	// DO requires at least one forwarding rule. When the caller omits them, default
	// to plain HTTP 80→80 — the same default DO's own console applies — so a
	// minimal create yields a REAL, usable LB rather than a 422.
	rules := toGodoRules(in.ForwardingRules)
	if len(rules) == 0 {
		rules = []godo.ForwardingRule{{EntryProtocol: "http", EntryPort: 80, TargetProtocol: "http", TargetPort: 80}}
	}
	lb, _, err := o.s.State.lbs.Create(ctx, &godo.LoadBalancerRequest{
		Name:            physicalName(org, name),
		Region:          region,
		Type:            strings.TrimSpace(in.Type), // empty → DO default (REGIONAL)
		SizeSlug:        strings.TrimSpace(in.Size), // empty → DO default
		ForwardingRules: rules,
	})
	if err != nil {
		if s := doStatus(err); s == http.StatusConflict || s == http.StatusUnprocessableEntity {
			return nil, zip.ErrConflict("a load balancer with that name already exists")
		}
		return nil, gatewayErr(err)
	}
	view := toLBView(name, lb)
	return &view, nil
}

// GetLoadBalancer returns one of the caller org's load balancers by id. One that
// exists in another org's namespace is reported 404, never 403 — the same
// existence-oracle guard the VPC read applies.
func (o ops) getLB(ctx context.Context, in *idIn) (*lbView, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := validID(in.ID)
	if !ok {
		return nil, zip.ErrBadRequest("invalid id")
	}
	lb, _, err := o.s.State.lbs.Get(ctx, id)
	if err != nil {
		return nil, notFoundOr(err, "load balancer not found")
	}
	name, ok := friendlyName(orgPrefix(org), lb.Name)
	if !ok {
		return nil, zip.ErrNotFound("load balancer not found")
	}
	view := toLBView(name, lb)
	return &view, nil
}

// DeleteLoadBalancer removes one of the caller org's load balancers and answers
// 204. Ownership is confirmed by re-fetching the resource before anything is
// deleted, so a cross-tenant id is a 404 rather than a delete of another org's
// load balancer.
func (o ops) deleteLB(ctx context.Context, in *idIn) (*noContent, error) {
	org, err := o.org(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := validID(in.ID)
	if !ok {
		return nil, zip.ErrBadRequest("invalid id")
	}
	lb, _, err := o.s.State.lbs.Get(ctx, id)
	if err != nil {
		return nil, notFoundOr(err, "load balancer not found")
	}
	if _, ok := friendlyName(orgPrefix(org), lb.Name); !ok {
		return nil, zip.ErrNotFound("load balancer not found")
	}
	if _, err := o.s.State.lbs.Delete(ctx, id); err != nil {
		return nil, notFoundOr(err, "load balancer not found")
	}
	return nil, nil
}

// ── pagination ──────────────────────────────────────────────────────────────

func allVPCs(s *cloud.Service[state], ctx context.Context) ([]*godo.VPC, error) {
	var out []*godo.VPC
	opt := &godo.ListOptions{PerPage: perPage}
	for page := 1; page <= maxPages; page++ {
		opt.Page = page
		vpcs, resp, err := s.State.vpcs.List(ctx, opt)
		if err != nil {
			return nil, err
		}
		out = append(out, vpcs...)
		if lastPage(resp) {
			break
		}
	}
	return out, nil
}

func allLBs(s *cloud.Service[state], ctx context.Context) ([]godo.LoadBalancer, error) {
	var out []godo.LoadBalancer
	opt := &godo.ListOptions{PerPage: perPage}
	for page := 1; page <= maxPages; page++ {
		opt.Page = page
		lbs, resp, err := s.State.lbs.List(ctx, opt)
		if err != nil {
			return nil, err
		}
		out = append(out, lbs...)
		if lastPage(resp) {
			break
		}
	}
	return out, nil
}

func lastPage(resp *godo.Response) bool {
	return resp == nil || resp.Links == nil || resp.Links.IsLastPage()
}

// ── org resolution + naming (the tenant-isolation boundary) ─────────────────

// org resolves the caller's org and enforces the fail-closed posture in ONE
// place: 503 when DO is unconfigured, 403 when there is no validated principal.
//
// It reads the REQUEST, not just the org, because tenant() below turns on two
// facts the org key alone does not carry — whether the principal was validated,
// and whether it is a SuperAdmin (whose empty org falls back to the "admin"
// namespace). cloud.Bridge parks that request; off the HTTP path there is none,
// and the honest answer is a refusal rather than an invented identity.
func (o ops) org(ctx context.Context) (string, error) {
	if !configured(o.s) {
		return "", zip.Errorf(http.StatusServiceUnavailable, "digitalocean is not configured (DO_API_TOKEN not set)")
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	org, ok := tenant(c)
	if !ok {
		return "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, nil
}

// tenant resolves the caller's org exactly as clients/s3 does: a validated
// principal is REQUIRED (a bearer-less, forgeable X-Org-Id is refused), then the
// org is reduced to the SAME sanitized slug the shared-backend control plane keys
// on. A validated SuperAdmin with no org falls back to the "admin" namespace.
func tenant(c *zip.Ctx) (string, bool) {
	if !principal.Validated(c) {
		return "", false
	}
	if org := namespace.Sanitize(c.Org()); org != "" {
		return org, true
	}
	if c.IsAdmin() {
		return "admin", true
	}
	return "", false
}

// physicalName / orgPrefix / friendlyName reuse the ONE org-hash, DNS-safe naming
// convention every shared-backend subsystem shares (provisioning.BucketName /
// BucketPrefix). The "Bucket" name is historical; the derivation is generic —
// "o"<orgHash>-<name> — so a DO resource is namespaced to its org identically to
// an S3 bucket, and the isolation boundary can never drift between subsystems.
func physicalName(org, friendly string) string { return provisioning.BucketName(org, friendly) }
func orgPrefix(org string) string              { return provisioning.BucketPrefix(org) }

// friendlyName recovers the friendly name from a physical DO resource name that
// carries pfx (the caller's org prefix), or ("",false) when the resource is NOT
// in the caller's namespace. The recovered name is RE-VALIDATED against nameRE:
// a name that carries the prefix but a non-conforming remainder — only reachable
// via an out-of-band DO console create, never through createVPC/createLB — is
// treated as not-owned rather than echoed to the UI, so a list only ever returns
// names a subsequent GET/DELETE can address.
func friendlyName(pfx, physical string) (string, bool) {
	if !strings.HasPrefix(physical, pfx) {
		return "", false
	}
	name := strings.TrimPrefix(physical, pfx)
	if !nameRE.MatchString(name) {
		return "", false
	}
	return name, true
}

// ── helpers ─────────────────────────────────────────────────────────────────

// validID bounds a DigitalOcean resource id taken off the URL. A malformed id is
// a clean 400 before any DO call.
func validID(raw string) (string, bool) {
	id := strings.TrimSpace(raw)
	if !idRE.MatchString(id) {
		return "", false
	}
	return id, true
}

func toGodoRules(rs []fwdRule) []godo.ForwardingRule {
	out := make([]godo.ForwardingRule, 0, len(rs))
	for _, r := range rs {
		out = append(out, godo.ForwardingRule{
			EntryProtocol:  strings.ToLower(strings.TrimSpace(r.EntryProtocol)),
			EntryPort:      r.EntryPort,
			TargetProtocol: strings.ToLower(strings.TrimSpace(r.TargetProtocol)),
			TargetPort:     r.TargetPort,
		})
	}
	return out
}

// doStatus extracts the HTTP status DigitalOcean returned, or 0 when the error is
// not a godo API error (a transport failure).
func doStatus(err error) int {
	var er *godo.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		return er.Response.StatusCode
	}
	return 0
}

// notFoundOr maps a DO 404 to a clean 404 and anything else to a 502 — used by
// GET/DELETE where a missing resource is the expected not-found case.
func notFoundOr(err error, msg string) error {
	if doStatus(err) == http.StatusNotFound {
		return zip.ErrNotFound(msg)
	}
	return gatewayErr(err)
}

// gatewayErr surfaces an upstream DO failure as a 502 with DO's own message,
// never masking it as success — the console renders the honest error card.
func gatewayErr(err error) error {
	return zip.Errorf(http.StatusBadGateway, "digitalocean: %v", err)
}
