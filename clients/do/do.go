// Package do mounts the Hanzo Cloud DigitalOcean-native infra surface —
// /v1/vpcs and /v1/load-balancers — on the unified cloud binary (HIP-0106).
// DigitalOcean is Hanzo's EXCLUSIVE cloud venue; VPCs and Load Balancers are
// first-class DO resources, so this subsystem is a thin, org-scoped facade over
// the digitalocean/godo SDK's native VPCs + LoadBalancers services. It backs the
// console's "VPC" and "Load Balancers" pages, which render "not connected" today
// because nothing serves them.
//
//	GET    /v1/vpcs                 list the caller's VPCs            -> {vpcs:[...]}
//	POST   /v1/vpcs                 create {name,region,ip_range}     -> Vpc
//	GET    /v1/vpcs/:id             one VPC (owned)                   -> Vpc
//	DELETE /v1/vpcs/:id             delete one VPC (owned)
//	GET    /v1/load-balancers       list the caller's LBs             -> {loadBalancers:[...]}
//	POST   /v1/load-balancers       create {name,region,...}          -> LoadBalancer
//	GET    /v1/load-balancers/:id   one LB (owned)                    -> LoadBalancer
//	DELETE /v1/load-balancers/:id   delete one LB (owned)
//
// TENANT ISOLATION — DigitalOcean is a SINGLE account, so the org boundary is
// enforced by this subsystem, not by DO. A resource's PHYSICAL DO name is derived
// from the caller's validated org as "o"<orgHash>-<friendly> — the SAME org-hash,
// DNS-safe convention clients/s3 + clients/provisioning use for shared backends
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
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/provisioning"
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

// Mount wires /v1/vpcs/* and /v1/load-balancers/* onto app — one line over the
// generic subsystem entrypoint. Routes register unconditionally (even when
// unconfigured) so the surface owns its space and fails closed under its own name
// rather than 404-ing to a fallthrough.
func Mount(app *zip.App, deps cloud.Deps) error {
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
		b.Log.Info("digitalocean subsystem mounted", "prefix", "/v1/vpcs,/v1/load-balancers", "brand", b.Brand, "env", b.Env)
	}
	return st, nil
}

// routes is the ONE place the surface is wired — shared by Mount (real godo) and
// the test (injected fakes). Static list/create register before the :id param
// route so an id can never shadow the collection handler.
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/vpcs", cloud.Handle(s, listVPCs))
	app.Post("/v1/vpcs", cloud.Handle(s, createVPC))
	app.Get("/v1/vpcs/:id", cloud.Handle(s, getVPC))
	app.Delete("/v1/vpcs/:id", cloud.Handle(s, deleteVPC))

	app.Get("/v1/load-balancers", cloud.Handle(s, listLBs))
	app.Post("/v1/load-balancers", cloud.Handle(s, createLB))
	app.Get("/v1/load-balancers/:id", cloud.Handle(s, getLB))
	app.Delete("/v1/load-balancers/:id", cloud.Handle(s, deleteLB))
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

// ── VPC handlers ────────────────────────────────────────────────────────────

func listVPCs(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := begin(s, c)
	if err != nil {
		return err
	}
	all, err := allVPCs(s, c.Context())
	if err != nil {
		return gatewayErr(err)
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
	return c.JSON(http.StatusOK, map[string]any{"vpcs": out})
}

type createVPCReq struct {
	Name    string `json:"name"`
	Region  string `json:"region"`
	IPRange string `json:"ip_range"`
}

func createVPC(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := begin(s, c)
	if err != nil {
		return err
	}
	var body createVPCReq
	if err := c.Bind(&body); err != nil {
		return zip.ErrBadRequest("invalid JSON body")
	}
	name := strings.TrimSpace(body.Name)
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	region := strings.TrimSpace(body.Region)
	if region == "" {
		return zip.ErrBadRequest("region is required")
	}
	v, _, err := s.State.vpcs.Create(c.Context(), &godo.VPCCreateRequest{
		Name:        physicalName(org, name),
		RegionSlug:  region,
		IPRange:     strings.TrimSpace(body.IPRange), // empty → DO auto-assigns
		Description: "managed by Hanzo Cloud",
	})
	if err != nil {
		if s := doStatus(err); s == http.StatusConflict || s == http.StatusUnprocessableEntity {
			return zip.ErrConflict("a vpc with that name already exists")
		}
		return gatewayErr(err)
	}
	return c.JSON(http.StatusCreated, toVPCView(name, v))
}

func getVPC(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := begin(s, c)
	if err != nil {
		return err
	}
	id, ok := idParam(c)
	if !ok {
		return zip.ErrBadRequest("invalid id")
	}
	v, _, err := s.State.vpcs.Get(c.Context(), id)
	if err != nil {
		return notFoundOr(err, "vpc not found")
	}
	name, ok := friendlyName(orgPrefix(org), v.Name)
	if !ok {
		return zip.ErrNotFound("vpc not found") // not the caller's — existence-oracle guard
	}
	return c.JSON(http.StatusOK, toVPCView(name, v))
}

func deleteVPC(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := begin(s, c)
	if err != nil {
		return err
	}
	id, ok := idParam(c)
	if !ok {
		return zip.ErrBadRequest("invalid id")
	}
	// Confirm ownership by name prefix BEFORE deleting — a cross-tenant id is 404,
	// never a delete of another org's VPC.
	v, _, err := s.State.vpcs.Get(c.Context(), id)
	if err != nil {
		return notFoundOr(err, "vpc not found")
	}
	if _, ok := friendlyName(orgPrefix(org), v.Name); !ok {
		return zip.ErrNotFound("vpc not found")
	}
	if _, err := s.State.vpcs.Delete(c.Context(), id); err != nil {
		return notFoundOr(err, "vpc not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ── Load Balancer handlers ──────────────────────────────────────────────────

func listLBs(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := begin(s, c)
	if err != nil {
		return err
	}
	all, err := allLBs(s, c.Context())
	if err != nil {
		return gatewayErr(err)
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
	return c.JSON(http.StatusOK, map[string]any{"loadBalancers": out})
}

type fwdRule struct {
	EntryProtocol  string `json:"entry_protocol"`
	EntryPort      int    `json:"entry_port"`
	TargetProtocol string `json:"target_protocol"`
	TargetPort     int    `json:"target_port"`
}

type createLBReq struct {
	Name            string    `json:"name"`
	Region          string    `json:"region"`
	Type            string    `json:"type"`
	Size            string    `json:"size"`
	ForwardingRules []fwdRule `json:"forwarding_rules"`
}

func createLB(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := begin(s, c)
	if err != nil {
		return err
	}
	var body createLBReq
	if err := c.Bind(&body); err != nil {
		return zip.ErrBadRequest("invalid JSON body")
	}
	name := strings.TrimSpace(body.Name)
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	region := strings.TrimSpace(body.Region)
	if region == "" {
		return zip.ErrBadRequest("region is required")
	}
	// DO requires at least one forwarding rule. When the caller omits them, default
	// to plain HTTP 80→80 — the same default DO's own console applies — so a
	// minimal create yields a REAL, usable LB rather than a 422.
	rules := toGodoRules(body.ForwardingRules)
	if len(rules) == 0 {
		rules = []godo.ForwardingRule{{EntryProtocol: "http", EntryPort: 80, TargetProtocol: "http", TargetPort: 80}}
	}
	lb, _, err := s.State.lbs.Create(c.Context(), &godo.LoadBalancerRequest{
		Name:            physicalName(org, name),
		Region:          region,
		Type:            strings.TrimSpace(body.Type), // empty → DO default (REGIONAL)
		SizeSlug:        strings.TrimSpace(body.Size), // empty → DO default
		ForwardingRules: rules,
	})
	if err != nil {
		if s := doStatus(err); s == http.StatusConflict || s == http.StatusUnprocessableEntity {
			return zip.ErrConflict("a load balancer with that name already exists")
		}
		return gatewayErr(err)
	}
	return c.JSON(http.StatusCreated, toLBView(name, lb))
}

func getLB(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := begin(s, c)
	if err != nil {
		return err
	}
	id, ok := idParam(c)
	if !ok {
		return zip.ErrBadRequest("invalid id")
	}
	lb, _, err := s.State.lbs.Get(c.Context(), id)
	if err != nil {
		return notFoundOr(err, "load balancer not found")
	}
	name, ok := friendlyName(orgPrefix(org), lb.Name)
	if !ok {
		return zip.ErrNotFound("load balancer not found")
	}
	return c.JSON(http.StatusOK, toLBView(name, lb))
}

func deleteLB(s *cloud.Service[state], c *zip.Ctx) error {
	org, err := begin(s, c)
	if err != nil {
		return err
	}
	id, ok := idParam(c)
	if !ok {
		return zip.ErrBadRequest("invalid id")
	}
	lb, _, err := s.State.lbs.Get(c.Context(), id)
	if err != nil {
		return notFoundOr(err, "load balancer not found")
	}
	if _, ok := friendlyName(orgPrefix(org), lb.Name); !ok {
		return zip.ErrNotFound("load balancer not found")
	}
	if _, err := s.State.lbs.Delete(c.Context(), id); err != nil {
		return notFoundOr(err, "load balancer not found")
	}
	return c.NoContent(http.StatusNoContent)
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

// begin resolves the caller's org and enforces the fail-closed posture in ONE
// place: 503 when DO is unconfigured, 403 when there is no validated principal.
func begin(s *cloud.Service[state], c *zip.Ctx) (string, error) {
	if !configured(s) {
		return "", zip.Errorf(http.StatusServiceUnavailable, "digitalocean is not configured (DO_API_TOKEN not set)")
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
// on. A validated global admin with no org falls back to the "admin" namespace.
func tenant(c *zip.Ctx) (string, bool) {
	if !principal.Validated(c) {
		return "", false
	}
	if org := provisioning.SanitizeOrg(c.Org()); org != "" {
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

func idParam(c *zip.Ctx) (string, bool) {
	id := strings.TrimSpace(c.Param("id"))
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
