package lsp

// meter.go charges for this surface, on the Bill/Gate pattern apps/answer and
// every other metered app already use — Base.Bill is the per-org Meter
// the composition root builds, and the prepaid Commerce ledger behind it is the
// one ledger. Nothing here is a second accounting of anything.
//
// # What is billed, and why it is the prepare
//
// PREPARING a revision is a tree write, a dependency fetch and a language server
// indexing a repository: seconds to minutes of CPU, hundreds of megabytes, and a
// process that then sits resident. That is the cost this service actually incurs,
// so that is the event that carries a fee. There are two Models on the ledger and
// they name exactly that difference: "prepare" and "query".
//
// A QUERY against a prepared revision is a JSON-RPC round trip to a process that
// is already running and already holds the index. It costs microseconds. Charging
// per query would price the cheap thing and hide the expensive one, which teaches
// callers to re-key their revision instead of reusing it — the opposite of what
// the daemon's pool is for. Queries are recorded for attribution and cost nothing.
//
// The GATE runs before the work, not after: an out-of-funds caller gets a clean
// 402 instead of a dependency fetch we paid for and cannot bill.

import (
	"context"
	"strings"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// kind names this surface on the ledger — the scope a debit is attributed to and
// the one string commerce groups these charges by.
const kind = "lsp"

// prepareCents is the flat fee for preparing one revision. Flat rather than
// measured because the caller chooses the repository, not the cost of indexing
// it, and a bill that varies with how large somebody else's dependency tree
// turned out to be is not one anybody can predict.
const prepareCents = 2

// The two Models this surface books under — what the ledger row says the money
// bought. They are the only two things that happen here, and which one it was is
// the daemon's answer, never a guess made on this side.
const (
	modelPrepare = "prepare"
	modelQuery   = "query"
)

// payer is the org whose ledger this request debits: principal.Ledger, the
// SELECTED billing org, which a SuperAdmin masquerade deliberately moves off the
// effective org — so it is not what principal.Org carries. It falls back to the
// effective org, which is the answer for every caller who is not masquerading.
//
// It is read from the REQUEST and never from a body field: a caller-supplied
// payer is a caller billing somebody else. Empty off the HTTP path, which is the
// unbilled default — and the endpoint has already refused anything without a
// validated principal before this runs.
func payer(c *zip.Ctx, org string) account.Account {
	if a := principal.Payer(c); !a.Zero() {
		return a
	}
	// Off the HTTP path there is no request to resolve; the caller's own org is the
	// address, parsed by the one rule rather than assumed to be a bare slug.
	return account.PayerOf("", org)
}

// gate refuses the request unless the payer can cover a prepare.
//
// It gates the PREPARE price on every request, including ones the daemon will
// answer from a revision it already holds, because whether it holds one is not
// known until it is asked — and it is not a fact about the caller. Gating the
// worst case and charging the real one is the order that never bills for work it
// refused.
func (s *state) gate(ctx context.Context, c *zip.Ctx, org string) error {
	subject := payer(c, org)
	if subject.Zero() {
		return nil // off the HTTP path: unbilled, and already principal-gated
	}
	// The project here is the VALIDATED cap scope — the per-project spend limit
	// the gate enforces — which is a different question from the attribution
	// scope the ledger records below, and answered by a different call.
	project, validated := principal.ValidatedProject(c)
	if err := s.Bill.Authorize(ctx, subject, project, validated, kind, prepareCents); err != nil {
		return cloud.DenyResource(c, err)
	}
	return nil
}

// charge records the debit once the work is done. A query against an already
// prepared revision debits zero — it is still recorded, so per-project
// attribution sees the traffic.
func (s *state) charge(c *zip.Ctx, org string, prepared bool) {
	subject := payer(c, org)
	if subject.Zero() {
		return
	}
	model, cents := modelQuery, int64(0)
	if prepared {
		model, cents = modelPrepare, prepareCents
	}
	s.Bill.Record(subject, kind, metering.Usage{
		Model:       model,
		AmountCents: cents,
		Project:     principal.ProjectScope(c),
		RequestID:   strings.Clone(strings.TrimSpace(c.Header("X-Request-Id"))),
		ClientIP:    strings.Clone(cloud.ClientIP(c)),
	})
}
