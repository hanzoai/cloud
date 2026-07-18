package cloudflare

// kv.go — Cloudflare Workers KV (account-scoped: /accounts/{id}/storage/kv/
// namespaces). PHASE-2 STUB: routes + typed provider methods exist so the surface is
// complete and typed; the bodies ship in Phase 2, and each handler answers an honest
// 501 (via stubResult) after the authClient gate — never a fake success.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// KVNamespace is the Phase-2 KV namespace shape.
type KVNamespace struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// listKVNamespaces / createKVNamespace / deleteKVNamespace are the typed Phase-2
// provider seams; the wired bodies land in Phase 2.
func (cl *client) listKVNamespaces(context.Context, string) ([]KVNamespace, error) {
	return nil, errPhase2
}
func (cl *client) createKVNamespace(context.Context, string, string) (*KVNamespace, error) {
	return nil, errPhase2
}
func (cl *client) deleteKVNamespace(context.Context, string, string) error {
	return errPhase2
}

func kvNamespaceList(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	_, err = cl.listKVNamespaces(c.Context(), strings.TrimSpace(c.Query("account")))
	return stubResult(c, "kv", err)
}

func kvNamespaceCreate(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authWrite(s, c)
	if err != nil {
		return err
	}
	var in struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(c.Body(), &in)
	_, err = cl.createKVNamespace(c.Context(), strings.TrimSpace(c.Query("account")), strings.TrimSpace(in.Title))
	return stubResult(c, "kv", err)
}

func kvNamespaceDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authWrite(s, c)
	if err != nil {
		return err
	}
	err = cl.deleteKVNamespace(c.Context(), strings.TrimSpace(c.Query("account")), strings.TrimSpace(c.Param("namespace")))
	return stubResult(c, "kv", err)
}
