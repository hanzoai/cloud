package flags

// /v1/flags — the product flag API, org-scoped through the gateway principal
// (HIP-0026) and project-scoped through the principal's project. Evaluation is
// the embedded evaluator over the caller's own SQLite definitions; responses
// are PostHog-shaped so existing SDK consumers port 1:1.
//
// Every route that CAN be a typed op is one: zip.<Verb>(zapp, …) registers the
// route AND the registry entry the OpenAPI document, the MCP tool list and the
// CLI are all projected from. The one exception is PUT /v1/flags/defs/:key,
// whose body IS the flag definition — an open PostHog FlagDef object with no Go
// shape — so a typed In could only describe it by wrapping it, which would
// change the wire contract.

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

func routes(app cloud.Router, zapp *zip.App, s *cloud.Service[state]) {
	o := ops{s: s}
	// The request bridge FIRST: fiber runs middleware in registration order, so
	// one installed after these leaves would never run — and every org-scoped op
	// below resolves its tenant through it. Bounded to flags' own subtree; Serve
	// installs one app-wide too and nesting is harmless, which is what makes this
	// surface exercisable on a bare app.
	app.Group("/v1/flags").Use(cloud.Bridge())

	zip.Get(zapp, "/v1/flags/health", o.health)
	zip.Post(zapp, "/v1/flags", o.evaluate)
	zip.Post(zapp, "/v1/flags/decide", o.decide) // PostHog /decide alias
	zip.Get(zapp, "/v1/flags/defs", o.listDefs)
	zip.Get(zapp, "/v1/flags/defs/:key", o.getDef)
	app.Put("/v1/flags/defs/:key", cloud.Handle(s, putDef))
	zip.Delete(zapp, "/v1/flags/defs/:key", o.deleteDef)
	zip.Get(zapp, "/v1/flags/activity", o.listActivity)
}

// ops binds the service to flags' typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so
// it arrives as a RECEIVER and every op is a method value (o.listDefs). That is
// also the only bound form cmd/zipdoc can lift prose from: a closure returned by
// a helper is a call expression with no declaration to read. ops therefore
// carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// tenant resolves the org — the tenant-isolation KEY — from the validated
// principal, and the project scope (default project == the org store's root).
func tenant(c *zip.Ctx) (org, project string, ok bool) {
	org, ok = principal.Org(c)
	return org, principal.ProjectScope(c), ok
}

// scope is tenant for a typed op, which is handed a context and nothing else.
// It reads the SAME request the raw handlers read (cloud.Bridge parks it), so
// there is one tenancy rule and not two. Off the HTTP path — a CLI LocalInvoke —
// there is no request and no principal, so it refuses rather than defaulting to
// a tenant the caller never proved.
func scope(ctx context.Context) (org, project string, err error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", "", zip.ErrForbidden("X-Org-Id required")
	}
	org, project, ok = tenant(c)
	if !ok {
		return "", "", zip.ErrForbidden("X-Org-Id required")
	}
	return org, project, nil
}

func keyParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("key")) }

// ── health ─────────────────────────────────────────────────────────────────────

// flagHealth is the flag plane's liveness answer.
type flagHealth struct {
	// OK is true whenever this route answers.
	OK bool `json:"ok"`
	// Engine names the evaluator this binary resolves flags with.
	Engine string `json:"engine"`
}

// health reports that the flag plane is serving and names the evaluator behind
// it. It opens no store, so it answers whether or not the caller's definitions
// can be read.
//
// Response: {"ok": true, "engine": "hanzo-flags"}
func (o ops) health(ctx context.Context, _ *struct{}) (*flagHealth, error) {
	return &flagHealth{OK: true, Engine: "hanzo-flags"}, nil
}

// ── evaluation ─────────────────────────────────────────────────────────────────

// flagContext is one evaluation context: who the flags are being resolved for,
// and the properties the cohort filters match against.
type flagContext struct {
	// DistinctID is the identity the flags are evaluated for. Required.
	DistinctID string `json:"distinct_id" validate:"required"`
	// PersonProperties are the person-level properties the filters read, as an
	// arbitrary JSON object.
	PersonProperties json.RawMessage `json:"person_properties"`
	// Groups are the group-level contexts keyed by group-type index, each
	// {"key": ..., "properties": {...}}.
	Groups json.RawMessage `json:"groups"`
}

// flagVerdicts is the PostHog-shaped evaluation result: every flag's value for
// this identity, the payload attached to each, and whether any flag failed.
type flagVerdicts struct {
	// FeatureFlags maps flag key to its value — true, false, or a variant name.
	FeatureFlags map[string]json.RawMessage `json:"featureFlags"`
	// FeatureFlagPayloads maps flag key to the payload attached to the value it
	// resolved to.
	FeatureFlagPayloads map[string]json.RawMessage `json:"featureFlagPayloads"`
	// ErrorsWhileComputingFlags is true when a definition could not be evaluated.
	ErrorsWhileComputingFlags bool `json:"errorsWhileComputingFlags"`
}

// evaluate resolves every one of the caller's flags for one identity and returns
// each flag's value plus its payload. Evaluation is in-process over the org's own
// stored definitions — no flag is fetched and nothing is recorded.
//
// Example: {"distinct_id": "u_1", "person_properties": {"plan": "pro"}}
// Response: {"featureFlags": {"checkout-exp": "treatment"}, "featureFlagPayloads": {"checkout-exp": {"cta": "buy"}}, "errorsWhileComputingFlags": false}
func (o ops) evaluate(ctx context.Context, in *flagContext) (*flagVerdicts, error) {
	org, project, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.DistinctID) == "" {
		return nil, zip.ErrBadRequest("distinct_id is required")
	}
	evalCtx := map[string]json.RawMessage{
		"distinct_id": json.RawMessage(strconv.Quote(in.DistinctID)),
	}
	if len(in.PersonProperties) > 0 {
		evalCtx["person_properties"] = in.PersonProperties
	}
	if len(in.Groups) > 0 {
		evalCtx["groups"] = in.Groups
	}
	ctxJSON, err := json.Marshal(evalCtx)
	if err != nil {
		return nil, err
	}
	res, err := o.s.State.client.evaluateProject(org, project, ctxJSON)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "evaluate: %v", err)
	}
	var out flagVerdicts
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "evaluate: %v", err)
	}
	return &out, nil
}

// decide is the PostHog /decide spelling of evaluate. Same input, same answer —
// it exists so an SDK pointed at PostHog's path keeps working unchanged.
//
// Example: {"distinct_id": "u_1", "person_properties": {"plan": "pro"}}
func (o ops) decide(ctx context.Context, in *flagContext) (*flagVerdicts, error) {
	return o.evaluate(ctx, in)
}

// ── definitions ────────────────────────────────────────────────────────────────

// flagRef addresses one flag definition by its key.
type flagRef struct {
	// Key is the flag key from the path.
	Key string `json:"key" validate:"required"`
}

// flagDefs is a page of stored flag definitions.
type flagDefs struct {
	// Data is every definition in the caller's project, ordered by key.
	Data []DefRow `json:"data"`
}

// flagDeleted names the definition a delete removed.
type flagDeleted struct {
	// Deleted is the key that no longer has a definition.
	Deleted string `json:"deleted"`
}

// listDefs returns every flag definition stored for the caller's org and
// project, each with its version and who last changed it.
//
// Response: {"data": [{"key": "checkout-exp", "definition": {"active": true}, "version": 2, "updated_at": "2026-01-01T00:00:00Z", "updated_by": "z@hanzo.ai"}]}
func (o ops) listDefs(ctx context.Context, _ *struct{}) (*flagDefs, error) {
	org, project, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.client.stores.For(org, project)
	if err != nil {
		return nil, err
	}
	rows, err := st.List()
	if err != nil {
		return nil, err
	}
	return &flagDefs{Data: rows}, nil
}

// getDef returns one flag's stored definition, its version and who last changed
// it. A key with no definition is a 404, not an empty flag.
//
// Example: {"key": "checkout-exp"}
func (o ops) getDef(ctx context.Context, in *flagRef) (*DefRow, error) {
	org, project, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return nil, zip.ErrBadRequest("key is required")
	}
	st, err := o.s.State.client.stores.For(org, project)
	if err != nil {
		return nil, err
	}
	row, found, err := st.Get(key)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, zip.ErrNotFound("flag not found")
	}
	return &row, nil
}

// putDef stores one flag definition under the key in the path. The BODY IS THE
// DEFINITION — arbitrary PostHog FlagDef JSON, whose own "key" is forced to
// match the path — so this route stays raw: a typed In would have to wrap the
// definition in a field and every existing caller would break.
func putDef(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	key := keyParam(c)
	if key == "" {
		return zip.ErrBadRequest("key is required")
	}
	body := c.Body()
	if len(body) == 0 || !json.Valid(body) {
		return zip.ErrBadRequest("body must be the flag definition JSON")
	}
	st, err := s.State.client.stores.For(org, project)
	if err != nil {
		return err
	}
	if err := st.Upsert(key, json.RawMessage(body), c.UserEmail()); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	row, _, err := st.Get(key)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, row)
}

// deleteDef removes one flag definition. The flag stops being evaluated; nothing
// that already read it is rolled back, and the removal is recorded in the
// activity log.
//
// Example: {"key": "checkout-exp"}
// Response: {"deleted": "checkout-exp"}
func (o ops) deleteDef(ctx context.Context, in *flagRef) (*flagDeleted, error) {
	org, project, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return nil, zip.ErrBadRequest("key is required")
	}
	st, err := o.s.State.client.stores.For(org, project)
	if err != nil {
		return nil, err
	}
	deleted, err := st.Delete(key, actor(ctx))
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, zip.ErrNotFound("flag not found")
	}
	return &flagDeleted{Deleted: key}, nil
}

// ── activity ───────────────────────────────────────────────────────────────────

// flagActivityQuery bounds a read of the change log.
type flagActivityQuery struct {
	// Limit caps the entries returned; 0 or out of range means 100, and nothing
	// above 500 is honoured.
	Limit int `json:"limit"`
}

// flagActivity is a page of definition changes, newest first.
type flagActivity struct {
	// Data is the change entries, newest first.
	Data []ActivityRow `json:"data"`
}

// listActivity returns the definition change log for the caller's org and
// project — who created, updated or deleted which flag, and when.
//
// Example: {"limit": 50}
// Response: {"data": [{"id": 3, "key": "checkout-exp", "action": "deleted", "actor": "z@hanzo.ai", "at": "2026-01-01T00:00:00Z"}]}
func (o ops) listActivity(ctx context.Context, in *flagActivityQuery) (*flagActivity, error) {
	org, project, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	st, err := o.s.State.client.stores.For(org, project)
	if err != nil {
		return nil, err
	}
	rows, err := st.Activity(in.Limit)
	if err != nil {
		return nil, err
	}
	return &flagActivity{Data: rows}, nil
}

// actor is the email a change is attributed to. It comes from the same validated
// request the tenant does; off the HTTP path there is no signed-in person and the
// log records an empty actor rather than inventing one.
func actor(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	return c.UserEmail()
}
