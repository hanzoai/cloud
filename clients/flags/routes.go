package flags

// /v1/flags — the product flag API, org-scoped through the gateway principal
// (HIP-0026) and project-scoped through the principal's project. Evaluation is
// the embedded native engine over the caller's own SQLite definitions; responses
// are PostHog-shaped so existing SDK consumers port 1:1.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/flags/health", cloud.Handle(s, health))
	app.Post("/v1/flags", cloud.Handle(s, evaluateFlags))
	app.Post("/v1/flags/decide", cloud.Handle(s, evaluateFlags)) // PostHog /decide alias
	app.Get("/v1/flags/defs", cloud.Handle(s, listDefs))
	app.Get("/v1/flags/defs/:key", cloud.Handle(s, getDef))
	app.Put("/v1/flags/defs/:key", cloud.Handle(s, putDef))
	app.Delete("/v1/flags/defs/:key", cloud.Handle(s, deleteDef))
	app.Get("/v1/flags/activity", cloud.Handle(s, listActivity))
}

// tenant resolves the org — the tenant-isolation KEY — from the validated
// principal, and the project scope (default project == the org store's root).
func tenant(c *zip.Ctx) (org, project string, ok bool) {
	org, ok = principal.Org(c)
	return org, principal.ProjectScope(c), ok
}

func keyParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("key")) }

func health(s *cloud.Service[state], c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{
		"ok":     true,
		"engine": "hanzo-flags",
		"native": engineAvailable,
	})
}

// evaluateFlags runs the caller's flags for one identity. Body:
//
//	{"distinct_id": "u1", "person_properties": {...}, "groups": {"0": {"key": "acme", "properties": {...}}}}
//
// Response: {"featureFlags": {...}, "featureFlagPayloads": {...}, "errorsWhileComputingFlags": bool}
func evaluateFlags(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if !engineAvailable {
		return zip.Errorf(http.StatusServiceUnavailable, "native flags engine not built")
	}
	var body struct {
		DistinctID       string          `json:"distinct_id"`
		PersonProperties json.RawMessage `json:"person_properties"`
		Groups           json.RawMessage `json:"groups"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	if strings.TrimSpace(body.DistinctID) == "" {
		return zip.ErrBadRequest("distinct_id is required")
	}
	ctx := map[string]json.RawMessage{
		"distinct_id": json.RawMessage(strconv.Quote(body.DistinctID)),
	}
	if len(body.PersonProperties) > 0 {
		ctx["person_properties"] = body.PersonProperties
	}
	if len(body.Groups) > 0 {
		ctx["groups"] = body.Groups
	}
	ctxJSON, err := json.Marshal(ctx)
	if err != nil {
		return err
	}
	res, err := s.State.client.evaluateProject(org, project, ctxJSON)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "evaluate: %v", err)
	}
	return c.JSON(http.StatusOK, json.RawMessage(res))
}

func listDefs(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	st, err := s.State.client.stores.For(org, project)
	if err != nil {
		return err
	}
	rows, err := st.List()
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func getDef(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	key := keyParam(c)
	if key == "" {
		return zip.ErrBadRequest("key is required")
	}
	st, err := s.State.client.stores.For(org, project)
	if err != nil {
		return err
	}
	row, found, err := st.Get(key)
	if err != nil {
		return err
	}
	if !found {
		return zip.ErrNotFound("flag not found")
	}
	return c.JSON(http.StatusOK, row)
}

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

func deleteDef(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	key := keyParam(c)
	if key == "" {
		return zip.ErrBadRequest("key is required")
	}
	st, err := s.State.client.stores.For(org, project)
	if err != nil {
		return err
	}
	deleted, err := st.Delete(key, c.UserEmail())
	if err != nil {
		return err
	}
	if !deleted {
		return zip.ErrNotFound("flag not found")
	}
	return c.JSON(http.StatusOK, map[string]any{"deleted": key})
}

func listActivity(s *cloud.Service[state], c *zip.Ctx) error {
	org, project, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	st, err := s.State.client.stores.For(org, project)
	if err != nil {
		return err
	}
	rows, err := st.Activity(limit)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}
