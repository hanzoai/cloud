// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

// The Base DATA-PLANE forward (/v1/collections[/*]).
//
// The console's Base product (the Bases manager + the Records browser) drives a
// managed Hanzo Base's REST surface — list content types, CRUD a collection's
// records — at /v1/collections/*. The managed Base that carries the org's Bases
// registry (the `tenants` collection) plus their data is a SEPARATE deployment
// (base.hanzo.ai, the SuperBase orchestrator, in-cluster Service `base`); the
// standalone Next.js console reached it through a same-origin BFF
// (app/v1/superbase/[...path]) that forwarded to that deployment.
//
// In the go:embed console (the SPA served BY this cloud binary) that BFF is pruned
// — the client calls cloud's native /v1 directly — so /v1/collections/* has to be
// served HERE or every Base call 404s and the whole product goes dark. This file is
// that forward: a principal-gated reverse proxy to the managed Base, the in-binary
// replacement for the retired console BFF (the exact clients/o11y reverse-proxy
// pattern). It is ORTHOGONAL to the per-org embed lanes (which host a Base engine IN
// this binary at /v1/base/*) — this bridges the console to the managed orchestrator
// that owns the cross-instance `tenants` registry the embed does not.
//
// AUTH — ONE credential, forwarded (never minted). base.hanzo.ai validates a
// hanzo.id JWT against IAM's JWKS (StoreKeyExternalAuthOnly) and scopes each
// `tenants` row by the token subject (its ListRule owner_iam_user = @request.auth.id).
// cloud validates the SAME hanzo.id tokens but holds only the JWKS PUBLIC key, so it
// cannot MINT one to present — it forwards the caller's OWN Bearer (already on the
// request; the console's Base client sends it). SanitizeIdentity has already stripped
// any client-forged X-Org-Id and re-injected the validated one, so the org header
// that rides the proxy is authoritative. The principal gate refuses a bearer-less or
// forged call before it ever reaches the managed Base.
//
// LEAST PRIVILEGE — this is a COLLECTIONS proxy, not a Base tunnel. allowCollections
// admits EXACTLY the data plane the console uses (the schema list + create, the
// scaffolds palette, one content type, a collection's records) and refuses Base's
// non-collection admin (settings / backups / logs) — the byte-identical boundary the
// retired console BFF's allowBaseSurface enforced.

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// defaultOrchestrator is the in-cluster Service of the managed Base (base.hanzo.ai)
// that holds the org Bases registry. In-cluster (never the public host) so the DOKS
// pod's egress is not Cloudflare-gated. Overridable via CLOUD_BASE_ORCHESTRATOR_URL.
const defaultOrchestrator = "http://base.hanzo.svc.cluster.local:80"

// defaultOrchestratorHost is the vhost the managed Base is addressed as (its own
// canonical identity), forwarded as the upstream Host while we CONNECT to the
// in-cluster Service IP. Overridable via CLOUD_BASE_ORCHESTRATOR_HOST.
const defaultOrchestratorHost = "base.hanzo.ai"

func orchestratorURL() string {
	if v := strings.TrimSpace(os.Getenv("CLOUD_BASE_ORCHESTRATOR_URL")); v != "" {
		return v
	}
	return defaultOrchestrator
}

func orchestratorHost() string {
	if v := strings.TrimSpace(os.Getenv("CLOUD_BASE_ORCHESTRATOR_HOST")); v != "" {
		return v
	}
	return defaultOrchestratorHost
}

// mountCollections wires the /v1/collections[/*] Base data-plane forward. Always on
// (independent of the per-org embed gate): the console's Base product needs it
// whether or not this binary ALSO hosts per-org Bases at /v1/base/*.
func mountCollections(app cloud.Router) error {
	proxy, err := newCollectionsProxy(orchestratorURL(), orchestratorHost())
	if err != nil {
		return fmt.Errorf("base.mountCollections: %w", err)
	}
	h := func(c *zip.Ctx) error { return serveCollections(proxy, c) }
	app.All("/v1/collections", h)
	app.All("/v1/collections/*", h)
	return nil
}

// newCollectionsProxy builds the reverse proxy to the managed Base. Pure (URL+host
// in, handler out) so it is unit-testable without a live upstream. The path is
// forwarded UNCHANGED (1:1 with base's own /v1/collections/*); only scheme/host and
// the upstream vhost Host are set.
func newCollectionsProxy(rawURL, host string) (http.Handler, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)   // scheme/host = target; path unchanged
		r.Host = host // the managed Base's own vhost, not the Service name
	}
	return proxy, nil
}

// serveCollections gates on a validated principal, bounds the path to the Base data
// plane, then forwards. The caller's Bearer and the validated X-Org-Id ride the
// request unchanged (SanitizeIdentity already re-injected the trusted org), so the
// managed Base authorizes every read/write itself against the forwarded identity.
func serveCollections(proxy http.Handler, c *zip.Ctx) error {
	if !principal.Validated(c) {
		return zip.ErrForbidden("sign in to manage Base")
	}
	if !allowCollections(c.Path()) {
		return zip.Errorf(http.StatusNotFound, "not found")
	}
	return zip.AdaptNetHTTP(proxy)(c)
}

// allowCollections is the least-privilege boundary — the twin of the retired console
// BFF's allowBaseSurface. It admits ONLY:
//
//	/v1/collections                      list schemas (GET) + create a type (POST)
//	/v1/collections/meta/scaffolds       the field-template palette
//	/v1/collections/<name>               view / update / delete ONE content type
//	/v1/collections/<name>/records       that type's records (list / create)
//	/v1/collections/<name>/records/<id>  one record (get / update / delete)
//
// and refuses everything else Base mounts (settings / backups / logs), so
// /v1/collections stays a collections proxy, never a general Base tunnel.
func allowCollections(path string) bool {
	rel := strings.Trim(path, "/")
	if rel == "v1/collections" || rel == "v1/collections/meta/scaffolds" {
		return true
	}
	rest, ok := strings.CutPrefix(rel, "v1/collections/")
	if !ok || rest == "" {
		return false
	}
	segs := strings.Split(rest, "/")
	switch len(segs) {
	case 1: // <name>
		return segs[0] != ""
	case 2: // <name>/records
		return segs[0] != "" && segs[1] == "records"
	case 3: // <name>/records/<id>
		return segs[0] != "" && segs[1] == "records" && segs[2] != ""
	default:
		return false
	}
}
