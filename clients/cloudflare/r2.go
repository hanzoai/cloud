package cloudflare

// r2.go — Cloudflare R2 (account-scoped: /accounts/{id}/r2/buckets). PHASE-2 STUB:
// the routes and the typed provider methods exist so the surface is complete and
// typed, but the bodies ship in Phase 2 — each handler answers an honest 501 (via
// stubResult), never a fake success. Every handler still runs the authClient gate,
// so an R2 route is no softer a target than a wired one.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// R2Bucket is the Phase-2 R2 bucket shape.
type R2Bucket struct {
	Name         string `json:"name"`
	CreationDate string `json:"creation_date,omitempty"`
	Location     string `json:"location,omitempty"`
}

// listR2Buckets / createR2Bucket / deleteR2Bucket are the typed Phase-2 provider
// seams; the wired bodies land in Phase 2.
func (cl *client) listR2Buckets(context.Context, string) ([]R2Bucket, error) {
	return nil, errPhase2
}
func (cl *client) createR2Bucket(context.Context, string, string) (*R2Bucket, error) {
	return nil, errPhase2
}
func (cl *client) deleteR2Bucket(context.Context, string, string) error {
	return errPhase2
}

func r2BucketList(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	_, err = cl.listR2Buckets(c.Context(), strings.TrimSpace(c.Query("account")))
	return stubResult(c, "r2", err)
}

func r2BucketCreate(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(c.Body(), &in)
	_, err = cl.createR2Bucket(c.Context(), strings.TrimSpace(c.Query("account")), strings.TrimSpace(in.Name))
	return stubResult(c, "r2", err)
}

func r2BucketDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	err = cl.deleteR2Bucket(c.Context(), strings.TrimSpace(c.Query("account")), strings.TrimSpace(c.Param("bucket")))
	return stubResult(c, "r2", err)
}
