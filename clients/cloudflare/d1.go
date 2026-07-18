package cloudflare

// d1.go — Cloudflare D1 (account-scoped: /accounts/{id}/d1/database). PHASE-2 STUB:
// routes + typed provider methods exist so the surface is complete and typed; the
// bodies ship in Phase 2, and each handler answers an honest 501 (via stubResult)
// after the authClient gate — never a fake success.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// D1Database is the Phase-2 D1 database shape.
type D1Database struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// listD1Databases / createD1Database / deleteD1Database are the typed Phase-2
// provider seams; the wired bodies land in Phase 2.
func (cl *client) listD1Databases(context.Context, string) ([]D1Database, error) {
	return nil, errPhase2
}
func (cl *client) createD1Database(context.Context, string, string) (*D1Database, error) {
	return nil, errPhase2
}
func (cl *client) deleteD1Database(context.Context, string, string) error {
	return errPhase2
}

func d1DatabaseList(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	_, err = cl.listD1Databases(c.Context(), strings.TrimSpace(c.Query("account")))
	return stubResult(c, "d1", err)
}

func d1DatabaseCreate(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authWrite(s, c)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(c.Body(), &in)
	_, err = cl.createD1Database(c.Context(), strings.TrimSpace(c.Query("account")), strings.TrimSpace(in.Name))
	return stubResult(c, "d1", err)
}

func d1DatabaseDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authWrite(s, c)
	if err != nil {
		return err
	}
	err = cl.deleteD1Database(c.Context(), strings.TrimSpace(c.Query("account")), strings.TrimSpace(c.Param("database")))
	return stubResult(c, "d1", err)
}
