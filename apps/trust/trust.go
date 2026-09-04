// Package trust is your trust centre: the controls you publish, the coverage
// they compute to, the documents a reviewer asks for, and who you send data to.
//
// It serves /v1/trust/*. Any organization can publish one — this is a product,
// not a page — and the deployment's own is simply the first tenant. What
// separates the two is where their controls come from, and that is the only
// difference anywhere in the surface:
//
//   - the DEPLOYMENT's inventory is github.com/hanzoai/trust's controls.json,
//     compiled into the goja bundle, gated at build time by goja/check.mjs, and
//     unreachable from any request. Adding one is a commit.
//   - every OTHER organization authors rows in its own Base/SQLite file, held to
//     the SAME validator, folded by the SAME arithmetic.
//
// So nobody's coverage — including ours — is a number anyone typed. It is a fold
// over an inventory, against the whole published clause list of each framework,
// and an `absent` control still names the clause it would satisfy without ever
// moving a count.
//
// WRAP, DON'T REWRITE (HIP-0106). The logic is TypeScript in
// github.com/hanzoai/trust, bundled for goja; this package is the host. It is
// apps/captable's shape — the reusable apps/goja Base binding, one SQLite file
// per tenant, one transaction per request — with two tenant-bound host globals
// this capability needs and captable does not (goja.BaseConfig.Bind):
//
//	__own()    is this tenant the one whose inventory is compiled in
//	__audit(q) the platform's audit trail, ALREADY bound to this tenant
//
// TWO ENDPOINTS. /v1/trust/* is the caller's OWN centre, resolved from the
// validated bearer. /v1/trust/published/:org is what a visitor with no
// credential reads, and it is a different question, not a weaker check: a
// published trust centre is a public document addressed by a public name, the
// way a site is addressed by its slug. It answers only for an organization that
// has published, it never carries a gated document's address, and it reaches no
// other route.
//
// NEITHER ENDPOINT SERVES BYTES. A document is metadata here — title, kind, date,
// and whether it is released. The bytes and the grant belong to apps/dataroom,
// which already does per-page view tracking; this surface says what exists and
// what it takes to read it, which is the part a reviewer can check.
package trust

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/goja"
	"github.com/hanzoai/cloud/openapi"
	htrust "github.com/hanzoai/trust"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// prefix is the one address this subsystem answers at. Named once: the routes
// compose off it and manifest.PrefixesFor("trust") must agree with it.
const prefix = "/v1/trust"

// maxBody caps a write. A control, a policy or an FAQ entry is a small
// structured record; anything larger is malformed or hostile.
const maxBody = 1 << 20 // 1 MiB

// state is trust's own data; shared deps live in the embedded cloud.Base.
type state struct {
	host *goja.BaseHost
}

// mounted is the active service so Shutdown can release the per-tenant stores.
var mounted *cloud.Service[state]

// Mount wires the /v1/trust/* surface onto app per HIP-0106.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("trust.Use:  nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("trust.Use:  empty DataDir")
	}
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("trust.Use:  router carries no typed-op registry")
	}

	bundle, err := htrust.Bundle()
	if err != nil {
		return fmt.Errorf("trust.Use:  load bundle: %w", err)
	}

	// The organization whose inventory is compiled in is the deployment's OWN
	// brand org, so a white-label deployment publishes ITS controls and not
	// ours. Resolved once, here, because which org that is, is deployment
	// configuration; the bundle only asks whether this tenant is it.
	home := homeOrg(deps)

	host, err := goja.NewBase(goja.BaseConfig{
		Name:    "trust",
		Bundle:  bundle,
		Schema:  htrust.Schema,
		DataDir: cloud.DataDir(),
		OnOpen:  seed(home, cloud.Brand()),
		Bind: func(ctx context.Context, tenant string) map[string]any {
			return map[string]any{
				"__own":   func() bool { return tenant == home },
				"__audit": trail(ctx, deps.Audit, tenant),
			}
		},
	})
	if err != nil {
		return fmt.Errorf("trust.Use:  goja NewBase host: %w", err)
	}

	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "trust"), State: state{host: host}}
	mounted = s
	routes(zapp, s)

	s.Log.Info("trust mounted in-process (goja + per-tenant Base)",
		"prefix", prefix,
		"version", htrust.Version,
		"home", home,
		"trail", deps.Audit != nil,
		"brand", cloud.Brand(),
	)
	return nil
}

// homeOrg is the organization whose compiled-in inventory this deployment
// serves. It is the brand's own org — "hanzo" on api.hanzo.ai, "lux" on
// api.lux.cloud — so a white-label deployment never publishes another brand's
// controls under its own name.
func homeOrg(deps cloud.Deps) string {
	if b := strings.TrimSpace(cloud.Brand()); b != "" {
		return strings.ToLower(b)
	}
	return "hanzo"
}

// seed gives the deployment's OWN organization a published profile the first
// time its store opens, and every other organization nothing.
//
// The reason is that our inventory is already public — it is compiled into the
// bundle and committed in the open — so a deployment whose own centre answered
// 404 until somebody posted a profile would be withholding something it has
// already published. A customer's centre is the opposite case: theirs is empty
// and unpublished until they decide otherwise, which is why this seeds one
// organization and not all of them.
//
// It runs ONCE, on first open, and never overwrites: an organization that has
// edited its profile keeps what it wrote.
func seed(home, brand string) func(context.Context, string, *sql.DB) error {
	return func(ctx context.Context, tenant string, db *sql.DB) error {
		if tenant != home {
			return nil
		}
		name := strings.TrimSpace(brand)
		if name == "" {
			name = home
		}
		profile := map[string]any{
			"name":      cloud.BrandDisplay(name),
			"tagline":   "How we run, in public.",
			"summary":   "Every control here names the repository and file where its mechanism lives, and every coverage number is computed from that inventory against each framework's whole published clause list.",
			"published": true,
		}
		body, err := json.Marshal(profile)
		if err != nil {
			return err
		}
		_, err = db.ExecContext(ctx,
			`INSERT INTO record (kind, id, ord, data, updated) VALUES ('profile', '', 0, ?, ?)
			 ON CONFLICT(kind, id) DO NOTHING`,
			string(body), time.Now().UnixMilli())
		return err
	}
}

// trail builds the __audit bridge for ONE tenant, or nil when this deployment
// has no tamper-evident store.
//
// The query it accepts carries no organization, and this is where that property
// is made true: Org is pinned to the tenant the host resolved, and the bundle
// has no field in which to name another. A tenant the caller cannot express is
// a tenant the caller cannot cross.
//
// nil is deliberate and is NOT an empty result. Without __audit the bundle
// answers 501 and says the trail was not read; an empty list would read as "we
// looked and found nothing", which is a different claim and a false one.
func trail(ctx context.Context, rec *audit.Recorder, tenant string) any {
	if rec == nil {
		return nil
	}
	return func(q map[string]any) map[string]any {
		f := audit.Filter{Org: tenant, Actions: strs(q["actions"]), Limit: intOf(q["limit"])}
		if t, ok := stamp(q["from"]); ok {
			f.Since = t
		}
		if t, ok := stamp(q["to"]); ok {
			f.Until = t
		}
		rows, total, err := rec.Query(ctx, f)
		if err != nil {
			// The bundle has no vocabulary for "the store failed", and inventing
			// an empty page here would report a clean quarter. Panicking inside
			// goja surfaces as the bundle's own 500 with this sentence.
			panic("trust: audit query failed: " + err.Error())
		}
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.ToWire())
		}
		return map[string]any{"rows": out, "total": total}
	}
}

// strs reads a JS string array off a goja-exported value.
func strs(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// intOf reads a JS number off a goja-exported value. goja hands back int64 for a
// whole number and float64 otherwise, so both are read.
func intOf(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// stamp parses one RFC 3339 bound. The bundle has already refused a malformed
// one with a 400, so anything arriving here is well formed or empty; an
// unparseable value leaves the bound unset rather than widening the window by
// guessing.
func stamp(v any) (time.Time, bool) {
	s, _ := v.(string)
	if s = strings.TrimSpace(s); s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// routes wires the /v1/trust surface.
//
// Every op is TYPED, which is what makes each one an OpenAPI operation with a
// schema, an MCP tool, a CLI command and a method in eight generated SDKs
// rather than an address and nothing else.
//
// They are declared on the GROUP, so an op's path is the prefix composed with
// its leaf — the identity every projection keys on, and the form cmd/zipdoc
// resolves. cloud.Bridge is absent on purpose: the composer owns that install,
// and it is what parks the validated org a typed op reads off the context.
func routes(zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}
	g := zapp.Group(prefix)

	// No /health here. The composer registers GET /v1/<name>/health for every app
	// that does not own its own (serve.go), and a second declaration of one
	// address is not a shadowing risk but a REFUSAL — zip will not compose the
	// program at all, so the plugin binary panics at startup. Nothing is lost:
	// which inventory is running rides on every substantive answer as `version`.
	zip.Get(g, "/published/:org", o.published)
	zip.Get(g, "/profile", o.profile)
	zip.Get(g, "/controls", o.listControls)
	zip.Get(g, "/controls/:id", o.getControl)
	zip.Get(g, "/frameworks", o.listFrameworks)
	zip.Get(g, "/coverage", o.coverage)
	zip.Get(g, "/coverage/:framework", o.frameworkCoverage)
	zip.Get(g, "/documents", o.listDocuments)
	zip.Get(g, "/subprocessors", o.listSubprocessors)
	zip.Get(g, "/policies", o.listPolicies)
	zip.Get(g, "/faq", o.listFaq)
	zip.Get(g, "/updates", o.listUpdates)
	zip.Get(g, "/risk", o.risk)
	zip.Get(g, "/evidence", o.evidence)

	// The caller's own whole centre, one request. Declared on the APP with its
	// absolute path because this op IS the prefix: zip.Get(g, "") composes to
	// "/v1/trust/", a different address, which would ship in the document and in
	// every SDK. Same reason apps/plan and apps/pricing declare theirs absolutely.
	zip.Get(zapp, prefix, o.center)

	// Writes. One address per section, so a caller names the section in the path
	// and the bundle's one write path validates it.
	zip.Put(g, "/:kind/:id", o.put)
	zip.Delete(g, "/:kind/:id", o.remove)

}

// The published endpoint renders `security: []`. A published trust centre is a
// public document, and requiring a bearer to read one would defeat the point of
// publishing it. It reaches nothing else: the bundle refuses every other route
// without the validated tenant, and this one refuses an organization that has
// not published.
//
// Declared in init() and not in routes(): openapi.Open registers into a
// PROCESS-WIDE table and panics on a duplicate, so a second Mount in one process
// — which is what a test binary does, and what a host that remounts would do —
// would take the program down on a declaration that has not changed.
func init() {
	openapi.Open(prefix+"/published/{org}", "GET")
}

// Shutdown closes the per-tenant stores + the goja engine. Idempotent.
func Shutdown(context.Context) error {
	if mounted == nil || mounted.State.host == nil {
		return nil
	}
	err := mounted.State.host.Close()
	mounted = nil
	return err
}

// log is the subsystem logger, for the one failure that is the host's rather
// than the caller's.
func (o ops) log() luxlog.Logger { return o.s.Log }
