package link

// wire.go is the composition root for the linked-account router: it builds a live
// Router from cloud.Deps — the KMS-backed Resolver, the commerce Meter over the
// routed-usage counter, and the mounted account registry — and provides the
// reference Upstream over the platform AIClient. It is the ONE place the seams are
// joined for production; tests join fakes directly.
//
// INERT UNTIL CALLED. Building a Router wires nothing into the request path. The
// cross-repo increment that calls Route from the /v1 chat egress — threading the
// resolved credential via WithCredential so the in-process ai provider adapter dials
// with the caller's own account — lands separately, so this branch adds a proven,
// self-contained engine without changing the hot path now under review. This mirrors
// exactly how route.go shipped the redundancy POLICY and deferred EXECUTION.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
)

// policyFromEnv reads the cycle policy (LINK_ROUTER_POLICY), defaulting to the
// route.go redundancy order.
func policyFromEnv() Policy {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LINK_ROUTER_POLICY"))) {
	case "most-remaining", "mostremaining", "headroom":
		return PolicyMostRemaining
	case "round-robin", "roundrobin", "rr":
		return PolicyRoundRobin
	default:
		return PolicyPlan
	}
}

// cooldownFromEnv reads the post-429 per-account cooldown (LINK_ROUTER_COOLDOWN),
// defaulting to 60s.
func cooldownFromEnv() time.Duration {
	if s := strings.TrimSpace(os.Getenv("LINK_ROUTER_COOLDOWN")); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	return 60 * time.Second
}

// feeFromEnv builds the BYO platform-fee Pricer from LINK_BYO_FEE_UUSD_PER_1K
// (micro-USD per 1000 tokens, mirroring CLOUD_AI_PRICE_UUSD_PER_1K's shape). The
// default is 0 — the honest no-fee default: a customer routing through their OWN
// api key pays the provider directly, and Hanzo invents no charge until an operator
// sets the fee. Usage is metered regardless.
func feeFromEnv() Pricer {
	s := strings.TrimSpace(os.Getenv("LINK_BYO_FEE_UUSD_PER_1K"))
	if s == "" {
		return ZeroPrice
	}
	uusdPer1k, err := strconv.ParseInt(s, 10, 64)
	if err != nil || uusdPer1k <= 0 {
		return ZeroPrice
	}
	return func(res Result) int64 {
		if res.TotalTokens <= 0 {
			return 0
		}
		micros := res.TotalTokens * uusdPer1k / 1000
		cents := (micros + 9999) / 10000 // ceil micro-USD → cents
		if cents < 0 {
			return 0
		}
		return cents
	}
}

// NewRouterFromDeps builds the live Router from cloud.Deps and a caller-supplied
// Upstream. The account registry is the mounted link Store. It returns (nil, false,
// nil) when link is not mounted — so a co-resident caller can cleanly skip routing —
// and an error only for a bad configuration.
func NewRouterFromDeps(deps cloud.Deps, up Upstream) (*Router, bool, error) {
	if mounted == nil {
		return nil, false, nil
	}
	if up == nil {
		return nil, false, fmt.Errorf("link: NewRouterFromDeps requires an Upstream")
	}
	store := mounted.State.store
	r, err := NewRouter(Config{
		Links:    store,
		Resolver: NewKMSResolver(deps.KMS),
		Upstream: up,
		Meter:    NewMeter(store, deps.Metering, feeFromEnv(), deps.Logger),
		Logger:   deps.Logger,
		Policy:   policyFromEnv(),
		Cooldown: cooldownFromEnv(),
	})
	if err != nil {
		return nil, false, err
	}
	return r, true, nil
}

// AIClientUpstream adapts a platform types.AIClient into an Upstream. It threads the
// resolved credential + account to the client through the request context
// (WithCredential / WithAccount) — never a header, body, or log — so an
// ACCOUNT-AWARE client (the in-process ai egress that reads CredentialFrom at dial
// time) authenticates with the caller's OWN credential. The request Payload must be
// a *types.ChatRequest.
type AIClientUpstream struct{ AI types.AIClient }

// Call dials the wrapped AIClient with the credential attached to the context. A
// provider quota/429 surfaces as an error the router classifies via IsQuota and
// cycles on; any other error is terminal.
func (u AIClientUpstream) Call(ctx context.Context, cred Credential, a Account, req Request) (Result, error) {
	cr, ok := req.Payload.(*types.ChatRequest)
	if !ok || cr == nil {
		return Result{}, fmt.Errorf("link: AIClientUpstream requires a *types.ChatRequest payload")
	}
	ctx = WithAccount(WithCredential(ctx, cred), a)
	resp, err := u.AI.ChatCompletion(ctx, cr)
	if err != nil {
		return Result{}, err
	}
	if resp == nil {
		return Result{Model: cr.Model}, nil
	}
	return Result{
		Model:            cr.Model,
		PromptTokens:     int64(resp.PromptTokens),
		CompletionTokens: int64(resp.CompletionTokens),
		TotalTokens:      int64(resp.TotalTokens),
		Response:         resp,
	}, nil
}
