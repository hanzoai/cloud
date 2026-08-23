// Package registry is your container and package registry: push images, pull them
// back, see what you store.
//
// It is Hanzo Registry: the management plane over the platform's artifact
// registries — list projects, container images, tags and npm packages, and mint
// scoped pull tokens, on the unified /v1 plane.
//
// PRODUCT-REPO MODEL. The registries themselves are running products: the OCI
// registry at oci.hanzo.ai (github.com/hanzoai/registry — CNCF distribution,
// S3-backed, Hanzo IAM token auth) and the npm registry at pkg.hanzo.ai
// (hanzoai/pkg — verdaccio on S3, with the hanzoai/git forge's /v1/packages
// ecosystems beside it on the same host). This subsystem reimplements NONE of
// them: every op is a typed read of what those services genuinely answer today,
// plus one token mint through the SAME IAM realm the docker CLI uses. cloud
// adds IAM auth, the tenant boundary, and the unified surface (OpenAPI/MCP/
// CLI/SDK projection).
//
// CONTROL PLANE ONLY. The OCI wire — manifests, blobs, push, pull — stays on
// oci.hanzo.ai and is deliberately NOT proxied here: a registry data path
// through the API host would double-move every image byte and break the
// content-addressed client protocol. /v1/registry answers the questions AROUND
// the wire (what exists, who may pull it) and hands out the address of the wire
// itself.
//
// TENANT ISOLATION. The org is the VALIDATED principal's org (principal.Org —
// minted by the identity boundary from a verified credential), NEVER an In
// field. The registries are shared platform deployments, so the boundary is
// enforced HERE on the registries' own namespace conventions: an org's images
// are the catalog entries under `<org>/…` (the fleet's `<host>/<org>/<app>`
// push convention) and its packages are `<org>` and `@<org>/…` on the npm
// host. Reads outside the namespace are filtered out before the response
// exists; a minted token can only ever name `<org>/<image>` with the `pull`
// action.
//
// FAIL-CLOSED. No validated principal → 403 before any upstream byte. The
// catalog credential (REGISTRY_CLIENT_ID/REGISTRY_CLIENT_SECRET, an IAM
// application's service credentials, KMS-synced env) rides only Basic auth to
// the token realm; an upstream that refuses it surfaces 503 — a deployment
// fault, never a caller-auth bug. An unreachable upstream is 503.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/zap-proto/zip"
)

// defaultUpstream is the public OCI registry host (the hanzoai/registry
// deployment). Overridable via REGISTRY_UPSTREAM — tests point it at an
// httptest server speaking the same distribution wire.
const defaultUpstream = "https://oci.hanzo.ai"

// defaultPkg is the public npm registry host (the hanzoai/pkg verdaccio
// deployment). Overridable via REGISTRY_PKG.
const defaultPkg = "https://pkg.hanzo.ai"

// env is a registry base URL, without its trailing slash: every caller appends a
// rooted path, and a doubled slash is a different address to a distribution registry.
func env(key, fallback string) string {
	return strings.TrimRight(environ.Or(key, fallback), "/")
}

func upstream() string { return env("REGISTRY_UPSTREAM", defaultUpstream) }
func pkgHost() string  { return env("REGISTRY_PKG", defaultPkg) }

// credential is the platform's service credential for the OCI token realm: an
// IAM application's client_id/client_secret (the machine path GetRegistryToken
// privileges), KMS-synced into the pod env. Presented ONLY as Basic auth to the
// realm the registry's own 401 challenge advertises — never logged, never
// echoed. Empty is a valid dev posture against an auth-less registry.
func credential() (string, string) {
	return strings.TrimSpace(os.Getenv("REGISTRY_CLIENT_ID")),
		strings.TrimSpace(os.Getenv("REGISTRY_CLIENT_SECRET"))
}

const (
	// timeout bounds one upstream call (a challenge probe, a token mint, one
	// catalog or search page).
	timeout = 20 * time.Second
	// maxBody bounds one upstream response read. Catalog and search pages are
	// small; 8 MiB is far above anything measured and still a bound.
	maxBody = 8 << 20
	// maxPages bounds the catalog walk (1000 names a page). A shared registry
	// beyond 50k repositories is a paging redesign, not a bigger constant.
	maxPages = 50
	// tokenSlack is subtracted from a minted token's lifetime before caching so
	// a token is never handed out or reused within its final seconds.
	tokenSlack = 30 * time.Second
)

// httpClient is the ONE client for every registry call (connection-pooled).
var httpClient = &http.Client{Timeout: timeout + 5*time.Second}

// segRE is the OCI distribution path-component grammar. Every segment of a
// repository name must match it before the name is folded into an upstream URL
// or a token scope, so a hostile value can never smuggle path structure or
// scope structure into the wire.
var segRE = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)

// repoOK validates a full repository name (slash-separated segments, bounded
// total length) against the distribution grammar.
func repoOK(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	for seg := range strings.SplitSeq(name, "/") {
		if !segRE.MatchString(seg) {
			return false
		}
	}
	return true
}

// state is this subsystem's own data: the parsed token challenge and the
// short-lived token cache. The registries own all artifact data; nothing is
// stored here.
type state struct {
	mu     sync.Mutex
	realm  string // token realm advertised by the registry's 401 challenge
	svc    string // token service name from the same challenge
	authed bool   // false when /v2/ answered 200 with no challenge (auth-less dev registry)
	probed bool
	tokens map[string]token // scope → live token, pruned by expiry
}

type token struct {
	value   string
	expires time.Time
}

// Mount wires /v1/registry/* onto app. The subsystem holds no store and runs
// no goroutine: it probes the registries per request and caches only the token
// challenge and the short-lived tokens it minted.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "registry",
		func(cloud.Base) (state, error) {
			return state{tokens: map[string]token{}}, nil
		},
		routes)
}

// ops binds the service to every op on this plane; each op is a method value,
// the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published
// document and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/registry generate` (a prerequisite of build).
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the served /v1/registry surface: six typed ops, nothing
// untyped. Ops are declared on the GROUP, so each op's path is the group's
// prefix composed with its leaf — the identity every projection (document, MCP
// tool, CLI command, SDK method) keys on.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/registry")
	o := ops{s: s}

	// cloud.Bridge is installed by whoever composes the app — the fused host at
	// its root — never here: the validated principal every op below reads is
	// parked on the context by that root install.

	zip.Get(g, "/status", o.status)
	zip.Get(g, "/projects", o.projects)
	zip.Get(g, "/images", o.images)
	zip.Get(g, "/tags", o.tags)
	zip.Get(g, "/packages", o.packages)
	zip.Post(g, "/token", o.token)
}

// ── the shapes the ops take and give ────────────────────────────────────────
//
// A typed op's Go type name IS its schema name and the fleet's schema namespace
// is FLAT, so every name below carries the product prefix.

// registryNoInput is the In of an op that takes nothing off the wire. Its whole
// input is the caller's validated principal.
type registryNoInput struct{}

// registryStatus reports whether the two registry halves are reachable from
// this binary — the OCI host's own /v2/ probe (a live registry answers 200 or
// its token challenge) and the npm host's /-/ping composed into one honest
// lens.
type registryStatus struct {
	// Oci is true when the OCI registry answered its /v2/ probe.
	Oci bool `json:"oci"`
	// Pkg is true when the npm registry answered its ping.
	Pkg bool `json:"pkg"`
	// Host is the OCI registry host clients push to and pull from.
	Host string `json:"host"`
	// PkgHost is the npm registry host.
	PkgHost string `json:"pkgHost"`
	// Realm is the token endpoint the OCI registry's challenge advertises,
	// present only when the registry is reachable and auth-gated.
	Realm string `json:"realm,omitempty"`
	// Service is the token service name from the same challenge.
	Service string `json:"service,omitempty"`
}

// registryProject is one namespace the caller can see, with what it holds on
// each registry half.
type registryProject struct {
	// Project is the namespace: the org's slug, which prefixes its image names
	// and scopes its npm packages.
	Project string `json:"project"`
	// Images is how many of the org's repositories the OCI catalog holds.
	Images int `json:"images"`
	// Packages is how many of the org's packages the npm registry reports.
	Packages int `json:"packages"`
}

// registryProjectList is the caller's project listing. Today an org is exactly
// one namespace, so the list holds one row; the shape stays a list so a
// multi-project org never moves the wire.
type registryProjectList struct {
	// Data is the namespaces visible to the caller.
	Data []registryProject `json:"data"`
}

// registryImage is one container repository the caller's org owns.
type registryImage struct {
	// Name is the repository name inside the org's namespace (e.g. "cloud").
	Name string `json:"name"`
	// Ref is the full pullable reference (host + org + name) the docker CLI
	// takes verbatim.
	Ref string `json:"ref"`
}

// registryImageList is the org's container repositories, read live from the
// OCI catalog.
type registryImageList struct {
	// Data is the org's repositories.
	Data []registryImage `json:"data"`
	// Truncated is true when the catalog walk hit its page bound before the
	// registry was exhausted — the list is a prefix, not the whole.
	Truncated bool `json:"truncated,omitempty"`
}

// registryTags addresses one org-owned repository whose tags to list.
type registryTags struct {
	// Image is the repository name inside the org's namespace, as returned by
	// the images op. It rides the query string.
	Image string `json:"image"`
}

// registryTagList is one repository's tag listing, read live from the OCI
// registry.
type registryTagList struct {
	// Image is the repository name inside the org's namespace.
	Image string `json:"image"`
	// Ref is the full repository reference the tags belong to.
	Ref string `json:"ref"`
	// Data is the tag names, as the registry reports them.
	Data []string `json:"data"`
}

// registryPackages filters the org's npm package listing.
type registryPackages struct {
	// Query narrows the listing within the org's scope when present; the org
	// boundary itself is never widened by it. It rides the query string.
	Query string `json:"query"`
}

// registryPackage is one npm package in the caller's scope.
type registryPackage struct {
	// Name is the package name (`<org>` or `@<org>/…`).
	Name string `json:"name"`
	// Version is the latest published version.
	Version string `json:"version"`
	// Description says what the package is, as published.
	Description string `json:"description,omitempty"`
	// Updated is when the package last changed, as the registry reports it.
	Updated string `json:"updated,omitempty"`
}

// registryPackageList is the org's npm packages, read live from the npm
// registry's search index.
type registryPackageList struct {
	// Data is the packages in the org's scope.
	Data []registryPackage `json:"data"`
}

// registryMint names one org-owned image to mint a pull token for.
type registryMint struct {
	// Image is the repository name inside the org's namespace (e.g. "cloud").
	Image string `json:"image"`
}

// registryToken is a short-lived, pull-only registry token for exactly one of
// the org's images — a capability, not a login.
type registryToken struct {
	// Token is the bearer to present on the OCI wire
	// (`Authorization: Bearer …` against the host's /v2/ routes).
	Token string `json:"token"`
	// Expires is the token's lifetime in seconds.
	Expires int `json:"expires"`
	// Ref is the one repository reference the token can pull.
	Ref string `json:"ref"`
}

// ── the ops ─────────────────────────────────────────────────────────────────

// Status reports whether the OCI and npm registries are reachable and, when
// the OCI half is auth-gated, which token realm its challenge advertises — an
// honest lens for "is the registry plane up", never a fabricated ok.
func (o ops) status(ctx context.Context, _ *registryNoInput) (*registryStatus, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	out := &registryStatus{Host: hostOf(upstream()), PkgHost: hostOf(pkgHost())}
	if realm, svc, authed, err := o.challenge(ctx); err == nil {
		out.Oci = true
		if authed {
			out.Realm, out.Service = realm, svc
		}
	}
	if st, _, err := get(ctx, pkgHost()+"/-/ping", ""); err == nil && st == http.StatusOK {
		out.Pkg = true
	}
	return out, nil
}

// Projects lists the namespaces the caller can see with what each holds: the
// org's slug, its repository count on the OCI catalog, and its package count
// on the npm registry. Today that is exactly one row — the caller's org.
func (o ops) projects(ctx context.Context, _ *registryNoInput) (*registryProjectList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	images, _, err := o.catalog(ctx, org)
	if err != nil {
		return nil, err
	}
	packages, err := search(ctx, org, "")
	if err != nil {
		return nil, err
	}
	return &registryProjectList{Data: []registryProject{{
		Project: org, Images: len(images), Packages: len(packages),
	}}}, nil
}

// Images lists the org's container repositories, read live from the OCI
// catalog and filtered server-side to the org's namespace — the page can only
// ever hold the caller's own images.
func (o ops) images(ctx context.Context, _ *registryNoInput) (*registryImageList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	names, truncated, err := o.catalog(ctx, org)
	if err != nil {
		return nil, err
	}
	out := &registryImageList{Data: []registryImage{}, Truncated: truncated}
	host := hostOf(upstream())
	for _, n := range names {
		out.Data = append(out.Data, registryImage{
			Name: strings.TrimPrefix(n, org+"/"),
			Ref:  host + "/" + n,
		})
	}
	return out, nil
}

// Tags lists one org-owned repository's tags, read live from the OCI registry.
// The repository is addressed inside the org's namespace — a name outside it
// cannot be expressed, and an unknown one answers 404.
func (o ops) tags(ctx context.Context, in *registryTags) (*registryTagList, error) {
	org, repo, err := o.owned(ctx, in.Image)
	if err != nil {
		return nil, err
	}
	st, body, err := o.ociGet(ctx, "/v2/"+repo+"/tags/list", "repository:"+repo+":pull")
	if err != nil {
		return nil, err
	}
	if st == http.StatusNotFound {
		return nil, zip.ErrNotFound("image not found")
	}
	if st != http.StatusOK {
		return nil, upstreamErr(st)
	}
	var v struct {
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "registry upstream: malformed tag list")
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	return &registryTagList{
		Image: strings.TrimPrefix(repo, org+"/"),
		Ref:   hostOf(upstream()) + "/" + repo,
		Data:  v.Tags,
	}, nil
}

// Packages lists the org's npm packages — `<org>` and `@<org>/…` — from the
// npm registry's search index, optionally narrowed by a query within that
// scope. The org boundary is applied server-side after the search, so a query
// can never widen it.
func (o ops) packages(ctx context.Context, in *registryPackages) (*registryPackageList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := search(ctx, org, strings.TrimSpace(in.Query))
	if err != nil {
		return nil, err
	}
	return &registryPackageList{Data: rows}, nil
}

// Token mints a short-lived, pull-only registry token for exactly one of the
// org's images, through the same IAM realm the docker CLI authenticates
// against. The scope is pinned server-side to `<org>/<image>` with the `pull`
// action — no field exists to name another org's image or ask for push. Use it
// as `Authorization: Bearer …` on the OCI wire; it expires in minutes.
//
// Example: {"image": "cloud"}
func (o ops) token(ctx context.Context, in *registryMint) (*registryToken, error) {
	_, repo, err := o.owned(ctx, in.Image)
	if err != nil {
		return nil, err
	}
	realm, svc, authed, err := o.challenge(ctx)
	if err != nil {
		return nil, err
	}
	if !authed {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "registry advertises no token auth")
	}
	tok, expires, err := mint(ctx, realm, svc, "repository:"+repo+":pull")
	if err != nil {
		return nil, err
	}
	return &registryToken{Token: tok, Expires: expires, Ref: hostOf(upstream()) + "/" + repo}, nil
}

// ── tenancy ─────────────────────────────────────────────────────────────────

// owned is the addressing gate every per-image op passes through: resolve the
// caller's org, validate the image name's shape, and compose the ONE
// repository name the op may touch — `<org>/<image>`. The org segment comes
// from the principal, so a foreign repository cannot be expressed at all.
func (o ops) owned(ctx context.Context, image string) (org, repo string, err error) {
	org, err = principal.Acting(ctx)
	if err != nil {
		return "", "", err
	}
	image = strings.TrimSpace(image)
	repo = org + "/" + image
	if image == "" || !repoOK(repo) {
		return "", "", zip.ErrBadRequest("image must be a registry path (lowercase letters, digits, separators)")
	}
	return org, repo, nil
}

// ── the OCI client ──────────────────────────────────────────────────────────

// challenge probes GET /v2/ once per process and caches what it learns: an
// auth-gated registry answers 401 with the token realm and service in its
// WWW-Authenticate header (the wire's own service discovery); an auth-less one
// answers 200. Anything else is unreachable.
func (o ops) challenge(ctx context.Context) (realm, svc string, authed bool, err error) {
	st := &o.s.State
	st.mu.Lock()
	if st.probed {
		defer st.mu.Unlock()
		return st.realm, st.svc, st.authed, nil
	}
	st.mu.Unlock()

	status, _, hdr, err := head(ctx, upstream()+"/v2/")
	if err != nil {
		return "", "", false, zip.Errorf(http.StatusServiceUnavailable, "registry unavailable")
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		realm, svc = parseChallenge(hdr.Get("WWW-Authenticate"))
		if realm == "" {
			return "", "", false, zip.Errorf(http.StatusBadGateway, "registry upstream: 401 without a token challenge")
		}
		authed = true
	default:
		return "", "", false, upstreamErr(status)
	}
	st.mu.Lock()
	st.realm, st.svc, st.authed, st.probed = realm, svc, authed, true
	st.mu.Unlock()
	return realm, svc, authed, nil
}

// challengeRE pulls one `key="value"` pair out of a Bearer challenge.
var challengeRE = regexp.MustCompile(`(\w+)="([^"]*)"`)

// parseChallenge reads realm and service out of a WWW-Authenticate Bearer
// header.
func parseChallenge(h string) (realm, svc string) {
	if !strings.HasPrefix(strings.TrimSpace(h), "Bearer ") {
		return "", ""
	}
	for _, m := range challengeRE.FindAllStringSubmatch(h, -1) {
		switch m[1] {
		case "realm":
			realm = m[2]
		case "service":
			svc = m[2]
		}
	}
	return realm, svc
}

// mint asks the realm for a token with the given scope, presenting the
// platform credential as Basic auth — the same protocol the docker CLI speaks.
// The credential rides ONLY that header; a realm that refuses it is a
// deployment fault reported 503, never a caller-auth bug.
func mint(ctx context.Context, realm, svc, scope string) (string, int, error) {
	id, secret := credential()
	if id == "" {
		return "", 0, zip.Errorf(http.StatusServiceUnavailable, "registry credential unset (REGISTRY_CLIENT_ID)")
	}
	u := realm + "?" + url.Values{"service": {svc}, "scope": {scope}}.Encode()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", 0, err
	}
	req.SetBasicAuth(id, secret)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", 0, zip.Errorf(http.StatusServiceUnavailable, "registry token realm unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", 0, zip.Errorf(http.StatusServiceUnavailable, "registry realm refused the platform credential")
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, upstreamErr(resp.StatusCode)
	}
	var v struct {
		Token     string `json:"token"`
		Access    string `json:"access_token"`
		ExpiresIn int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &v); err != nil || (v.Token == "" && v.Access == "") {
		return "", 0, zip.Errorf(http.StatusBadGateway, "registry realm: malformed token response")
	}
	if v.Token == "" {
		v.Token = v.Access
	}
	if v.ExpiresIn <= 0 {
		v.ExpiresIn = 60
	}
	return v.Token, v.ExpiresIn, nil
}

// bearer returns a live token for scope, minting through the realm on miss and
// caching until just before expiry.
func (o ops) bearer(ctx context.Context, scope string) (string, error) {
	realm, svc, authed, err := o.challenge(ctx)
	if err != nil {
		return "", err
	}
	if !authed {
		return "", nil
	}
	st := &o.s.State
	st.mu.Lock()
	if t, ok := st.tokens[scope]; ok && time.Now().Before(t.expires) {
		st.mu.Unlock()
		return t.value, nil
	}
	st.mu.Unlock()
	value, expiresIn, err := mint(ctx, realm, svc, scope)
	if err != nil {
		return "", err
	}
	st.mu.Lock()
	st.tokens[scope] = token{value: value, expires: time.Now().Add(time.Duration(expiresIn)*time.Second - tokenSlack)}
	st.mu.Unlock()
	return value, nil
}

// ociGet issues one authed read against the OCI host: resolve a token for the
// scope (none when the registry is auth-less) and GET the path.
func (o ops) ociGet(ctx context.Context, path, scope string) (int, []byte, error) {
	tok, err := o.bearer(ctx, scope)
	if err != nil {
		return 0, nil, err
	}
	st, body, _, err := getFull(ctx, upstream()+path, tok)
	if err != nil {
		return 0, nil, zip.Errorf(http.StatusServiceUnavailable, "registry unavailable")
	}
	if st == http.StatusUnauthorized || st == http.StatusForbidden {
		return 0, nil, zip.Errorf(http.StatusServiceUnavailable, "registry refused the platform credential")
	}
	return st, body, nil
}

// catalog walks the OCI catalog and returns the repository names under the
// org's namespace, with a truncation flag when the page bound cut the walk
// short. Filtering happens HERE, before any response shape exists — foreign
// names are dropped, never serialized.
func (o ops) catalog(ctx context.Context, org string) ([]string, bool, error) {
	var names []string
	path := "/v2/_catalog?n=1000"
	for page := 0; path != ""; page++ {
		if page == maxPages {
			return names, true, nil
		}
		tok, err := o.bearer(ctx, "registry:catalog:*")
		if err != nil {
			return nil, false, err
		}
		st, body, hdr, err := getFull(ctx, upstream()+path, tok)
		if err != nil {
			return nil, false, zip.Errorf(http.StatusServiceUnavailable, "registry unavailable")
		}
		if st == http.StatusUnauthorized || st == http.StatusForbidden {
			return nil, false, zip.Errorf(http.StatusServiceUnavailable, "registry refused the platform credential")
		}
		if st != http.StatusOK {
			return nil, false, upstreamErr(st)
		}
		var v struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			return nil, false, zip.Errorf(http.StatusBadGateway, "registry upstream: malformed catalog")
		}
		for _, n := range v.Repositories {
			if strings.HasPrefix(n, org+"/") {
				names = append(names, n)
			}
		}
		path = nextLink(hdr.Get("Link"))
	}
	return names, false, nil
}

// nextLink extracts the next-page path from an RFC 5988 Link header
// (`</v2/_catalog?last=…&n=…>; rel="next"`), empty when there is none.
func nextLink(h string) string {
	for part := range strings.SplitSeq(h, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		if i, j := strings.Index(part, "<"), strings.Index(part, ">"); i >= 0 && j > i {
			return part[i+1 : j]
		}
	}
	return ""
}

// ── the npm client ──────────────────────────────────────────────────────────

// search reads the npm registry's search index and returns the packages in the
// org's scope — `<org>` itself and `@<org>/…` — optionally narrowed by a
// caller query. The scope filter runs on the results, so the query cannot
// widen it.
func search(ctx context.Context, org, query string) ([]registryPackage, error) {
	text := org
	if query != "" {
		text = query
	}
	u := pkgHost() + "/-/v1/search?" + url.Values{"text": {text}, "size": {"250"}}.Encode()
	st, body, err := get(ctx, u, "")
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "package registry unavailable")
	}
	if st != http.StatusOK {
		return nil, upstreamErr(st)
	}
	var v struct {
		Objects []struct {
			Updated string `json:"updated"`
			Package struct {
				Name        string `json:"name"`
				Version     string `json:"version"`
				Description string `json:"description"`
				Date        string `json:"date"`
			} `json:"package"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "package registry: malformed search response")
	}
	rows := []registryPackage{}
	for _, obj := range v.Objects {
		name := obj.Package.Name
		if name != org && !strings.HasPrefix(name, "@"+org+"/") {
			continue
		}
		updated := obj.Updated
		if updated == "" {
			updated = obj.Package.Date
		}
		rows = append(rows, registryPackage{
			Name:        name,
			Version:     obj.Package.Version,
			Description: obj.Package.Description,
			Updated:     updated,
		})
	}
	return rows, nil
}

// ── transport ───────────────────────────────────────────────────────────────

// head issues one unauthenticated GET and returns status, body and headers —
// the challenge probe.
func head(ctx context.Context, u string) (int, []byte, http.Header, error) {
	return getFull(ctx, u, "")
}

// get issues one GET (Bearer-authed when tok is set) and returns status+body.
func get(ctx context.Context, u, tok string) (int, []byte, error) {
	st, body, _, err := getFull(ctx, u, tok)
	return st, body, err
}

// getFull is the ONE upstream read: bounded by timeout and maxBody, carrying
// at most a Bearer token, never a credential pair. A transport failure is
// credential-free by construction.
func getFull(ctx context.Context, u, tok string) (int, []byte, http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("registry request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, body, resp.Header, nil
}

// upstreamErr maps a registry-side failure status to the caller's error: 5xx
// become 502 (the upstream broke, this plane did not), everything else relays
// the status without inventing detail.
func upstreamErr(status int) error {
	if status >= 500 {
		return zip.Errorf(http.StatusBadGateway, "registry upstream error (%d)", status)
	}
	return zip.Errorf(status, "registry upstream answered %d", status)
}

// hostOf strips the scheme off an upstream URL for human-facing refs.
func hostOf(u string) string {
	u = strings.TrimPrefix(u, "https://")
	return strings.TrimPrefix(u, "http://")
}
