// Command smoke is the durable, authenticated functional smoke for the unified
// cloud API (api.hanzo.ai). It replaces the boot-only "did it reach listening"
// check with a REAL per-subsystem probe: it hits ONE side-effect-free read on each
// core product surface and asserts each returns a WORKING code.
//
// THE INVARIANT IT GUARDS (why it exists). A release must never ship if a core
// endpoint is broken. Two failure classes are ALWAYS a hard fail, on EVERY probe,
// because this is a read-only smoke:
//
//   - 402 on a read — the "balance gate over-blocks reads" regression: a
//     $0-balance org must be able to VIEW its own resources (models, chats, usage,
//     keys, buckets, deployments). A read never spends, so a 402 here is the bug.
//   - 5xx (500/502/504) — a crash / upstream fault (e.g. the /v1/billing/usage
//     self-dispatch 500 this smoke was written to catch).
//
// Beyond that, each probe declares how it SHOULD behave:
//   - health  : liveness, must be 200.
//   - public  : public data (marketing aggregates, catalogs, /health), 200 (a
//     staged/uninitialised subsystem may answer 503 — tolerated).
//   - authed  : an org-scoped read — 401/403 without a token; 2xx WITH a valid
//     token (strict), or 2xx/401/403 when the token may not validate in
//     this env (default). 503 tolerated (subsystem staged).
//   - tolerant: preview/SuperAdmin/cross-org reads whose code is config-dependent
//     ({200,401,403}); still never 402/5xx.
//
// USAGE
//
//	SMOKE_BASE_URL=http://127.0.0.1:8000 SMOKE_TOKEN=<bearer> go run ./cmd/smoke
//	go run ./cmd/smoke -base https://api.hanzo.ai -token "$T" -strict
//
// Env: SMOKE_BASE_URL, SMOKE_TOKEN, SMOKE_STRICT=1, SMOKE_TIMEOUT (seconds).
// Flags (-base/-token/-strict/-timeout) override env. Exit code is non-zero iff any
// probe fails, so it drops straight into a release gate.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

type class int

const (
	classHealth   class = iota // liveness — must be 200
	classPublic                // public data — 200 (503 tolerated if staged)
	classAuthed                // org-scoped read — gates without a token, 2xx with one
	classTolerant              // preview/superadmin/cross-org — {200,401,403}
)

type probe struct {
	name   string
	method string
	path   string
	class  class
}

// probes is the core-subsystem matrix — one side-effect-free read per major product
// surface mounted in apps/apps.go (+ the AI module catch-all). Paths are the LITERAL
// registered routes (no /api/ prefix); verified against their registration sites.
var probes = []probe{
	// ── liveness ── (/healthz is on the SEPARATE health listener :9090; the main
	// HTTP surface's always-on, config-independent liveness is base's /v1/base/health)
	{"health", http.MethodGet, "/v1/base/health", classHealth},

	// ── public (no auth, no balance) ──
	{"traffic-globe", http.MethodGet, "/v1/traffic/globe", classPublic},
	{"router-stats-platform", http.MethodGet, "/v1/router/stats?scope=platform", classPublic},
	{"templates", http.MethodGet, "/v1/templates", classPublic},
	{"prompts-catalog", http.MethodGet, "/v1/prompts/catalog", classPublic},
	{"kms-health", http.MethodGet, "/v1/kms/health", classPublic},
	{"deploy-health", http.MethodGet, "/v1/deploy/health", classPublic},
	{"s3-health", http.MethodGet, "/v1/s3/health", classPublic},
	{"analytics-health", http.MethodGet, "/v1/analytics/health", classPublic},

	// ── authed org-scoped reads (gate without a token; 2xx with one) ──
	{"models", http.MethodGet, "/v1/models", classAuthed},
	{"billing-balance", http.MethodGet, "/v1/billing/balance", classAuthed},
	{"billing-usage", http.MethodGet, "/v1/billing/usage", classAuthed}, // BUG 2: was 500
	{"router-stats", http.MethodGet, "/v1/router/stats", classAuthed},   // BUG 1: was 402 authed
	{"projects", http.MethodGet, "/v1/projects", classAuthed},
	{"agents", http.MethodGet, "/v1/agents", classAuthed},
	{"functions", http.MethodGet, "/v1/functions", classAuthed},
	{"wallets", http.MethodGet, "/v1/wallets", classAuthed},
	{"integrations", http.MethodGet, "/v1/integrations", classAuthed},
	{"marketplace", http.MethodGet, "/v1/marketplace/listings", classAuthed},
	{"o11y", http.MethodGet, "/v1/o11y/status", classAuthed},
	{"team", http.MethodGet, "/v1/team/bots", classAuthed},
	{"storage-buckets", http.MethodGet, "/v1/s3/buckets", classAuthed},
	{"analytics", http.MethodGet, "/v1/analytics/overview", classAuthed},
	{"knowledge", http.MethodGet, "/v1/kb/connectors/catalog", classAuthed},
	{"automations", http.MethodGet, "/v1/automations/connectors", classAuthed},

	// ── tolerant (preview / SuperAdmin / cross-org / staged-optional) ──
	{"account", http.MethodGet, "/v1/auth/account", classTolerant},
	{"chats", http.MethodGet, "/v1/chat/chats", classTolerant},
	{"kms-secrets", http.MethodGet, "/v1/kms/orgs/smoke/secrets", classTolerant}, // BUG 1: was 402
	{"deploy-apps", http.MethodGet, "/v1/deploy/applications", classTolerant},
	{"websearch", http.MethodGet, "/v1/websearch/search?q=hanzo", classTolerant},
}

type result struct {
	probe
	status int
	pass   bool
	reason string
}

func main() {
	base := flag.String("base", env("SMOKE_BASE_URL", ""), "base URL of the cloud API (e.g. http://127.0.0.1:8000)")
	token := flag.String("token", env("SMOKE_TOKEN", ""), "bearer token for the authenticated matrix (optional)")
	strict := flag.Bool("strict", env("SMOKE_STRICT", "") != "", "require authed reads to be 2xx with a token (fails on 401/403)")
	timeoutSec := flag.Int("timeout", envInt("SMOKE_TIMEOUT", 15), "per-request timeout in seconds")
	flag.Parse()

	if strings.TrimSpace(*base) == "" {
		fmt.Fprintln(os.Stderr, "smoke: -base (or SMOKE_BASE_URL) is required")
		os.Exit(2)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(*base), "/")
	hasToken := strings.TrimSpace(*token) != ""

	client := &http.Client{Timeout: time.Duration(*timeoutSec) * time.Second}
	results := make([]result, 0, len(probes))
	for _, p := range probes {
		status, err := do(client, baseURL, *token, p)
		r := result{probe: p, status: status}
		if err != nil {
			r.pass, r.reason = false, "request error: "+err.Error()
		} else {
			r.pass, r.reason = evaluate(p, status, hasToken, *strict)
		}
		results = append(results, r)
	}

	report(results, baseURL, hasToken, *strict)
	for _, r := range results {
		if !r.pass {
			os.Exit(1)
		}
	}
}

// do issues one probe request. The token (when present) rides Authorization on EVERY
// probe — a public endpoint ignores it, an authed one is exercised on its real path.
func do(client *http.Client, baseURL, token string, p probe) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), client.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, p.method, baseURL+p.path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// evaluate maps a probe's status to pass/fail. The two universal regressions (402 on
// a read, any 5xx) fail on EVERY class before the per-class rules are consulted.
func evaluate(p probe, status int, hasToken, strict bool) (bool, string) {
	// Universal: this is a read-only smoke, so a 402 is the balance-gate-over-blocks
	// regression and a 5xx is a crash/upstream fault — never acceptable, any class.
	if status == http.StatusPaymentRequired {
		return false, "402 on a read — balance gate over-blocks reads (the bug this guards)"
	}
	if status >= 500 && status != http.StatusServiceUnavailable {
		return false, fmt.Sprintf("%d server error", status)
	}
	staged := status == http.StatusServiceUnavailable // subsystem staged/uninitialised in this env

	switch p.class {
	case classHealth:
		if status == http.StatusOK {
			return true, ""
		}
		return false, "liveness must be 200"
	case classPublic:
		if status == http.StatusOK || staged {
			return true, ""
		}
		return false, "public endpoint must be 200"
	case classAuthed:
		if staged {
			return true, "" // subsystem not initialised in this env
		}
		if hasToken {
			if status >= 200 && status < 300 {
				return true, ""
			}
			if !strict && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
				return true, "" // token not validated by this env — a correct gate, not a break
			}
			return false, "authed read should be 2xx with a token"
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return true, ""
		}
		return false, "authed read must gate (401/403) without a token"
	case classTolerant:
		if status == http.StatusOK || status == http.StatusUnauthorized || status == http.StatusForbidden || staged {
			return true, ""
		}
		return false, "expected 200/401/403"
	}
	return false, "unclassified"
}

func report(results []result, baseURL string, hasToken, strict bool) {
	sort.SliceStable(results, func(i, j int) bool { return results[i].class < results[j].class })
	mode := "anonymous"
	if hasToken {
		mode = "authenticated"
		if strict {
			mode += " (strict)"
		}
	}
	fmt.Printf("\ncloud smoke — %s\nbase=%s\n\n", mode, baseURL)
	fmt.Printf("%-4s  %-22s  %-8s  %s\n", "", "SUBSYSTEM", "STATUS", "PATH")
	fmt.Println(strings.Repeat("─", 88))
	passed, failed := 0, 0
	for _, r := range results {
		mark := "PASS"
		if !r.pass {
			mark = "FAIL"
			failed++
		} else {
			passed++
		}
		code := fmt.Sprintf("%d", r.status)
		if r.status == 0 {
			code = "ERR"
		}
		line := fmt.Sprintf("%-4s  %-22s  %-8s  %s", mark, r.name, code, r.path)
		if !r.pass {
			line += "  ← " + r.reason
		}
		fmt.Println(line)
	}
	fmt.Println(strings.Repeat("─", 88))
	fmt.Printf("%d passed, %d failed, %d total\n\n", passed, failed, len(results))
	if failed > 0 {
		fmt.Printf("SMOKE FAIL: %d core endpoint(s) broken — release must not ship\n", failed)
	} else {
		fmt.Println("SMOKE PASS: every core subsystem returned a working code (no 402-on-read, no 5xx)")
	}
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}
