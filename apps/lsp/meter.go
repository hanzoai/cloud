package lsp

// meter.go charges for this surface, on the Bill/Gate pattern apps/answer and
// every other metered app already use — Base.Bill is the per-org ResourceMeter
// the composition root builds, and the prepaid Commerce ledger behind it is the
// one ledger. Nothing here is a second accounting of anything.
//
// # What is billed, and why it is the cold start
//
// A COLD start is a git checkout, a dependency fetch and a language server
// indexing a repository: seconds to minutes of CPU, hundreds of megabytes, and a
// process that then sits resident. That is the cost this service actually incurs,
// so that is the event that carries a fee.
//
// A WARM point query is a JSON-RPC round trip to a process that is already
// running and already holds the index. It costs microseconds. Charging per query
// would price the cheap thing and hide the expensive one, which teaches callers
// to re-key their workspace instead of reusing it — the opposite of what the pool
// is for. Warm queries are recorded for attribution and cost nothing.
//
// The GATE runs before the work, not after: an out-of-funds caller gets a clean
// 402 instead of a checkout we performed and cannot bill.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// kind names this surface on the ledger — the scope a debit is attributed to and
// the one string commerce groups these charges by.
const kind = "lsp"

// coldCents is the flat fee for one cold start. Flat rather than measured because
// the caller chooses the repository, not the cost of indexing it, and a bill that
// varies with how large somebody else's dependency tree turned out to be is not
// one anybody can predict.
const coldCents = 2

// payer is the org whose ledger this request debits: principal.Ledger, the
// SELECTED billing org, which a SuperAdmin masquerade deliberately moves off the
// effective org — so it is not what principal.Org carries. It falls back to the
// effective org, which is the answer for every caller who is not masquerading.
//
// It is read from the REQUEST and never from a body field: a caller-supplied
// payer is a caller billing somebody else. Empty off the HTTP path, which is the
// unbilled default — and the door has already refused anything without a
// validated principal before this runs.
func payer(c *zip.Ctx, org string) string {
	if subject := principal.Ledger(c); subject != "" {
		return subject
	}
	return org
}

// gate refuses the request unless the payer can cover a cold start.
//
// It gates the COLD price on every request, including ones that will turn out to
// be warm, because whether a workspace is warm is not known until the pool is
// asked — and it is not a fact about the caller. Gating the worst case and
// charging the real one is the order that never bills for work it refused.
func (s *state) gate(ctx context.Context, c *zip.Ctx, org string) error {
	subject := payer(c, org)
	if subject == "" {
		return nil // off the HTTP path: unbilled, and already principal-gated
	}
	// The project here is the VALIDATED cap scope — the per-project spend limit
	// the gate enforces — which is a different question from the attribution
	// scope the ledger records below, and answered by a different call.
	project, validated := principal.ValidatedProject(c)
	if err := s.Bill.Gate(ctx, subject, project, validated, kind, coldCents); err != nil {
		return cloud.DenyResource(c, err)
	}
	return nil
}

// charge records the debit once the work is done. A warm query debits zero — it
// is still recorded, so per-project attribution sees the traffic.
func (s *state) charge(c *zip.Ctx, org, method string, cold bool) {
	subject := payer(c, org)
	if subject == "" {
		return
	}
	var cents int64
	if cold {
		cents = coldCents
	}
	s.Bill.MeterUsage(subject, kind, metering.Usage{
		Model:       method,
		AmountCents: cents,
		Project:     principal.ProjectScope(c),
		RequestID:   strings.Clone(strings.TrimSpace(c.Header("X-Request-Id"))),
		ClientIP:    strings.Clone(cloud.ClientIP(c)),
	})
}
