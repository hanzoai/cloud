// Package agent mounts the hanzoai/agent orchestrator into cloud: POST /v1/agent
// (+ /v1/agent/presets, /v1/agent/conversations). The orchestrator logic and its
// per-org conversation history live in github.com/hanzoai/agent, which imports
// NEITHER cloud NOR ai. Cloud is the composition root: it injects the two seams —
//   - Completer: the ai subsystem's /v1/chat/completions, replayed in-process (the
//     one path that returns tool_calls AND carries per-org reserve/settle billing);
//   - ToolPlane: the unified tool registry (tools.Default()), so /v1/agent's
//     server-executed tools are the org's activated MCP/registry tools.
// /v1/agent is a DISTINCT path (not /v1/chat, which ai owns as completions), so a
// specific route wins over ai's /v1/* glob — no collision.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	hz "github.com/hanzoai/agent"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/tools"
	openai "github.com/hanzoai/go-openai"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// maxCompletionResponse bounds the in-process completion body read so a hostile or
// broken upstream cannot balloon memory.
const maxCompletionResponse = 8 << 20

// Mount wires POST /v1/agent (+ reads) into cloud, injecting the ai completion and
// the tool plane. The caller identity comes from cloud's validated principal.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("agent.Mount: nil zip.App")
	}
	_, err := hz.Mount(app, hz.Deps{
		Logger:  deps.Logger,
		DataDir: deps.DataDir,
		Brand:   deps.Brand,
		Model:   deps.AIDefaultModel,
		Principal: func(c *zip.Ctx) (hz.Principal, bool) {
			p, ok := tools.PrincipalFrom(c)
			if !ok {
				return hz.Principal{}, false
			}
			return hz.Principal{Org: p.Org, Project: p.Project, User: p.User, Cred: credential(c)}, true
		},
	}, aiCompleter{app: app}, toolPlane{})
	return err
}

// ── Completer: replay /v1/chat/completions in-process ─────────────────────────────

type aiCompleter struct{ app *zip.App }

// Complete replays the request against the SAME app at /v1/chat/completions, so it
// flows the whole middleware chain (per-org reserve/settle billing) and returns
// tool_calls. Non-streaming. Mirrors the tool plane's in-process dispatch contract:
// the caller's OWN credential headers are replayed; no minted authority header.
func (a aiCompleter) Complete(ctx context.Context, cred map[string]string, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	req.Stream = false
	b, err := json.Marshal(req)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	hreq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b)).WithContext(ctx)
	hreq.Header.Set("Content-Type", "application/json")
	for k, v := range cred {
		hreq.Header.Set(k, v)
	}
	resp, err := a.app.Fiber().Test(hreq, fiber.TestConfig{Timeout: 0})
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxCompletionResponse))
	if resp.StatusCode/100 != 2 {
		// Carry the completion's OWN status + body so the round can pass a
		// caller-facing refusal (402 insufficient_balance, 429, 403) straight
		// through instead of masking it as a gateway 502. hz.UpstreamError is the
		// agent's typed seam for exactly this.
		return openai.ChatCompletionResponse{}, &hz.UpstreamError{Status: resp.StatusCode, Body: raw}
	}
	var out openai.ChatCompletionResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return openai.ChatCompletionResponse{}, fmt.Errorf("decode completion: %w", err)
	}
	return out, nil
}

// ── ToolPlane: adapter over the unified registry ──────────────────────────────────

type toolPlane struct{}

func (toolPlane) List(ctx context.Context, scope hz.Scope) []hz.Tool {
	ts := tools.Default().List(ctx, tools.Scope{Org: scope.Org, Project: scope.Project})
	out := make([]hz.Tool, 0, len(ts))
	for _, t := range ts {
		out = append(out, hz.Tool{
			Name:         t.Name,
			Description:  t.Description,
			Schema:       t.Schema,
			Activated:    t.Activated,
			Dispatchable: t.Dispatchable,
		})
	}
	return out
}

func (toolPlane) Exists(ctx context.Context, scope hz.Scope, name string) bool {
	return tools.Default().Exists(ctx, tools.Scope{Org: scope.Org, Project: scope.Project}, name)
}

// Dispatch resolves the caller the ONE canonical way — tools.PrincipalFrom(c) — so
// the tool runs under the SAME validated identity + credential as a direct call.
// No reconstruction: the credential is only ever read from the live request.
func (toolPlane) Dispatch(c *zip.Ctx, name string, args map[string]any) (any, error) {
	p, ok := tools.PrincipalFrom(c)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	return tools.Default().Dispatch(c.Context(), p, name, args)
}

// ── helpers ───────────────────────────────────────────────────────────────────────

// credential extracts the caller's replayable credential headers (the same set the
// tool plane replays) so the in-process completion runs as the caller.
func credential(c *zip.Ctx) map[string]string {
	cred := map[string]string{}
	for _, h := range []string{"Authorization", "X-Authorization", "Cookie", "Accept-Language", "X-Forwarded-For"} {
		if v := c.Header(h); v != "" {
			cred[h] = v
		}
	}
	return cred
}
