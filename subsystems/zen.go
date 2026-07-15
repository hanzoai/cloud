// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package subsystems

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/metering"
	cloudmoney "github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/clients/principal"
	hmoney "github.com/hanzoai/money"
	"github.com/hanzoai/zen"
	"github.com/zap-proto/zip"
)

// mountZen mounts zen co-resident in the unified cloud binary. zen is the ONE
// serving layer for the zen model family; it owns identity, routing, the 1M
// context ladder, vision, tools, and the Anthropic↔OpenAI codec. ai stays the
// auth+billing+discovery seam and the /v1/models authority; it no longer carries
// a parallel zen table or identity prompts. (See hip-00NN.)
//
// zen mounts as a MIDDLEWARE, not a route owner: Claim is scoped to /v1/* and
// routes every request whose model is a zen SKU to zen's pipeline in-process,
// calling c.Next() for everything else so ai's /v1/* catch-all serves non-zen
// models. ONE mount mechanism; the host owns the routes, zen owns the family.
//
// Billing. cloud's edge middleware (serve.go) runs IdentityMiddleware,
// AuditTrail, ScopeRateLimit, and BillingGate app-wide BEFORE MountAll, so a
// zen-claimed request is already authenticated, audited, and rate-limited at the
// edge. But the edge BillingGate prices bare /v1/messages, /v1/chat/completions,
// and /v1/embeddings at 0 (they are not under /v1/ai/ in selfMeteredPrefixes), and
// ai's OWN in-handler metering — which used to bill zen* — is SKIPPED because
// zen's Claim runs before ai's beego catch-all. zen therefore bills zen* itself,
// through the same commerce metering client the edge gate uses, so there is ONE
// billing source for zen* (never double-billed, never free): zen's Gate
// authorizes the estimate before the upstream call, zen's Meter records the
// exact served cost after. zen's Meter/Gate are wired here; ai's edge gate stays
// 0 for these paths.
//
// Billing granularity is org / project / user, mirroring the edge gate's
// identityFromCtx exactly:
//   - the HOME org (principal.BillingOrg) is the balance key — who PAYS. An admin
//     acting in another org bills the admin's home org, never the org acted on.
//   - the project (principal.ValidatedProject) scopes spend caps; a project may
//     carry its own billing account, resolved server-side by commerce from the
//     org's project binding (the X-Billing-Account-Id header only attributes,
//     never redirects spend). Empty is the org-wide default.
//   - the user (c.User) is the actor for the audit trail.
//
// It is wired BEFORE ai in Wire() so Claim's c.Next() falls through to ai's
// catch-all. zen's catalog reads its upstream keys from KMS via the Key resolver.
func mountZen(app any, deps cloud.Deps) error {
	a, ok := app.(*zip.App)
	if !ok {
		return fmt.Errorf("zen.Mount: app is %T, want *zip.App", app)
	}
	z, err := zen.New(zen.Config{
		Logger: deps.Logger,
		Key:    zenKeyResolver(deps.KMS),
		Tenant: cloudTenantResolver,
		Gate:   commerceGate(deps.Metering),
		Meter:  commerceMeter(deps.Metering),
	})
	if err != nil {
		return fmt.Errorf("zen: %w", err)
	}
	// The one mount: Claim scoped to /v1, ahead of ai's catch-all.
	a.Group("/v1", z.Claim())
	return nil
}

// cloudTenantResolver is the multi-tenant billing-identity resolver: it keys
// the balance on the validated HOME org (who pays), resolves the project scope
// from a validated claim, and attributes the user. It mirrors the edge gate's
// identityFromCtx so a zen* debit lands on the SAME ledger axes the edge gate
// would have used — home-org balance, project+service scope, user actor — and a
// masquerading admin bills their own home org, never the org acted on. A request
// with no validated principal resolves to an empty Tenant, which zen's Valid()
// gate refuses (no free, anonymous usage).
func cloudTenantResolver(c *zip.Ctx) zen.Tenant {
	home, _ := principal.BillingOrg(c)
	project, _ := principal.ValidatedProject(c)
	return zen.Tenant{
		Org:        c.Org(),
		User:       c.User(),
		BillingOrg: home,
		Project:    project,
	}
}

// commerceGate is zen's pre-serve authorization backed by cloud commerce. It
// asks the metering client whether the home org can cover the request's
// estimated cost (priced at the tier that WILL serve, so an overflow is gated
// against its real cost). The balance key is the home org (User); the project
// scopes the spend cap. The estimate is exact 18-dp atto-USD from zen, folded to
// whole cents for the balance check (a sub-cent estimate gates as "any positive
// balance", the same contract as the edge gate's AmountCents). An unconfigured
// metering client (nil) admits everything — zen's own tenant gate still refuses
// anonymous traffic, and the edge BillingGate + ScopeRateLimit already ran. A
// denied verdict returns the metering reason so zen surfaces it as its 402.
func commerceGate(m *metering.Client) zen.Gate {
	if m == nil || !m.Enabled() {
		return nil
	}
	return func(ctx context.Context, t zen.Tenant, model string, est hmoney.Amount) error {
		if t.BillingOrg == "" {
			return fmt.Errorf("a billable tenant is required (no anonymous usage)")
		}
		// zen's estimate is exact 18-dp atto-USD (hanzoai/money). Fold to whole
		// cents for the balance check via cloud's typed money.Amount.Cents() — a
		// sub-cent estimate gates as 0 (any-positive-balance), matching the edge
		// gate's AmountCents contract. The post-serve Meter debits the exact 18-dp.
		cents := cloudmoney.FromInt(est.Minor()).Cents()
		v, err := m.AuthorizeVerdict(ctx, metering.AuthInput{
			User:        t.BillingOrg,
			Org:         t.BillingOrg,
			AmountCents: cents,
			Project:     t.Project,
			Service:     zenService,
		})
		if err != nil {
			// Balance unknown -> fail-closed (mirrors the edge gate's 503).
			return fmt.Errorf("billing unavailable")
		}
		if !v.Allow {
			if v.Reason == "spend_cap" {
				return fmt.Errorf("spend cap reached for this scope")
			}
			return fmt.Errorf("insufficient balance")
		}
		return nil
	}
}

// commerceMeter is zen's post-serve usage recorder backed by cloud commerce.
// It debits the home org for the EXACT served cost as a typed money.Amount
// (native 18-dp USD — the same precision the co-resident finance ledger holds),
// so an exact per-token cost is never floored to cents or micros. Attributed to
// the requested zen SKU with real token counts. The debit is detached (background
// context) so a client disconnect cannot cancel it and recording never blocks the
// reply — the same contract as the edge gate's post-request record. An
// unconfigured client is a no-op (zen's logMeter still ran as the default, so the
// audit trail is never empty).
func commerceMeter(m *metering.Client) zen.Meter {
	if m == nil || !m.Enabled() {
		return nil
	}
	return commerceMeterImpl{m}
}

type commerceMeterImpl struct{ m *metering.Client }

func (g commerceMeterImpl) Record(ctx context.Context, u zen.Usage) {
	if u.Tenant.BillingOrg == "" {
		return // never debit an unattributable request
	}
	usage := metering.Usage{
		User:            u.Tenant.BillingOrg,
		Org:             u.Tenant.BillingOrg,
		Actor:           u.Tenant.User,
		Model:           u.Model,
		Provider:        zenProvider,
		Service:         zenService,
		Project:         u.Tenant.Project,
		PromptTokens:    u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:     u.PromptTokens + u.CompletionTokens,
		Amount:          cloudmoney.FromInt(u.Cost.Minor()), // exact 18-dp USD, no floor
		RequestID:       u.RequestID,
		Currency:        "usd",
		Status:          "success",
	}
	// Detached: the request context is recycled once the handler returns, so a
	// background context carries the debit to commerce without racing the reply.
	go func() { _, _ = g.m.Record(context.Background(), usage) }()
}

// zenService is the commerce service axis zen* spend attributes to. zen serves
// the same LLM product ai does, so it shares ai's service label — a per-scope
// spend cap on "ai" binds both surfaces, and Observe reconciles them together.
const zenService = "ai"

// zenProvider is the metering provider label that marks a debit as zen-served
// (the LLM family zen owns) for internal cost reconciliation vs the raw upstream.
const zenProvider = "zen"

// zenKeyResolver is zen's upstream-credential resolver. zen's catalog names each
// provider's key by an env-var convention (DO_AI_API_KEY, ANTHROPIC_API_KEY, …);
// the resolver turns that name into a concrete secret. It reads the ENVIRONMENT
// FIRST, then falls back to KMS — the SAME order ai uses (object/kms.go: "the prod
// hot path resolves DO_AI_API_KEY from the env before any live KMS"). The operator
// injects these provider keys as env from the KMS-synced K8s secret
// (cloud-api-llm-keys), so the env is the live value; the embedded KMS store is a
// fallback that is not always seeded with the provider keys. Reading KMS-only made
// zen send an EMPTY key whenever the store lacked it, and the upstream (DO GenAI)
// answered 401 "Unable to authenticate you" — surfacing to the caller as a failed
// chat while ai (which reads env) worked. Env-first fixes that with one source of
// truth shared across both zen and ai. An empty result on both still returns "" so
// the upstream call fails fast (never silent free usage).
func zenKeyResolver(kms cloud.KMSClient) func(context.Context, string) string {
	return func(ctx context.Context, envName string) string {
		if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
			return v
		}
		if kms == nil {
			return ""
		}
		b, err := kms.GetSecret(ctx, envName)
		if err != nil || len(b) == 0 {
			return ""
		}
		return string(b)
	}
}
