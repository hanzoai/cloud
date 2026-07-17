// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

// The /v1/iam edge — the tenant gate in front of Hanzo IAM.
//
// WHY. The embedded console reads org members + projects at a clean /v1/iam/*.
// When IAM is folded in-process (Enabled("iam")) that subsystem answers those
// directly; until then (the staged default) this edge forwards them to the
// standalone IAM, so the one-binary console is never left without IAM. Either way
// the request enters at ONE path — /v1/iam/* — and the client stays identical.
//
// WHY THE PIN IS LOAD-BEARING. IAM's own authz is permissive on org-keyed routes
// (a lister with no `organization` returns every tenant's rows), so the org pin
// here — taken from the VALIDATED, server-minted X-Org-Id, never a raw client
// header (see middleware_identity SanitizeIdentity) — is what stops one tenant
// from reading another's. A super admin may cross; a tenant may not.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// reads any org member may do; writes an org admin may do. Nothing else reaches IAM.
var (
	iamReads  = words("get-users", "get-user", "get-roles", "get-organization", "get-organization-projects")
	iamWrites = words("add-user", "update-user", "delete-user", "update-organization", "add-project", "delete-project")
	// keyed: the caller must OWN the org — owner + organization are pinned to their scope.
	iamKeyed = words("get-organization-projects", "add-project", "delete-project")
	// meta: the object is owned by the "admin" org, so it is guarded by NAME, not owner.
	iamMeta = words("get-organization", "update-organization")
	// public: the sign-in surface — unauthenticated BY DESIGN on IAM. Forwarded
	// verbatim BEFORE the tenant gate so the console can reach sign-in before a
	// session exists (without this, /v1/iam/get-app-login and /v1/iam/login 401
	// "sign in to continue" — a chicken-and-egg that bricks console login when the
	// console SPA is served one-binary off cloud, not the standalone IAM host).
	// These carry NO tenant data: login-page config, credential submit, the OAuth
	// token exchange, OIDC discovery, and the login-flow captcha/verification aids.
	iamPublic = words("get-app-login", "login", "signin", "signup",
		"get-captcha", "send-verification-code", "verify-code")
	// publicPrefixes: multi-segment public sign-in routes (the OAuth token endpoint
	// and OIDC discovery live under these).
	iamPublicPrefixes = []string{"oauth/", "login/oauth/", ".well-known/"}
)

type iamEdge struct {
	host string
	cred string
	http *http.Client
}

func newIamEdge() *iamEdge {
	return &iamEdge{host: iamHost(), cred: iamCred(), http: &http.Client{Timeout: 15 * time.Second}}
}

// mount attaches the edge at /v1/iam/*. Registered after the subsystems and before
// the console catch-all, so a real segment answers JSON and an unknown one 404s
// (never the SPA shell).
func (e *iamEdge) mount(app *zip.App) { app.Group("/v1/iam").All("/*", e.pass) }

// pass gates the request to the caller's own org, then forwards it to IAM under
// cloud's service credential, returning IAM's envelope verbatim.
func (e *iamEdge) pass(c *zip.Ctx) error {
	seg := strings.Trim(c.Param("*"), "/")
	// The public sign-in surface forwards to IAM BEFORE the tenant gate — a caller
	// has no session (hence no org) until it signs in. No tenant data crosses here.
	if e.public(seg) {
		q, _ := url.ParseQuery(string(c.Fiber().Request().URI().QueryString()))
		return e.forward(c, seg, q, c.Method() == http.MethodPost, c.Body())
	}
	org, ok := principal.Org(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, e.fail("sign in to continue"))
	}
	write := c.Method() == http.MethodPost
	if write && !iamWrites[seg] || !write && !iamReads[seg] {
		return c.JSON(http.StatusNotFound, e.fail("unknown iam route"))
	}
	super := principal.IsSuperAdmin(c)
	if write && !super && !principal.IsOrgAdmin(c) {
		return c.JSON(http.StatusForbidden, e.fail("org admin required"))
	}

	q, _ := url.ParseQuery(string(c.Fiber().Request().URI().QueryString()))

	// Every org the request NAMES must be the caller's own (a super admin may cross).
	// "admin" is the metadata owner, allowed only on the org-metadata reads/writes.
	if !e.own(q.Get("owner"), org, super, iamMeta[seg]) ||
		!e.own(e.owner(q.Get("id")), org, super, iamMeta[seg]) ||
		!e.own(q.Get("organization"), org, super, false) {
		return c.JSON(http.StatusForbidden, e.fail("out of scope"))
	}
	// Org metadata is pinned by NAME (its owner is "admin"): a non-super may name only their own org.
	if iamMeta[seg] && !super {
		if n := e.name(q.Get("id")); n != "" && n != org {
			return c.JSON(http.StatusForbidden, e.fail("out of scope"))
		}
	}

	body := c.Body()
	// A scoped WRITE must carry the caller's own org in owner + organization, so an
	// omitted/empty one can't create an owner="" row visible across tenants.
	if write && iamKeyed[seg] && !super {
		if e.field(body, "organization") != org || e.field(body, "owner") != org {
			return c.JSON(http.StatusForbidden, e.fail("out of scope"))
		}
	}
	// Pin the org on org-keyed traffic so an omitted organization can't make IAM's
	// lister drop its filter and return every tenant's rows.
	if iamKeyed[seg] && !super {
		q.Set("organization", org)
	}

	return e.forward(c, seg, q, write, body)
}

func (e *iamEdge) forward(c *zip.Ctx, seg string, q url.Values, write bool, body []byte) error {
	target := e.host + "/v1/iam/" + seg
	if s := q.Encode(); s != "" {
		target += "?" + s
	}
	method, reader := http.MethodGet, io.Reader(nil)
	if write {
		method, reader = http.MethodPost, strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(c.Context(), method, target, reader)
	if err != nil {
		return c.JSON(http.StatusBadGateway, e.fail("iam request failed"))
	}
	req.Header.Set("Accept", "application/json")
	if write {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.cred != "" {
		req.Header.Set("Authorization", e.cred)
	}
	res, err := e.http.Do(req)
	if err != nil {
		return c.JSON(http.StatusBadGateway, e.fail("identity service unavailable"))
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	ct := res.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	c.SetHeader("Content-Type", ct)
	return c.Bytes(res.StatusCode, out)
}

// public reports whether seg is an unauthenticated-by-design sign-in route that
// must be reachable before a session exists — an exact match in iamPublic or a
// multi-segment route under a public prefix (the OAuth token endpoint / OIDC
// discovery). Everything else stays behind the tenant gate.
func (e *iamEdge) public(seg string) bool {
	if iamPublic[seg] {
		return true
	}
	for _, p := range iamPublicPrefixes {
		if strings.HasPrefix(seg, p) {
			return true
		}
	}
	return false
}

// own reports whether the caller may NAME org v: empty is a no-op, a super admin
// crosses, "admin" is allowed only where the segment scopes to the caller itself
// (metadata reads), and otherwise v must equal the caller's scope.
func (e *iamEdge) own(v, org string, super, metaOK bool) bool {
	if v == "" || super {
		return true
	}
	if metaOK && v == "admin" {
		return true
	}
	return v == org
}

// owner / name split an `<owner>/<name>` id; a bare id is returned whole as either.
func (e *iamEdge) owner(id string) string {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i]
	}
	return id
}

func (e *iamEdge) name(id string) string {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[i+1:]
	}
	return id
}

// field reads a string key from a JSON body (best-effort — a write carries it).
func (e *iamEdge) field(body []byte, key string) string {
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		if s, ok := m[key].(string); ok {
			return s
		}
	}
	return ""
}

func (e *iamEdge) fail(msg string) map[string]any {
	return map[string]any{"status": "error", "msg": msg}
}

func words(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
