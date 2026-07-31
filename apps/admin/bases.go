package admin

// The BASES panel (/v1/admin/bases) — the tenant Base-instance surface, scoped by the ONE
// tenant predicate: a SuperAdmin sees EVERY tenant's Base instance; any other admin caller
// sees ONLY their own subtree's. "Base" is Hanzo's multi-tenant app engine (hanzoai/base —
// a per-tenant DB store); an instance is one tenant's Base.
//
// SEAM (honest gap). The Base engine is being EMBEDDED into cloud (a /v1/base subsystem);
// until it lands, this panel proxies a server-authed Base admin surface at BASE_ADMIN_URL
// (secret from KMS via BASE_ADMIN_TOKEN — never a client claim, the SAME pattern
// waitlist.go uses) and returns the HONEST empty state when unconfigured — never
// fabricated instances. When /v1/base is embedded, point BASE_ADMIN_URL at the in-process
// handler; the scope filter below is unchanged.
//
// SCOPE SAFETY (defense in depth). A non-super caller's read is filtered to their subtree
// in TWO places: the upstream is asked for their org (?org=), AND every returned row is
// re-checked against the scope here — so a mis-filtering or unparseable upstream can NEVER
// leak another tenant's instance to a scoped caller (it degrades to empty, not to raw
// passthrough).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/admin/core"
)

const (
	baseAdminURLEnv   = "BASE_ADMIN_URL"
	baseAdminTokenEnv = "BASE_ADMIN_TOKEN"
)

var baseHTTP = &http.Client{Timeout: 15 * time.Second}

// baseInstance is one tenant's Base instance as the cockpit renders it. `Org` is the
// tenant slug the scope filter keys on — it MUST be present for a row to be visible to a
// scoped (non-super) caller.
type baseInstance struct {
	Name    string `json:"name"`
	Org     string `json:"org"`
	URL     string `json:"url"`
	Status  string `json:"status"`
	Plan    string `json:"plan"`
	Region  string `json:"region"`
	Created string `json:"created"`
}

func baseAdminConfig() (base, token string, ok bool) {
	base = strings.TrimRight(strings.TrimSpace(os.Getenv(baseAdminURLEnv)), "/")
	token = strings.TrimSpace(os.Getenv(baseAdminTokenEnv))
	return base, token, base != ""
}

// baseProxy issues a server-authed GET to the Base admin surface and returns its raw JSON
// body + status. Bounded read; Bearer token only when configured; never forwards a client
// header.
func baseProxy(ctx context.Context, target, token string) (json.RawMessage, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("base request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := baseHTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("could not reach the Base engine: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("base read: %w", err)
	}
	return json.RawMessage(raw), resp.StatusCode, nil
}

// bases lists the tenant Base instances in the caller's window — a SuperAdmin sees every
// tenant's, anyone else only their own subtree's.
//
// The scope is enforced TWICE: the upstream is asked for the caller's org, AND every row
// it returns is re-checked against the resolved scope. An upstream that ignored the
// filter therefore degrades to empty, never to a cross-tenant leak.
//
// The Base engine is being embedded into cloud; until it lands this proxies
// BASE_ADMIN_URL and, when that is unset, answers 200 with an empty list and msg saying
// so — the honest not-yet state, never fabricated instances.
//
// Response: {"status":"ok","msg":"","data":[{"name":"acme-base","org":"acme",
// "url":"https://acme.base.hanzo.ai","status":"running","plan":"pro","region":"nyc3",
// "created":"2026-03-01T00:00:00Z"}],"total":1}
func (o ops) bases(ctx context.Context, _ *core.None) (*basesOut, error) {
	c, err := core.AdmitScoped(ctx, o.s)
	if err != nil {
		return nil, err
	}
	sc := core.ResolveScope(o.s, c)
	base, token, ok := baseAdminConfig()
	if !ok {
		return &basesOut{
			Status: core.OK,
			Msg:    "the Base engine is not yet embedded on this deployment",
			Data:   []baseInstance{},
			Total:  core.Total(0),
		}, nil
	}
	q := url.Values{}
	if !sc.Super && len(sc.Orgs) > 0 {
		q.Set("org", sc.Orgs[0]) // defense 1: server-side narrowing to the caller's org
	}
	target := base + "/v1/base/instances"
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	raw, code, err := baseProxy(ctx, target, token)
	if err != nil {
		return &basesOut{Status: core.Err, Msg: err.Error()}, nil
	}
	if code/100 != 2 {
		return &basesOut{Status: core.Err, Msg: fmt.Sprintf("base engine returned http %d", code)}, nil
	}
	// Defense 2: re-check every row against the resolved scope. A scoped caller NEVER
	// sees a row outside their subtree even if the upstream ignored ?org=.
	out := make([]baseInstance, 0)
	for _, r := range decodeInstances(raw) {
		if sc.ScopedToOrg(r.Org) {
			out = append(out, r)
		}
	}
	return &basesOut{Status: core.OK, Data: out, Total: core.Total(len(out))}, nil
}

// basesOut is the GET /v1/admin/bases envelope. total == len(data): the list is the
// caller's whole window after scope filtering, unpaginated.
type basesOut struct {
	Status string         `json:"status"`
	Msg    string         `json:"msg"`
	Data   []baseInstance `json:"data"`
	Total  *int           `json:"total,omitempty"`
}

// decodeInstances tolerates BOTH a bare JSON array and a { data: [...] } envelope (the two
// shapes a Base admin surface might return), so the panel is robust to the engine's exact
// wire form.
func decodeInstances(raw json.RawMessage) []baseInstance {
	body := raw
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Data) > 0 {
		body = env.Data
	}
	var rows []baseInstance
	_ = json.Unmarshal(body, &rows)
	return rows
}
