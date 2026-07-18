package cloudflare

// workers.go — Cloudflare Workers, WIRED: script put/list/delete, the account
// workers.dev subdomain + per-script enable, and zone route bind/list/delete. Reads
// use authClient (validated org); mutations use authWrite (validated org + org admin).
// Script upload is the modern multipart module format (a metadata part + the module
// part). Scripts + subdomain are account-scoped (/accounts/{id}/workers/*); routes are
// zone-scoped (/zones/{zone_id}/workers/routes).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// WorkerScriptPut is the upload request for a Workers module script. Script is the
// ES-module source; MainModule names the entry file (default "worker.js").
// CompatibilityDate/Flags and Bindings ride the multipart metadata part.
type WorkerScriptPut struct {
	Script             string          `json:"script"`
	MainModule         string          `json:"mainModule,omitempty"`
	CompatibilityDate  string          `json:"compatibilityDate,omitempty"`
	CompatibilityFlags []string        `json:"compatibilityFlags,omitempty"`
	Bindings           json.RawMessage `json:"bindings,omitempty"`
}

// WorkerRouteCreate binds a Worker script to a URL pattern within a zone.
type WorkerRouteCreate struct {
	Pattern string `json:"pattern"`
	Script  string `json:"script,omitempty"`
}

// ── scripts ─────────────────────────────────────────────────────────────────────

func workersScriptList(s *cloud.Service[state], c *zip.Ctx) error {
	cl, org, err := authClient(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), org, c)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, "/accounts/"+acct+"/workers/scripts", nil)
}

func workersScriptPut(s *cloud.Service[state], c *zip.Ctx) error {
	cl, org, err := authWrite(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), org, c)
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
	if err := cl.cfUpload(c.Context(), http.MethodPut, "/accounts/"+acct+"/workers/scripts/"+name, contentType, body, &out); err != nil {
		return cfErr(err)
	}
	return writeResult(c, out)
}

func workersScriptDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, org, err := authWrite(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), org, c)
	if err != nil {
		return err
	}
	name, err := pathSeg(c, "script", nameRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodDelete, "/accounts/"+acct+"/workers/scripts/"+name, nil)
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

func workersSubdomainGet(s *cloud.Service[state], c *zip.Ctx) error {
	cl, org, err := authClient(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), org, c)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, "/accounts/"+acct+"/workers/subdomain", nil)
}

// workersScriptSubdomainSet enables/disables a script on the account workers.dev
// subdomain (POST .../scripts/{script}/subdomain {enabled}).
func workersScriptSubdomainSet(s *cloud.Service[state], c *zip.Ctx) error {
	cl, org, err := authWrite(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), org, c)
	if err != nil {
		return err
	}
	name, err := pathSeg(c, "script", nameRE)
	if err != nil {
		return err
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	return cl.pass(c, http.MethodPost, "/accounts/"+acct+"/workers/scripts/"+name+"/subdomain", map[string]bool{"enabled": in.Enabled})
}

// ── zone routes ─────────────────────────────────────────────────────────────────

func workersRouteList(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	zone, err := pathSeg(c, "zone", idRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, "/zones/"+zone+"/workers/routes", nil)
}

func workersRouteCreate(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authWrite(s, c)
	if err != nil {
		return err
	}
	zone, err := pathSeg(c, "zone", idRE)
	if err != nil {
		return err
	}
	var in WorkerRouteCreate
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	pattern := strings.TrimSpace(in.Pattern)
	if pattern == "" {
		return zip.ErrBadRequest("route pattern is required")
	}
	body := map[string]string{"pattern": pattern}
	if sc := strings.TrimSpace(in.Script); sc != "" {
		body["script"] = sc
	}
	return cl.pass(c, http.MethodPost, "/zones/"+zone+"/workers/routes", body)
}

func workersRouteDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authWrite(s, c)
	if err != nil {
		return err
	}
	zone, err := pathSeg(c, "zone", idRE)
	if err != nil {
		return err
	}
	route, err := pathSeg(c, "route", idRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodDelete, "/zones/"+zone+"/workers/routes/"+route, nil)
}
