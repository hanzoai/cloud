package cloudflare

// workers.go — Cloudflare Workers, WIRED: script put/list/delete, the account
// workers.dev subdomain + per-script enable, and zone route bind/list/delete. Reads
// use authClient (validated org); mutations use authWrite (validated org + org admin).
// Script upload is the modern multipart module format (a metadata part + the module
// part). Scripts + subdomain are account-scoped (/accounts/{id}/workers/*); routes are
// zone-scoped (/zones/{zone_id}/workers/routes).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/zap-proto/zip"
)

// WorkerScriptPut is the upload request for a Workers module script. Script is the
// ES-module source; MainModule names the entry file (default "worker.js").
// CompatibilityDate/Flags and Bindings ride the multipart metadata part. It is the
// struct the handler binds AND what the document declares for the route
// (openapi.Register, cloudflare.go), so the published contract follows the code.
type WorkerScriptPut struct {
	// Script is the ES-module SOURCE — the code itself, not a name or a URL. It
	// shares a name with the `:script` path segment, which names the Worker; those
	// are two different things, and keeping them apart is why this route cannot be a
	// typed op.
	Script string `json:"script"`
	// MainModule is the module file the runtime starts at. Empty means "worker.js".
	MainModule string `json:"mainModule,omitempty"`
	// CompatibilityDate pins which Workers runtime behaviour the script runs under,
	// as a date ("2024-01-01").
	CompatibilityDate string `json:"compatibilityDate,omitempty"`
	// CompatibilityFlags turn individual runtime behaviours on or off around that
	// date ("nodejs_compat").
	CompatibilityFlags []string `json:"compatibilityFlags,omitempty"`
	// Bindings are the resources the script can reach (KV, D1, R2, secrets, …), in
	// Cloudflare's own binding vocabulary. Passed through as written: this plane
	// deliberately does not model Cloudflare's shapes.
	Bindings json.RawMessage `json:"bindings,omitempty"`
}

// ── scripts ─────────────────────────────────────────────────────────────────────

// WorkersScriptList lists the Worker scripts on the org's Cloudflare account. Any
// org member may read.
func (o ops) workersScriptList(ctx context.Context, _ *noInput) (*cfResult, error) {
	cl, acct, err := o.acctClient(ctx)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodGet, "/accounts/"+acct+"/workers/scripts", nil)
}

// workersScriptPut uploads (or replaces) a module Worker script. Requires org admin.
//
// NOT a typed op: the path names the script (`:script`) and the body field `script`
// carries the module SOURCE. zip's URL binder matches a path param to the In field
// of the same name and gives the URL the last word, so a typed In would overwrite
// the source with the script name. Renaming either side would move the wire.
func (o ops) workersScriptPut(c *zip.Ctx) error {
	ctx := c.Context()
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return err
	}
	name, err := pathSeg(c, "script", nameRE)
	if err != nil {
		return err
	}
	var in WorkerScriptPut
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	if strings.TrimSpace(in.Script) == "" {
		return zip.ErrBadRequest("script source is required")
	}
	body, contentType, err := buildWorkerUpload(in)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	var out json.RawMessage
	if err := cl.cfUpload(ctx, http.MethodPut, "/accounts/"+acct+"/workers/scripts/"+name, contentType, body, &out); err != nil {
		return cfErr(err)
	}
	return writeResult(c, out)
}

// scriptRef addresses one Worker script by name, from the path.
type scriptRef struct {
	// Script is the Worker script name.
	Script string `json:"script"`
}

// WorkersScriptDelete removes a Worker script from the org's Cloudflare account.
// Requires org admin. Routes bound to the script stop serving it.
//
// Example: {"script": "edge-router"}
func (o ops) workersScriptDelete(ctx context.Context, in *scriptRef) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	name, err := seg("script", in.Script, nameRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodDelete, "/accounts/"+acct+"/workers/scripts/"+name, nil)
}

// buildWorkerUpload builds the Cloudflare multipart/form-data body for a module
// Worker upload: a metadata JSON part (main_module + optional compatibility settings
// and bindings) plus the module source part, whose form field name MUST equal
// main_module so Cloudflare links them. Returns the body + its multipart content type.
func buildWorkerUpload(in WorkerScriptPut) ([]byte, string, error) {
	main := strings.TrimSpace(in.MainModule)
	if main == "" {
		main = "worker.js"
	}
	if !nameRE.MatchString(main) {
		return nil, "", fmt.Errorf("mainModule is invalid")
	}
	meta := map[string]any{"main_module": main}
	if in.CompatibilityDate != "" {
		meta["compatibility_date"] = in.CompatibilityDate
	}
	if len(in.CompatibilityFlags) > 0 {
		meta["compatibility_flags"] = in.CompatibilityFlags
	}
	if len(in.Bindings) > 0 {
		meta["bindings"] = in.Bindings
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	mh := make(textproto.MIMEHeader)
	mh.Set("Content-Disposition", `form-data; name="metadata"`)
	mh.Set("Content-Type", "application/json")
	mp, err := mw.CreatePart(mh)
	if err != nil {
		return nil, "", err
	}
	if _, err := mp.Write(metaJSON); err != nil {
		return nil, "", err
	}

	sh := make(textproto.MIMEHeader)
	sh.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, main, main))
	sh.Set("Content-Type", "application/javascript+module")
	sp, err := mw.CreatePart(sh)
	if err != nil {
		return nil, "", err
	}
	if _, err := sp.Write([]byte(in.Script)); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

// ── workers.dev subdomain ───────────────────────────────────────────────────────

// WorkersSubdomainGet reads the org account's workers.dev subdomain — the name
// under which every subdomain-enabled script is served. Any org member may read.
func (o ops) workersSubdomainGet(ctx context.Context, _ *noInput) (*cfResult, error) {
	cl, acct, err := o.acctClient(ctx)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodGet, "/accounts/"+acct+"/workers/subdomain", nil)
}

// subdomainSetIn toggles one script on the account workers.dev subdomain.
type subdomainSetIn struct {
	// Script is the Worker script name, from the path.
	Script string `json:"script"`
	// Enabled publishes the script on <script>.<subdomain>.workers.dev when true,
	// and withdraws it when false.
	Enabled bool `json:"enabled"`
}

// WorkersScriptSubdomainSet publishes or withdraws one Worker script on the
// account's workers.dev subdomain. Requires org admin.
//
// Example: {"script": "edge-router", "enabled": true}
func (o ops) workersScriptSubdomainSet(ctx context.Context, in *subdomainSetIn) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	name, err := seg("script", in.Script, nameRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodPost, "/accounts/"+acct+"/workers/scripts/"+name+"/subdomain",
		map[string]bool{"enabled": in.Enabled})
}

// ── zone routes ─────────────────────────────────────────────────────────────────

// WorkersRouteList lists the Worker routes bound within one zone — the URL
// patterns that dispatch to a script. Any org member may read. Routes are
// zone-scoped, so no account is resolved.
//
// Example: {"zone": "0123456789abcdef0123456789abcdef"}
func (o ops) workersRouteList(ctx context.Context, in *zoneRef) (*cfResult, error) {
	cl, _, err := o.authClient(ctx)
	if err != nil {
		return nil, err
	}
	zone, err := seg("zone", in.Zone, idRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodGet, "/zones/"+zone+"/workers/routes", nil)
}

// routeCreateIn binds a Worker script to a URL pattern within a zone.
type routeCreateIn struct {
	// Zone is the 32-hex Cloudflare zone id, from the path.
	Zone string `json:"zone"`
	// Pattern is the URL pattern to bind, e.g. "acme.com/api/*".
	Pattern string `json:"pattern"`
	// Script is the Worker script to dispatch to. Omit it to leave the pattern
	// bound to no script, which is how Cloudflare expresses "bypass the Worker here".
	Script string `json:"script"`
}

// WorkersRouteCreate binds a URL pattern in a zone to a Worker script. Requires
// org admin — a route is what puts a script in front of live traffic.
//
// Example: {"zone": "0123456789abcdef0123456789abcdef", "pattern": "acme.com/api/*", "script": "edge-router"}
func (o ops) workersRouteCreate(ctx context.Context, in *routeCreateIn) (*cfResult, error) {
	cl, _, err := o.authWrite(ctx)
	if err != nil {
		return nil, err
	}
	zone, err := seg("zone", in.Zone, idRE)
	if err != nil {
		return nil, err
	}
	pattern := strings.TrimSpace(in.Pattern)
	if pattern == "" {
		return nil, zip.ErrBadRequest("route pattern is required")
	}
	body := map[string]string{"pattern": pattern}
	if sc := strings.TrimSpace(in.Script); sc != "" {
		body["script"] = sc
	}
	return cl.relay(ctx, http.MethodPost, "/zones/"+zone+"/workers/routes", body)
}

// routeRef addresses one Worker route within one zone, both from the path.
type routeRef struct {
	// Zone is the 32-hex Cloudflare zone id.
	Zone string `json:"zone"`
	// Route is the 32-hex Cloudflare route id.
	Route string `json:"route"`
}

// WorkersRouteDelete unbinds a Worker route, so its pattern stops dispatching to a
// script. Requires org admin.
//
// Example: {"zone": "0123456789abcdef0123456789abcdef", "route": "fedcba9876543210fedcba9876543210"}
func (o ops) workersRouteDelete(ctx context.Context, in *routeRef) (*cfResult, error) {
	cl, _, err := o.authWrite(ctx)
	if err != nil {
		return nil, err
	}
	zone, err := seg("zone", in.Zone, idRE)
	if err != nil {
		return nil, err
	}
	route, err := seg("route", in.Route, idRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodDelete, "/zones/"+zone+"/workers/routes/"+route, nil)
}
