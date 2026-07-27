// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package apps

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"strings"

	aicontrollers "github.com/hanzoai/ai/controllers"
	aiobject "github.com/hanzoai/ai/object"
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
func mountZen(a cloud.Router, deps cloud.Deps) error {
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
		// zen's estimate is an exact 18-dp USD value. Fold it to whole cents for
		// the balance check via cloud's typed money.Amount.Cents() — a sub-cent
		// estimate gates as 0 (any-positive-balance), matching the edge gate's
		// AmountCents contract. The post-serve Meter debits the exact 18-dp.
		//
		// The fold is Cents() on the CREDIT amount, never Minor() on the zen one:
		// Minor() renders money.USD's 2 decimals, so it returned cents that FromInt
		// then read as atto — every estimate came back 0, and AuthorizeVerdict skips
		// its `available >= AmountCents` check when AmountCents is 0, admitting a
		// request of ANY size against any positive balance.
		cents := credit(est).Cents()
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
	// Beside the commerce debit, land the SAME warehouse row + gen_ai span every
	// native ai path writes (TraceServedUsage = recordTrace WITHOUT recordUsage —
	// the debit below is the one billing source, never doubled). zen knows its
	// EXACT per-tier retail (Charge) and upstream COGS (Cost), so the row carries
	// true margin (credit → nano). Without this, zen* traffic is
	// warehouse/o11y-blind exactly where prod runs (the unified binary).
	aicontrollers.TraceServedUsage(context.Background(), aicontrollers.ServedUsage{
		Owner:            u.Tenant.BillingOrg,
		User:             u.Tenant.User,
		Model:            u.Model,
		Provider:         zenProvider,
		RequestID:        u.RequestID,
		Status:           "success",
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		BilledNano:       nano(credit(u.Charge)),
		CostNano:         nano(credit(u.Cost)),
	})
	// Detached: the request context is recycled once the handler returns, so a
	// background context carries the debit to commerce without racing the reply.
	usage := meterUsage(u)
	go func() { _, _ = g.m.Record(context.Background(), usage) }()

	// Enso learning ledger: the embedded zen mount serves the zen catalog in-process
	// and never reaches ai's pipeToFamily, so ai's family-event writer never runs for
	// zen traffic. Write the SAME RoutingEvent here (source="family") through the ONE
	// shared writer, keyed on the client-visible response id (zen.Usage.ResponseID), so
	// zen* calls land in the same ledger — stats, world, spark retrain, and /v1/feedback
	// all read these rows. No prompt text; no shadow (zen.Usage carries no request
	// text — that stays the auto/enso-proxy path's job). Fire-and-forget.
	owner := u.Tenant.Org
	if owner == "" {
		owner = u.Tenant.BillingOrg
	}
	go aiobject.RecordFamilyRouting(aiobject.FamilyRoutingInput{
		Owner:            owner,
		User:             u.Tenant.User,
		RequestedModel:   u.Model,
		RoutedModel:      u.Upstream,
		ResponseId:       u.ResponseID,
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		CostCents:        credit(u.Charge).Cents(),
		RouterEndpoint:   os.Getenv("ROUTER_ENDPOINT"),
	})
}

// meterUsage projects a served zen.Usage onto the commerce debit. It is the ONE
// place the debit's amount is chosen, and it is pure — no ledger, no warehouse —
// so the money property is a unit test rather than an integration.
//
// The amount is the RETAIL Charge: what the caller pays. Cost is the upstream
// COGS we pay to serve the call; it is never the debit. It rides only the
// warehouse row (CostNano), where margin = Charge − Cost stays exact. Debiting
// Cost would collect our own COGS and book zero margin on every zen call — and
// because the affiliate and OSS payout bases read this debit, it would fund
// their shares out of principal. This mirrors ai, whose debit is likewise the
// customer price (usageBilledCents), never its CostIn/CostOut COGS.
func meterUsage(u zen.Usage) metering.Usage {
	return metering.Usage{
		User:             u.Tenant.BillingOrg,
		Org:              u.Tenant.BillingOrg,
		Actor:            u.Tenant.User,
		Model:            u.Model,
		Provider:         zenProvider,
		Service:          zenService,
		Project:          u.Tenant.Project,
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.PromptTokens + u.CompletionTokens,
		Amount:           credit(u.Charge), // exact 18-dp USD, no floor
		RequestID:        u.RequestID,
		Currency:         "usd",
		Status:           "success",
	}
}

// credit re-denominates a zen price into cloud's credit unit. It is the ONE
// conversion at this seam — every site below goes through it, so the unit is
// decided once rather than re-derived per call site.
//
// zen prices every SKU as an exact 18-dp value tagged money.USD (meter.go:
// money.New(<18-dp decimal>, money.USD)), and cloud's credit unit is the SAME USD
// value at 18-dp storage scale. So the conversion carries the exact decimal across
// and changes only the minor-unit convention: no rescale, no rounding, no factor.
// It is right by construction because the decimal is the value — the currency's
// Decimals is a rendering convention, not part of it.
//
// It must NEVER go through Amount.Minor(). money.USD declares 2 decimals, so
// Minor() rescales zen's 18-dp value to CENTS; feeding cents to the 18-dp
// FromInt understates the debit by 10^16 (a $17.376 charge debits $0.0000000000000017),
// and folds every sub-cent charge to a zero the ledger drops entirely.
func credit(a hmoney.Amount) cloudmoney.Amount { return cloudmoney.FromDecimal(a.Decimal()) }

// nano folds an exact credit Amount to nano-USD (1e-9) for the warehouse margin
// columns. It takes the typed Amount rather than a bare *big.Int so the unit is
// carried by the type: a cents integer is not a cloudmoney.Amount and can no
// longer be passed here. A single request's cost always fits int64 at nano.
func nano(a cloudmoney.Amount) int64 {
	return new(big.Int).Div(a.Atto(), big.NewInt(1_000_000_000)).Int64()
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
