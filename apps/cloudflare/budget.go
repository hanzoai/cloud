package cloudflare

// budget.go — which Cloudflare refusals are a BUDGET STOP, and on which layer.
//
// Cloudflare stops an account's work at four ceilings, and none of them says so
// in a way a status can carry: Workers and Durable Objects at the account's
// spending limit (Billing > Spend Management), Workers AI at the end of the daily
// free allocation (3036, a 429 — the same status as 3040, "capacity temporarily
// exceeded"), and AI Gateway at a gateway's spend limit (a bare 429, the same
// status as its rate limit). Relayed as-is they read as "slow down" or, through
// cfErr's default, a 502 — so the org was told nothing about which ceiling to
// raise, and "retry" was the one response certain not to help.
//
// The classification is by WHAT CLOUDFLARE SAID, never by status alone, because
// every status here is shared with a refusal that is not a budget. Codes where
// Cloudflare documents one (3036; 1103 to a visitor); otherwise the words a
// spending limit is said in, with the layer taken from the words when they name a
// product and from the path when they do not. A refusal matching neither is not a
// budget stop and keeps its old mapping.
//
// What the stop then MEANS is sdk/supply's: one class, budget_exhausted, naming
// the layer — rendered by cfErr and said on the pulse by the client that read it.

import (
	"context"
	"errors"
	"strings"

	"github.com/hanzoai/cloud/sdk/supply"
)

// Cloudflare's documented codes for a budget stop.
const (
	// codeNeuronAllocation is Workers AI's "Account limited": the daily free
	// allocation of Neurons is used up. It resets at 00:00 UTC.
	codeNeuronAllocation = 3036
	// codeWorkerSpendLimit is what a visitor of a Worker stopped at its account's
	// spending limit receives (1103).
	codeWorkerSpendLimit = 1103
)

// budgetWords are the phrases Cloudflare says a spending ceiling in. One must be
// present for a refusal without a documented code to be read as a stop.
var budgetWords = []string{"spending cap", "spending limit", "spend limit", "budget", "daily free allocation"}

// budgetLayer is the layer a Cloudflare refusal stopped at, or "" when it is not a
// budget stop. path is the Cloudflare API path the refusal answered.
func budgetLayer(path string, code int, msg string) supply.Layer {
	switch code {
	case codeNeuronAllocation:
		return supply.WorkersAI
	case codeWorkerSpendLimit:
		return supply.Workers
	}
	m := strings.ToLower(msg)
	if !containsAny(m, budgetWords) {
		return ""
	}
	// The product the sentence names, most specific first: "Workers AI" before
	// "Workers", so a Workers AI sentence is never filed under Workers.
	switch {
	case strings.Contains(m, "durable object"):
		return supply.DurableObjects
	case strings.Contains(m, "workers ai"), strings.Contains(m, "neurons"):
		return supply.WorkersAI
	case strings.Contains(m, "gateway"):
		return supply.AIGateway
	case strings.Contains(m, "worker"):
		return supply.Workers
	}
	// A sentence that names no product: the address it answered does.
	p := strings.ToLower(path)
	switch {
	case strings.Contains(p, "/ai-gateway/"):
		return supply.AIGateway
	case strings.Contains(p, "/ai/run/"):
		return supply.WorkersAI
	case strings.Contains(p, "durable_objects"):
		return supply.DurableObjects
	case strings.Contains(p, "/workers/"):
		return supply.Workers
	}
	return ""
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// stop is the budget stop err carries, if it is one.
func stop(err error) (*supply.Exhausted, bool) {
	ce, ok := errors.AsType[*cfError](err)
	if !ok || ce.layer == "" {
		return nil, false
	}
	return &supply.Exhausted{Provider: "cloudflare", Layer: ce.layer, Upstream: ce.upstream, Msg: ce.said}, true
}

// refused is envErr for a response this client read: the same error, and when it
// is a budget stop it is also counted and said on the pulse, naming the org whose
// account stopped. The request itself is answered by cfErr.
func (cl *client) refused(ctx context.Context, path string, status int, data []byte) error {
	err := envErr(path, status, data)
	if ex, ok := stop(err); ok {
		ex.Org = cl.org
		supply.Report(ctx, ex)
	}
	return err
}
