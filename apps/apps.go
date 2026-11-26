// Package apps is the composition root for the OSS Hanzo Cloud core: the single,
// explicit list of which subsystems are linked into the binary AND the order they
// mount in.
//
// Wire() returns []cloud.MountSpec in mount order (slice position == order). There
// is no init()-registry and no order-int: adding, removing, or reordering a
// subsystem is a one-line edit to Wire(), read top-to-bottom. cmd/cloud calls
// Wire() and threads the slice into cloud.Serve — the set is defined ONCE, here.
//
// This is the local dev server: every subsystem here runs on one machine with a
// data directory as its only dependency.
//
// (This package must NOT live in package cloud: the subsystems import cloud for
// Deps + Typed, so a root-package bundle would form an import cycle. As a sibling
// subpackage it composes them without one.)
package apps

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/agenttrace"
	"github.com/hanzoai/cloud/apps/blueprint"
	"github.com/hanzoai/cloud/apps/fanclub"
	"github.com/hanzoai/cloud/apps/template"
	"github.com/hanzoai/cloud/apps/x402"
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients"
	"github.com/hanzoai/cloud/clients/principal"

	// In-repo subsystem packages (clients/*). Each exports a Mount (and, where it
	// owns process-lifetime resources, a Shutdown); Wire references them directly.
	"github.com/hanzoai/cloud/clients/auditlog"
	"github.com/hanzoai/cloud/clients/base"
	"github.com/hanzoai/cloud/clients/code"
	"github.com/hanzoai/cloud/clients/dns"
	"github.com/hanzoai/cloud/clients/exec"
	"github.com/hanzoai/cloud/clients/flags"
	"github.com/hanzoai/cloud/clients/functions"
	"github.com/hanzoai/cloud/clients/gateway"
	"github.com/hanzoai/cloud/clients/ingress"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/cloud/clients/kv"
	"github.com/hanzoai/cloud/clients/plugin"
	"github.com/hanzoai/cloud/clients/security"
	"github.com/hanzoai/cloud/clients/session"
	"github.com/hanzoai/cloud/clients/storage"
	"github.com/hanzoai/cloud/clients/tasks"
)

// Wire returns every linked subsystem as a cloud.MountSpec, in mount order. The
// slice position IS the order: cloud.MountAll iterates it as-given, registering
// each subsystem's teardown as a zip shutdown hook so teardown runs in reverse
// (LIFO). Enablement is a separate axis: cloud.Serve mounts only the specs
// cfg.Enabled(name) admits, so a STAGED subsystem is linked but inert until named.
func Wire() []cloud.MountSpec {
	return []cloud.MountSpec{
		// Pay per request over HTTP 402, for the routes an operator prices. It
		// wraps the routes mounted after it, so it comes first; with no
		// configuration it mounts nothing and every route is served as before.
		{Name: "x402", Mount: payments, Description: "Charge per request over HTTP 402 for the routes you price."},

		// Insights feature-flag evaluation seam (no routes; a hot value plane).
		{Name: "flags", Mount: flags.Mount, Shutdown: flags.Shutdown, OwnsHealth: true, Description: "Feature flags: ship it dark, then turn it on for who you pick."},
		// Embedded KMS secrets plane /v1/kms/*. OwnsHealth: serves its own
		// fail-closed /v1/kms/health. Fails closed until CLOUD_KMS_MASTER_KEY_REF.
		{Name: "kms", Mount: kms.Mount, OwnsHealth: true, Description: "Secret custody: your org's secrets sealed at rest."},
		// Embedded runtime edge (/v1/ingress/*). STAGED — edge listeners stay off
		// unless the operator names "ingress" in CLOUD_ENABLE.
		{Name: "ingress", Mount: ingress.Mount, Shutdown: ingress.Shutdown, Description: "Your edge: automatic TLS certificates and hostname routing."},
		// /v1/s3/buckets/* + /v1/s3/health. OwnsHealth (real fail-closed probe).
		{Name: "storage", Mount: storage.Mount, OwnsHealth: true, Prefix: "/v1/s3", Description: "Object storage: your buckets and the files in them."},
		// The /v1/dns forward head: relays the console DNS dashboard to the DNS
		// control plane under the caller's own validated bearer.
		{Name: "dns", Mount: dns.Mount, Description: "Your DNS records, relayed to the DNS control plane under your own bearer."},
		{Name: "code", Mount: code.Mount, Shutdown: code.Shutdown, Description: "Search and symbols across your repos, for you and your agents."},
		{Name: "session", Mount: session.Mount, Shutdown: ctxShutdown(session.Shutdown), Prefix: "/v1/code/sessions", Description: "Live coding-agent runs: list them, watch them, steer them."},
		{Name: "security", Mount: security.Mount, Shutdown: ctxShutdown(security.Shutdown), OwnsHealth: true, Description: "Secret scanning for your code: submit sources, get findings."},
		{Name: "exec", Mount: exec.Mount, Description: "The code interpreter: run a snippet in a sandbox and move files."},
		{Name: "gateway", Mount: gateway.Mount, Description: "Live control of the edge policy your API applies to every request."},
		{Name: "audit", Mount: auditlog.Mount, Description: "Your org's tamper-evident audit trail."},

		// The local apps. Each is a REAL implementation over the embedded store,
		// not a relay to a cluster: the dev edition is meant to be a working
		// product on a laptop with no network, and an app that only forwards
		// somewhere else would make it a client for a thing you do not have.
		//
		// Every route in these is a typed op, which is why they need no
		// per-app integration work: one declaration is simultaneously the REST
		// route, the OpenAPI schema, the MCP tool and the generated SDK method.
		{Name: "base", Mount: base.Mount, Shutdown: base.Shutdown, Description: "A document store per org: collections of JSON records."},
		{Name: "kv", Mount: kv.Mount, Shutdown: kv.Shutdown, Description: "Your key-value store: versioned values your apps read and write."},
		{Name: "tasks", Mount: tasks.Mount, Shutdown: tasks.Shutdown, Description: "Durable work: a lease and acknowledge queue."},
		// STAGED (see stagedSubsystems): functions RUNS CODE, so it is linked in
		// every build but mounts only when an operator names it.
		{Name: "functions", Mount: functions.Mount, Shutdown: functions.Shutdown, Description: "Serverless code: publish it and call it over HTTP."},

		// Apps that import zip and nothing of their host: the same package mounts in
		// any host that hands it a router and the few things it reads. Each takes
		// only what it names, so an entry here is an adapter from cloud.Deps to
		// that and nothing more.
		{Name: "blueprint", Mount: logged("blueprint", blueprint.Mount), OwnsHealth: true, Description: "What a template costs to run: its bill of images and a compute-cost estimate."},

		{Name: "template", Mount: templates, Shutdown: template.Shutdown, Description: "Starter kits you can deploy as they come, and your org's own."},

		{Name: "agenttrace", Mount: agenttraceApp, Shutdown: agenttrace.Shutdown, OwnsHealth: true, Description: "Unified OpenTelemetry GenAI agent execution tracing under one task id across all six Hanzo subsystems."},

		// Opt-in Cloud-native FanClub AI organization intelligence app
		// (CLOUD_ENABLE_STAGED=fanclub). Persists exclusively in Cloud Base,
		// enforces Hanzo IAM identity, and routes inference through deps.AI.
		{Name: "fanclub", Mount: fanclubApp, Description: "Organization intelligence reference app over Cloud Base, IAM, Tasks, MCP, and AI."},

		// Runtime wasm/proxy plugins — mounts dead last.
		{Name: "plugins", Mount: plugin.Mount, Description: "Runtime plugins, wasm and proxy, mounted under /v1/plugins."},
	}
}

// ServeSingle is the ONE way to run a single app standalone: validate `name`
// against Wire(), then serve exactly it (cloud.Serve with a one-name enable list).
// Returns an error for an unknown name rather than booting a no-op.
func ServeSingle(name string) error {
	if name == "" {
		return fmt.Errorf("ServeSingle: empty app name")
	}
	for _, spec := range Wire() {
		if spec.Name == name {
			return cloud.Serve(Wire(), []string{name})
		}
	}
	return fmt.Errorf("ServeSingle: unknown app %q", name)
}

// logged adapts an app that asks its host for a router and a logger to the mount
// contract: the logger is scoped to the app's name, as cloud.Mount scopes it.
func logged(name string, mount func(*zip.App, luxlog.Logger) error) cloud.MountFunc {
	return func(app *zip.App, deps cloud.Deps) error {
		return mount(app, deps.Logger.New("subsystem", name))
	}
}

// payments adapts x402 to cloud.Deps: the operator names a JSON file in
// CLOUD_X402_CONFIG holding the assets, the prices and the facilitator. With none
// named nothing is priced and nothing mounts; a file that does not load stops the
// start, because a price the process did not understand must not leave its route
// free.
func payments(app *zip.App, deps cloud.Deps) error {
	path := os.Getenv("CLOUD_X402_CONFIG")
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("x402: %w", err)
	}
	defer f.Close()
	cfg, err := x402.Load(f)
	if err != nil {
		return err
	}
	return x402.Mount(app, deps.Logger.New("subsystem", "x402"), cfg)
}

// templates adapts the starter-kit gallery to cloud.Deps: the caller is whoever the
// identity boundary validated, and the private kits live in a database under the
// data directory.
func templates(app *zip.App, deps cloud.Deps) error {
	return template.Mount(app, template.Deps{
		Log:    deps.Logger.New("subsystem", "template"),
		Org:    principal.OrgFrom,
		Acting: principal.Acting,
		Open:   opener(deps.DataDir, "templates"),
	})
}

// agenttraceApp adapts apps/agenttrace to cloud.Deps: authenticated org isolation
// comes strictly from the host's validated identity boundary (principal), never
// from untrusted request fields. Global exporter diagnostics additionally require
// validated platform SuperAdmin authority.
func agenttraceApp(app *zip.App, deps cloud.Deps) error {
	return agenttrace.Mount(app, agenttrace.Deps{
		Log:    deps.Logger.New("subsystem", "agenttrace"),
		Org:    principal.OrgFrom,
		Acting: principal.Acting,
		Operator: func(ctx context.Context) error {
			if _, err := principal.Acting(ctx); err != nil {
				return err
			}
			if !zip.CallerOf(ctx).Admin {
				return zip.ErrForbidden("platform SuperAdmin authority required for global trace diagnostics")
			}
			return nil
		},
	})
}

// fanclubBase adapts Cloud Base's in-process store to apps/fanclub.BaseStore so
// FanClub holds zero database of its own — all fc_members, fc_proposals, and
// fc_audit records live in Cloud Base (base.db) under the caller's IAM org.
type fanclubBase struct{}

func (fanclubBase) Collections(ctx context.Context, org string) ([]fanclub.BaseCollectionCount, error) {
	cols, err := base.CollectionsOrg(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]fanclub.BaseCollectionCount, len(cols))
	for i, c := range cols {
		out[i] = fanclub.BaseCollectionCount{Name: c.Name, Records: c.Records}
	}
	return out, nil
}

func (fanclubBase) List(ctx context.Context, org, collection string, limit, offset int) ([]fanclub.BaseRecord, error) {
	recs, err := base.ListOrg(ctx, org, collection, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]fanclub.BaseRecord, len(recs))
	for i, r := range recs {
		out[i] = fanclub.BaseRecord{Collection: r.Collection, ID: r.ID, Doc: r.Doc, Created: r.Created, Updated: r.Updated}
	}
	return out, nil
}

func (fanclubBase) Get(ctx context.Context, org, collection, id string) (*fanclub.BaseRecord, error) {
	r, err := base.GetOrg(ctx, org, collection, id)
	if err != nil {
		return nil, err
	}
	return &fanclub.BaseRecord{Collection: r.Collection, ID: r.ID, Doc: r.Doc, Created: r.Created, Updated: r.Updated}, nil
}

func (fanclubBase) Create(ctx context.Context, org, collection string, doc json.RawMessage) (*fanclub.BaseRecord, error) {
	r, err := base.CreateOrg(ctx, org, collection, doc)
	if err != nil {
		return nil, err
	}
	return &fanclub.BaseRecord{Collection: r.Collection, ID: r.ID, Doc: r.Doc, Created: r.Created, Updated: r.Updated}, nil
}

func (fanclubBase) Update(ctx context.Context, org, collection, id string, doc json.RawMessage) (*fanclub.BaseRecord, error) {
	r, err := base.UpdateOrg(ctx, org, collection, id, doc)
	if err != nil {
		return nil, err
	}
	return &fanclub.BaseRecord{Collection: r.Collection, ID: r.ID, Doc: r.Doc, Created: r.Created, Updated: r.Updated}, nil
}

func fanclubRedirects(domain string) []string {
	if raw := strings.TrimSpace(os.Getenv("FANCLUB_OAUTH_REDIRECT_URIS")); raw != "" {
		var out []string
		for _, part := range strings.Split(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	scheme := "https://"
	host := domain
	if h, _, err := net.SplitHostPort(domain); err == nil {
		host = h
	}
	if host == "localhost" || net.ParseIP(host).IsLoopback() {
		scheme = "http://"
	}
	baseURL := scheme + domain
	return []string{
		baseURL + "/fanclub/index.html",
		baseURL + "/fanclub/",
	}
}

// fanclubApp adapts apps/fanclub to cloud.Deps: IAM TokenValidator, Cloud Base
// collections, Cloud Tasks durable queue, and Cloud AIClient with bearer relay.
func fanclubApp(app *zip.App, deps cloud.Deps) error {
	adminOrg := strings.TrimSpace(os.Getenv("IAM_ADMIN_ORG"))
	if adminOrg == "" {
		adminOrg = "admin"
	}
	validator := cloud.NewTokenValidator(deps.IAMIssuer)
	infConfigured := deps.AITransport != "" && deps.AI != nil

	return fanclub.Mount(app, fanclub.Deps{
		Log:                 deps.Logger.New("subsystem", "fanclub"),
		IAMIssuer:           deps.IAMIssuer,
		AdminOrg:            adminOrg,
		ClientID:            strings.TrimSpace(os.Getenv("FANCLUB_IAM_CLIENT_ID")),
		RedirectURIs:        fanclubRedirects(deps.Domain),
		DefaultModel:        deps.AIDefaultModel,
		InferenceConfigured: infConfigured,
		InferenceTransport:  deps.AITransport,
		ValidateToken: func(raw string) (fanclub.VerifiedIdentity, error) {
			v, err := validator.Validate(raw)
			if err != nil {
				return fanclub.VerifiedIdentity{}, err
			}
			return fanclub.VerifiedIdentity{
				Org:          v.Owner,
				User:         v.User,
				Username:     v.Username,
				Email:        v.Email,
				Issuer:       v.Issuer,
				IsOrgAdmin:   v.IsAdmin,
				IsSuperAdmin: v.Owner != "" && v.Owner == adminOrg,
			}, nil
		},
		Store: fanclubBase{},
		EnqueueTask: func(ctx context.Context, org, kind string, payload json.RawMessage) (string, error) {
			return tasks.EnqueueOrg(ctx, org, kind, payload)
		},
		Chat: func(ctx context.Context, req fanclub.ChatRequest) (fanclub.ChatResponse, error) {
			if deps.AI == nil {
				return fanclub.ChatResponse{}, errors.New("ai client unavailable")
			}
			callCtx := clients.WithAIBearer(ctx, cloud.BearerFrom(ctx))
			resp, err := deps.AI.ChatCompletion(callCtx, &cloud.ChatRequest{
				Model:      req.Model,
				Prompt:     req.Prompt,
				Org:        req.Org,
				BillingOrg: req.BillingOrg,
				Project:    req.Project,
			})
			if err != nil {
				return fanclub.ChatResponse{}, err
			}
			return fanclub.ChatResponse{
				Content:          resp.Content,
				Model:            req.Model,
				PromptTokens:     resp.PromptTokens,
				CompletionTokens: resp.CompletionTokens,
				TotalTokens:      resp.TotalTokens,
			}, nil
		},
	})
}

// opener opens {dataDir}/{name}.db through cek, so it is encrypted at rest under
// the deployment's key, and applies the app's schema in one transaction before
// handing the database over. One connection, because one writer owns the file.
func opener(dataDir, name string) func(context.Context, func(context.Context, *sql.Tx) error) (*sql.DB, error) {
	return func(ctx context.Context, migrate func(context.Context, *sql.Tx) error) (*sql.DB, error) {
		if dataDir == "" {
			return nil, errors.New("empty DataDir")
		}
		db, err := cek.Open(filepath.Join(dataDir, name+".db"))
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		if err := migrate(ctx, tx); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	}
}

// ctxShutdown adapts a subsystem's zero-arg Shutdown() error to the
// cloud.ShutdownFunc(ctx) signature. Several subsystems expose the simpler form
// (their teardown ignores the deadline); this bridges the impedance mismatch in
// ONE place so the Wire entries stay declarative — no inline closures.
func ctxShutdown(f func() error) cloud.ShutdownFunc {
	return func(context.Context) error { return f() }
}
