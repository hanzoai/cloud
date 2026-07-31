// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The royalty BASIS: why the number is the number. GET /v1/authors shows an author
// the AMOUNT; without the basis the amount is unauditable, so this file serves the
// arithmetic behind it — the immutable ledger rows, the deploy edges that made a
// deploying org count, and the cost model the platform prices compute with.
//
// The split between the two is the whole design. A ledger row is a captured VALUE:
// LatchAccrual wrote share_bps, spend_cents and earning_cents in the SAME transaction
// as the balance increment, so the row explains itself forever — immune to a later
// rate retune, a share change, or a spend restatement. It is served VERBATIM and
// never recomputed. The rate card is a current MODEL: spend_cents is commerce's
// all-provider usage rollup, whose compute slice was priced hour-by-hour across the
// whole month (several cards can contribute to one row, and tokens/storage were never
// priced by a card at all), so stamping one card onto a row would be a fabrication by
// construction. The card is therefore served separately and labelled current by asOf.
// Consequence: no schema change, no migration, and no new risk to the money path.
package authors

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/blueprint"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// The PUBLISHED formulas. Fixed literals, not strings assembled from the constants
// they describe: a disclosure must read the same to every author regardless of what
// the process happens to be configured with. accrueOne and blueprint.priceFootprint
// are the code these two sentences describe.
const (
	formulaEarning      = "earningCents = spendCents * shareBps / 10000"
	formulaComputePrice = "microUsdPerHour = vcpu * microUsdPerVcpuHour + gb * microUsdPerGbHour"
)

// periodShape is exactly what periodKey mints. A period is echoed back into the
// response and into a SQL filter, so it is accepted only in the ONE form the accrual
// latch can ever have written.
var periodShape = regexp.MustCompile(`^\d{4}-\d{2}$`)

// basisOf builds ONE author's audit payload: the current model, their immutable rows,
// an account-wide reconciliation, and an honest statement of the slice returned. It is
// the ONE builder behind both the author's own read and the admin mirror — support
// reasons about the exact bytes the author is looking at, never a parallel view that
// could drift. period "" is every period.
func basisOf(s *cloud.Service[state], ctx context.Context, a Author, period string) (map[string]any, error) {
	rows, err := s.State.store.ListLedger(ctx, a.ID, period, ledgerLimit)
	if err != nil {
		return nil, fmt.Errorf("list ledger: %w", err)
	}
	deploys, err := s.State.store.ListDeploys(ctx, a.ID, deployLimit)
	if err != nil {
		return nil, fmt.Errorf("list deploys: %w", err)
	}
	ledgerRows, ledgerEarning, err := s.State.store.LedgerTotals(ctx, a.ID)
	if err != nil {
		return nil, err
	}

	// Group the attribution edges ONCE: a row is explained by its own deploying org's
	// edges, so one pass here keeps the row loop free of a per-row query.
	byOrg := make(map[string][]DeployEvent, len(deploys))
	for _, d := range deploys {
		byOrg[d.DeployingOrg] = append(byOrg[d.DeployingOrg], d)
	}

	consistent := true
	ledger := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		// REPORTED, never corrected: a stored earning that fails the formula is a fact
		// about the ledger, and silently recomputing it would erase the evidence.
		ok := r.EarningCents == r.SpendCents*r.ShareBps/bpsDenom
		consistent = consistent && ok
		ledger = append(ledger, map[string]any{
			"id":           r.ID,
			"period":       r.Period,
			"deployingOrg": r.DeployingOrg,
			"spendCents":   r.SpendCents,
			"shareBps":     r.ShareBps, // as APPLIED then — a share change never rewrites history
			// The other side of the SAME split, stated rather than left to be inferred:
			// the platform kept spend − earning at (10000 − shareBps). Both halves of
			// every row are readable, so the split is auditable without arithmetic.
			"platformShareBps":     bpsDenom - r.ShareBps,
			"platformEarningCents": r.SpendCents - r.EarningCents,
			"earningCents":         r.EarningCents,
			"consistent":           ok,
			"computeProof":         r.ComputeProof, // nil marshals to null: absence stays legible
			"createdAt":            r.CreatedAt,
			"attribution":          attributionAt(byOrg[r.DeployingOrg], r.CreatedAt),
		})
	}

	// An author reads their own rate and whether it is the platform default: a number
	// that sets your pay and that you cannot see is not auditable. Only the VALUE is
	// disclosed — who negotiated it, and when, stays in the admin audit trail. If
	// defaultShareBps is ever retuned, pre-existing authors correctly begin reading
	// "override": their share IS now non-default, so the statement stays true.
	shareSource := "default"
	if a.ShareBps != defaultShareBps {
		shareSource = "override"
	}

	out := map[string]any{
		"isAuthor": true,
		"id":       a.ID,
		"status":   a.Status,
		"asOf":     time.Now().Unix(),
		"shareBps": a.ShareBps,
		// The platform's own cut, published beside the creator's. Both are fields
		// read off the same one split, so neither side of the deal is a number the
		// other party has to take on trust.
		"platformShareBps": bpsDenom - a.ShareBps,
		"defaultShareBps":  defaultShareBps,
		"shareSource":      shareSource,
		// Where this author's payouts settle. "treasury" says plainly that this is a
		// FIRST-PARTY account whose royalty is realized into Hanzo's own reserve —
		// internal accounting, not an independent creator being paid.
		"settlesTo": settlementOf(s, a, methodCredits),
		"method": map[string]any{
			"spendBasis":   "org-metered-total",
			"period":       "utc-month",
			"earning":      formulaEarning,
			"computePrice": formulaComputePrice,
			"rateCard":     blueprint.Rates(),
			"sizing":       blueprint.Sizing(),
		},
		"ledger": ledger,
		// Two INDEPENDENT claims: the ledger foots to the balance (over ALL rows, never
		// the window), and every row shown here satisfies the formula.
		"reconciliation": map[string]any{
			"ledgerRows":         ledgerRows,
			"ledgerEarningCents": ledgerEarning,
			"accruedCents":       a.AccruedCents,
			"paidCents":          a.PaidCents,
			"pendingCents":       a.PendingCents(),
			"balanced":           ledgerEarning == a.AccruedCents,
			"consistent":         consistent,
		},
		// What slice you actually got — the shown rows must never imply the total.
		"window": map[string]any{
			"ledgerReturned":  len(rows),
			"ledgerLimit":     ledgerLimit,
			"deploysReturned": len(deploys),
			"deploysLimit":    deployLimit,
			"truncated":       len(rows) == ledgerLimit || len(deploys) == deployLimit,
		},
	}
	if period != "" {
		out["period"] = period
	}
	return out, nil
}

// attributionAt is WHY a deploying org counted for this author: the author's own
// deploy edges for that org that already existed when the row was written. An edge
// recorded afterwards cannot have explained the accrual, so the row's own timestamp
// is the cutoff. Nothing is inferred — an unexplained row simply carries no edges.
func attributionAt(edges []DeployEvent, at int64) []map[string]any {
	out := make([]map[string]any, 0, len(edges))
	for _, d := range edges {
		if d.CreatedAt > at {
			continue
		}
		out = append(out, map[string]any{
			"repoUrl":   d.RepoURL,
			"project":   d.Project,
			"createdAt": d.CreatedAt,
		})
	}
	return out
}

// AuthorBasisQuery narrows the royalty basis to one accrual period.
type AuthorBasisQuery struct {
	// Period is the UTC year-month the accrual latch mints, e.g. "2026-07". Empty is
	// every period; any other shape is refused.
	Period string `json:"period"`
}

// basis reads the audit trail behind the caller's OWN royalty. It answers the current
// cost model, the immutable ledger rows with the attribution edges that explain each,
// an account-wide reconciliation, and an honest statement of the slice returned.
//
// The subject is the principal's org, so no id can be supplied. It is a separate
// endpoint from the dashboard precisely because the dashboard accrues lazily on read:
// an audit must not move the money it is auditing, so this path never sweeps and
// calling it N times leaves the balances and the ledger byte-identical. An org that
// is not an author gets {isAuthor:false}, not a 404, which would answer "is this org
// an author?".
//
// The response is the open basis document, not a fixed record: the enrolled and
// not-enrolled answers carry different keys, and the model, reconciliation and window
// sections grow with the disclosure.
//
// Example: {"period":"2026-07"}
// Response: {"isAuthor":true,"id":"aut_9f2a","status":"approved","asOf":1780000000,"shareBps":2000,"platformShareBps":8000,"defaultShareBps":2000,"shareSource":"default","settlesTo":"wallet","period":"2026-07","ledger":[],"reconciliation":{"ledgerRows":0,"ledgerEarningCents":0,"accruedCents":0,"paidCents":0,"pendingCents":0,"balanced":true,"consistent":true}}
func (o ops) basis(ctx context.Context, in *AuthorBasisQuery) (*map[string]any, error) {
	s := o.s
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("sign in to view your royalty basis")
	}
	period, err := periodOf(in.Period)
	if err != nil {
		return nil, err
	}
	a, err := s.State.store.GetByOrg(ctx, org)
	if err == errNotFound {
		// Honest, not a 404 — a 404 here would answer "is this org an author?".
		return &map[string]any{
			"isAuthor":        false,
			"defaultShareBps": defaultShareBps,
		}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}
	out, err := basisOf(s, ctx, a, period)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return &out, nil
}

// AuthorBasisRef addresses one author's royalty basis for the support mirror.
type AuthorBasisRef struct {
	// ID is the author id from the path, as returned by the admin directory.
	ID string `json:"id"`
	// Period is the UTC year-month, e.g. "2026-07". Empty is every period.
	Period string `json:"period"`
}

// AuthorBasisOut is the GET /v1/admin/authors/:id/basis envelope.
type AuthorBasisOut struct {
	// Status is "ok".
	Status string `json:"status"`
	// Msg is empty on success.
	Msg string `json:"msg"`
	// Data is the SAME open basis document the author's own endpoint returns.
	Data map[string]any `json:"data"`
}

// adminBasis reads one author's royalty basis. It runs the SAME builder the author's
// own endpoint runs, so support sees exactly what the author sees. SuperAdmin only.
//
// Example: {"id":"aut_9f2a","period":"2026-07"}
// Response: {"status":"ok","msg":"","data":{"isAuthor":true,"id":"aut_9f2a","status":"approved","shareBps":2000,"platformShareBps":8000,"ledger":[]}}
func (o ops) adminBasis(ctx context.Context, in *AuthorBasisRef) (*AuthorBasisOut, error) {
	if err := admit(ctx); err != nil {
		return nil, err
	}
	s := o.s
	period, err := periodOf(in.Period)
	if err != nil {
		return nil, err
	}
	a, err := s.State.store.GetByID(ctx, strings.TrimSpace(in.ID))
	if err == errNotFound {
		return nil, zip.ErrNotFound("author not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "load author: %v", err)
	}
	out, err := basisOf(s, ctx, a, period)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	return &AuthorBasisOut{Status: "ok", Data: out}, nil
}

// periodOf validates the optional period narrowing, accepting only the shape the
// accrual latch mints.
func periodOf(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p != "" && !periodShape.MatchString(p) {
		return "", zip.ErrBadRequest("period must be YYYY-MM")
	}
	return p, nil
}
