package billing

// Per-scope spend-cap ENFORCEMENT (issue #70) — the money-path verdict the cloud
// metering gate consumes, computed over the SAME spend-alert rows the console
// Budgets page manages (no parallel model) and the SAME append-only usage ledger
// the balance gate reads (no parallel counter → no drift).
//
// Composition when several rows cover one request: MOST RESTRICTIVE WINS. The
// request is DENIED if ANY covering ENFORCE=true row is over its cap; the tightest
// (smallest-cap) violated row is reported. When none block, the highest soft-
// threshold utilization across covering rows drives the X-Spend-Warn header.
//
// Concurrency note: the commerce ledger is append-only with NO atomic
// compare-and-swap (RunInTransaction is a stub), so the cap is a SOFT boundary —
// a burst of concurrent requests can each read the same pre-debit spend and
// slightly overshoot. This is inherent to a lock-free money ledger and accepted;
// the cap still bounds sustained spend to the ceiling.

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/employee"
	"github.com/hanzoai/cloud/clients/commerce/models/spendalert"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"
)

// currentPeriod is the UTC calendar month, e.g. "2026-07". A cap resets at the
// first of each UTC month.
func currentPeriod() string { return time.Now().UTC().Format("2006-01") }

// periodStartUTC is midnight on the first of the current UTC month — the lower
// bound of the spend window a cap is measured over.
func periodStartUTC() time.Time {
	n := time.Now().UTC()
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// parseCents parses a non-negative cents amount; any invalid/negative value is 0
// (a pure "am I already over the cap" check).
func parseCents(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// scopeSpentCents sums this period's api-usage debits for the (project,service)
// scope in the caller's org namespace. Empty project/service is a WILDCARD on
// that axis (no filter), so the org-wide scope ("","") sums ALL usage, a project
// scope (P,"") sums every service in P, etc. — the covering relation.
//
// Reuses the exact ledger the balance gate reads (Withdraw, SourceKind
// "iam-user", Tags "api-usage") so the cap counts precisely the debited spend.
// The window is a single inequality (CreatedAt >= period start), datastore-legal
// alongside the equality filters. Idempotent recording (usage.go dedups on
// requestId) guarantees a retried debit is counted at most once.
func scopeSpentCents(db *datastore.Datastore, test bool, project, service string) (int64, error) {
	rootKey := db.NewKey("synckey", "", 1, nil)
	q := transaction.Query(db).Ancestor(rootKey).
		Filter("Test=", test).
		Filter("SourceKind=", "iam-user").
		Filter("Tags=", "api-usage").
		Filter("CreatedAt>=", periodStartUTC())
	if project != "" {
		q = q.Filter("Project=", project)
	}
	if service != "" {
		q = q.Filter("Service=", service)
	}

	transs := make([]*transaction.Transaction, 0)
	if _, err := q.GetAll(&transs); err != nil {
		return 0, err
	}
	var sum int64
	for _, t := range transs {
		sum += int64(t.Amount)
	}
	return sum, nil
}

// loadOrgScopes lists the org's spend-alert (budget/cap) rows — the org-wide
// policy set the cap verdict and the rate-limit config both read. BOUNDED to
// maxScopeRowsPerOrg so an attacker cannot inflate the row set to slow the scan
// (CreateSpendAlert enforces the same cap on writes).
func loadOrgScopes(db *datastore.Datastore) ([]*spendalert.SpendAlert, error) {
	rootKey := db.NewKey("synckey", "", 1, nil)
	rows := make([]*spendalert.SpendAlert, 0)
	_, err := spendalert.Query(db).Ancestor(rootKey).Limit(maxScopeRowsPerOrg).GetAll(&rows)
	return rows, err
}

// scopeExhausted reports whether a scope has reached/exceeded its cap such that
// ENFORCE refuses any further billable (>=1c) request — the ONE boundary both the
// verdict and the `over` display derive from, so they can never drift. It is
// exactly !WithinLimit(spent, 1, cap): committing one more cent breaks the cap.
func scopeExhausted(spent, cap int64) bool {
	return cap > 0 && !employee.WithinLimit(currency.Cents(spent), 1, currency.Cents(cap))
}

// hardAxesValidated reports whether a row may HARD-enforce (402) given whether the
// caller's PROJECT is bound to a validated claim. The ORG axis is always validated
// (owner claim) and the SERVICE axis is server-derived (route/provider, not a
// client field) — both always trustworthy. Only a row that constrains PROJECT
// (row.Project != "") needs projectValidated; without it that row DEGRADES to a
// soft warn (records + warns, never 402) so a forgeable X-Project-Id can never be
// used to hard-stop, nor evaded to bypass. (IAM does not yet mint a project claim;
// when it does, cloud sets pv=1 and project caps auto-harden — see
// principal.ValidatedProject.)
func hardAxesValidated(s *spendalert.SpendAlert, projectValidated bool) bool {
	return s.Project == "" || projectValidated
}

// authorizeResult is the gate verdict the metering client consumes. reason is
// "" (allow) or "spend_cap" (deny). warnPct is the actual utilization percent of
// the most-utilized covering cap when at/over its soft threshold (else 0), so the
// gate emits X-Spend-Warn without a second round trip.
type authorizeResult struct {
	Allow      bool   `json:"allow"`
	Reason     string `json:"reason"`
	CapCents   int64  `json:"capCents"`
	SpentCents int64  `json:"spentCents"`
	WarnPct    int    `json:"warnPct"`
}

// AuthorizeSpendCap is the per-request cap verdict for a (project,service) scope
// and a proposed amount, over the org's spend-alert rows. It evaluates EVERY
// covering row (most-restrictive-wins) and DENIES when any HARD-enforceable
// ENFORCE=true row is exceeded, reporting the tightest one. Soft rows — and a
// project-scoped enforce row whose project axis is NOT validated (pv=0) — never
// block; they only raise the warn utilization.
//
// FAIL CLOSED, not open: if a HARD-enforceable enforce row's spend sum cannot be
// computed, the verdict DENIES (spend_cap) — an attacker must not disable a cap by
// inducing a compute error. Per-scope sums are memoized so covering rows sharing a
// scope cost one query. The row scan is bounded (loadOrgScopes).
//
//	GET /v1/billing/spend-alerts/authorize?user=&project=&service=&amount=&pv=
func AuthorizeSpendCap(c *gin.Context) {
	org := middleware.GetOrganization(c)
	db := datastore.New(org.Namespaced(c))
	test := org.TestMode()

	reqProject := spendalert.NormalizeProject(c.Query("project"))
	reqService := strings.TrimSpace(c.Query("service"))
	amount := parseCents(c.Query("amount"))
	projectValidated := c.Query("pv") == "1"

	rows, err := loadOrgScopes(db)
	if err != nil {
		log.Error("spend-cap: load scopes failed: %v", err, c)
		http.Fail(c, 500, "failed to load spend caps", err)
		return
	}

	spentBy := map[string]int64{} // memoize per (project,service) scope.
	res := authorizeResult{Allow: true}
	var blockCap int64 // tightest violated enforced cap (most restrictive wins).
	for _, s := range rows {
		if s.Threshold <= 0 || !s.Covers(reqProject, reqService) {
			continue
		}
		hard := s.Enforce && hardAxesValidated(s, projectValidated)

		key := s.Project + "\x00" + s.Service
		spent, ok := spentBy[key]
		if !ok {
			v, serr := scopeSpentCents(db, test, s.Project, s.Service)
			if serr != nil {
				// FAIL CLOSED for a hard-enforceable row whose spend is unknown.
				if hard {
					log.Error("spend-cap: agg failed, failing CLOSED for enforce scope: %v", serr, c)
					c.JSON(200, authorizeResult{Allow: false, Reason: "spend_cap", CapCents: s.Threshold})
					return
				}
				continue // soft/degraded row: cannot warn, do not block.
			}
			spent = v
			spentBy[key] = v
		}

		// REUSE the spend primitive: within iff committed+requested <= cap.
		within := employee.WithinLimit(currency.Cents(spent), currency.Cents(amount), currency.Cents(s.Threshold))
		if hard && !within {
			if res.Reason != "spend_cap" || s.Threshold < blockCap {
				blockCap = s.Threshold
				res.Reason = "spend_cap"
				res.Allow = false
				res.CapCents = s.Threshold
				res.SpentCents = spent
			}
			continue
		}
		// Warn utilization — for soft rows, degraded project rows, AND enforce rows
		// still under cap (approaching the ceiling).
		if pct := int(spent * 100 / s.Threshold); pct >= s.EffectiveSoftPct() && pct > res.WarnPct {
			res.WarnPct = pct
		}
	}
	if !res.Allow {
		res.WarnPct = 0 // a deny carries no warn.
	}
	c.JSON(200, res)
}
