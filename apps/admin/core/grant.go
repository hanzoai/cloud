package core

// The ONE credit-write path + the ONE tamper-evident audit emit. ApplyGrant is the
// single core shared by POST /v1/admin/customers/:org/credit (org from the path) and
// POST /v1/admin/grants (org from the body): validate the amount + target org, deposit
// into the org's commerce ledger (trial vs prepaid by source), and record the audit
// row. One path, one way to grant.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/hanzoai/commerce/billing/creditledger"
	"github.com/zap-proto/zip"
)

// CreditRequest is the grant body. AmountCents is the credit to add (positive only — a
// grant, never a silent debit). Reason is the operator's justification, recorded in the
// audit trail's before/after (refund / comp / support).
type CreditRequest struct {
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Reason      string `json:"reason"`
	// User names the MEMBER to credit, by IAM username — the `name` half of
	// "<org>/<name>". Empty credits the org itself.
	//
	// It exists because "the org" is not always the account a request spends from.
	// In the shared signup org, whose members are strangers to each other, each pays
	// from their OWN wallet; a grant keyed on the org alone lands in a pool that
	// member can neither spend nor see, while they are refused at $0. Which of the
	// two a grant lands on is NOT decided here — principal.WalletFor asks account.Payer,
	// the same rule the spend gate asks — so a pooled tenant org keeps one balance no
	// matter what is named, and a per-member org can finally be funded per member.
	User string `json:"user"`
	// Source splits the grant into the commerce ledger's two money buckets:
	//   - "trial"   (default) — a non-cash promo/comp credit: spendable on non-premium
	//                metered usage only, NEVER refundable cash and NEVER paid out.
	//   - "prepaid" — real money added to the customer's cash balance. Refundable,
	//                GPU-eligible.
	// Unknown/empty → trial (fail-closed to non-cash).
	Source string `json:"source"`
}

// maxGrantCents caps a single grant at $100,000 — a guardrail against a fat-finger
// operator credit, not a policy limit. The cap keeps a typo from minting a fortune.
const maxGrantCents int64 = 100 * 100 * 1000

// grantTag maps a grant source to the commerce deposit Tags that billing/bucket
// DepositKind classifies into Credit (trial) vs Prepaid (real money). Default
// (empty/unknown/"trial") is the non-cash Credit bucket — a staff comp is never
// silently minted as payout-able real money.
func grantTag(source string) (tag, normalized string) {
	if strings.ToLower(strings.TrimSpace(source)) == "prepaid" {
		return "admin-grant", "prepaid" // DepositKind: bare → Prepaid (real money)
	}
	return "grant:admin", "trial" // DepositKind: grant:* → Credit (non-cash trial)
}

// grantNote composes the deposit note from the operator's reason (bounded), so the
// commerce ledger row itself carries the justification alongside the audit trail.
func grantNote(c *zip.Ctx, reason string) string {
	r := strings.TrimSpace(reason)
	if len(r) > 200 {
		r = r[:200]
	}
	by := strings.TrimSpace(c.UserEmail())
	if by == "" {
		by = strings.TrimSpace(c.User())
	}
	if r == "" {
		r = "operator credit"
	}
	if by != "" {
		return fmt.Sprintf("Admin grant by %s: %s", by, r)
	}
	return "Admin grant: " + r
}

// grantRef derives the commerce idempotency ref for ONE grant attempt from its
// (subject, amount, currency, source) BOUND to a nonce — so a retried grant (a
// commit-then-timeout re-submit carrying the SAME operator nonce) dedupes at commerce
// (at-most-once), while two DISTINCT grants — even same org + amount — never collide.
// Binding the amount/currency/source into the hash means a nonce accidentally reused
// for a DIFFERENT grant still lands (a different key), so dedup can never silently DROP
// a legitimate distinct grant.
//
// The SUBJECT is hashed, not the org: two grants of the same amount to two members
// of one org are DIFFERENT grants, and hashing the org alone would make the second
// dedupe away against the first — a silently dropped credit.
//
// WITH NO OPERATOR NONCE THE NONCE IS FRESH, and that is the same additive deposit this
// returned an EMPTY key for before — stated as a value instead of as an absence. A fresh
// nonce cannot match anything, so dedup never fires and each attempt lands, which is
// exactly what an empty key bought. What it does NOT do is fabricate retry-stability: a
// content-only hash would wrongly dedupe two legitimate identical comps, and this is not
// that. The distinction the old comment drew — retry-stable versus grant-unique — is real
// and is preserved; only the spelling of "grant-unique" changed.
//
// It changed because a grant's ref must not depend on WHICH PROCESS holds the books. The
// co-resident ledger accepts an empty ref (no dedup); the plane REFUSES one, because
// plane.CreditIn.Ref is the exactly-once key an op that CREATES money cannot do without.
// Answering that question two ways, one per transport, is how the same grant comes to
// have two identities. One rule, both paths — and every grant row now carries a citable
// ref rather than an empty one.
func grantRef(c *zip.Ctx, subject, currency, source string, amountCents int64) string {
	nonce := strings.TrimSpace(c.Header("Idempotency-Key"))
	if nonce == "" {
		nonce = strings.TrimSpace(c.Header("X-Idempotency-Key"))
	}
	if nonce == "" {
		nonce = rand.Text()
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		subject, strconv.FormatInt(amountCents, 10), currency, source, nonce,
	}, "|")))
	return "grant-" + hex.EncodeToString(sum[:])
}

// GrantResult is what a credit grant DID — the receipt both grant ops answer with.
type GrantResult struct {
	// Org is the tenant whose ledger was credited.
	Org string `json:"org"`
	// Subject is the ACCOUNT the credit landed on inside that ledger: the org slug for
	// a pooled org, "<org>/<name>" for a member of a per-member one. It is echoed
	// because the operator does not choose it — account.Payer does — so naming a
	// member of a pooled org credits the pool and the receipt has to say so.
	Subject string `json:"subject"`
	// GrantedCents is the amount actually credited.
	GrantedCents int64 `json:"grantedCents"`
	// Currency is the lower-cased ISO code the grant was denominated in.
	Currency string `json:"currency"`
	// Source is the money bucket: "trial" (non-cash comp) or "prepaid" (real money).
	Source string `json:"source"`
	// BalanceCents is the account balance AFTER the grant, in whole cents.
	BalanceCents int64 `json:"balanceCents"`
	// BalanceExact is that same balance at full 18-decimal precision, so a sub-cent
	// debit is visible rather than rounded away.
	BalanceExact string `json:"balanceExact"`
	// TransactionID is the ledger entry id, for reconciliation against commerce.
	TransactionID string `json:"transactionId"`
}

// GrantOut is the envelope of every credit-grant op. There is one shape because there is
// ONE credit-write path.
type GrantOut struct {
	// Status is "ok" or "error". Error means NO money moved: every refusal — a
	// non-positive amount, one over the per-grant cap, an unknown org, a deployment with
	// no durable audit store — is decided before the ledger is written.
	Status string `json:"status"`
	// Msg is why the grant was refused, and is empty on success. Two refusals also carry
	// a non-200 HTTP status alongside this envelope: 404 for an unknown org, 503 when
	// there is nowhere durable to record the grant.
	Msg string `json:"msg"`
	// Data is the receipt: the account credited, the amount, and the balance after.
	// Null exactly when Status is "error".
	Data *GrantResult `json:"data"`
}

// ApplyGrant validates the amount + target org, deposits into the org's commerce ledger
// (trial vs prepaid by source), and records the tamper-evident audit row. One path, one
// way to grant.
//
// Two refusals carry a NON-200 status, set on c before the envelope is returned: an
// unknown org is 404, and a deployment with no durable audit store is 503. Both keep the
// envelope body — the status is the addition, not a different contract.
func ApplyGrant(s *cloud.Service[State], c *zip.Ctx, org string, req CreditRequest) (*GrantOut, error) {
	ctx := c.Context()
	cr := CallerCreds(c)
	if req.AmountCents <= 0 {
		return &GrantOut{Status: Err, Msg: "amountCents must be positive"}, nil
	}
	if req.AmountCents > maxGrantCents {
		return &GrantOut{Status: Err, Msg: fmt.Sprintf("amountCents exceeds the %d-cent per-grant cap", maxGrantCents)}, nil
	}
	currency := strings.ToLower(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = "usd"
	}

	// Validate the target is a REAL org (never mint an orphan wallet on a typo).
	o, err := FindOrg(s, ctx, cr, org)
	if err != nil {
		return &GrantOut{Status: Err, Msg: err.Error()}, nil
	}
	if o == nil {
		c.Status(404)
		return &GrantOut{Status: Err, Msg: "customer not found"}, nil
	}

	// FAIL-CLOSED durability (SOC2 AU-2/AU-5): a credit grant moves REAL money and MUST
	// leave a durable, tamper-evident record. If this deployment has no audit store to
	// record into, REFUSE the grant BEFORE any money moves — never an unaudited money
	// move. A nil store is not a production state: cloud requires a persistent data dir
	// for the trail (audit_serve.go), and nil arises only from the explicit
	// CLOUD_AUDIT_DISABLED dev opt-out, on which moving money is not a supported op.
	if s.State.AuditStore == nil {
		c.Status(503)
		return &GrantOut{Status: Err, Msg: "grant refused: no durable audit store is configured on this deployment; a credit grant must be recorded before money moves"}, nil
	}

	// Resolve the ADDRESS the grant lands at — the same rule (account.Payer) the
	// spend gate resolves the payer with, so the credit and the spend it funds name
	// one wallet. Empty req.User is the org; a named member of a POOLED org still
	// resolves to that org's pool, because that is the account their requests will
	// be gated on. A refusal here means the name could not be turned into an address
	// (a "/" in it would silently address something else), never a fallback.
	w := principal.PayerFor(org, req.User)
	if w.Zero() {
		return &GrantOut{Status: Err, Msg: "user must be a bare IAM username (no '/')"}, nil
	}
	subject := w.Subject()

	tag, source := grantTag(req.Source)
	notes := grantNote(c, req.Reason)

	// ONE credit money-move: the co-resident native finance wallet is preferred (the ai
	// prepaid gate + the edge meter read/debit THAT wallet, so a grant MUST land there),
	// with the commerce HTTP deposit as the split-deploy fallback. Both return the
	// pre-balance (recorded even on failure), the entry/transaction id, and the post-balance,
	// so the audit + response below are one shape regardless of which path moved the money.
	// w.Org(), not the raw path/body org: both halves of the address come from ONE
	// resolved Account, so the ledger a deposit opens can never disagree in case with
	// the subject written into it.
	before, txID, after, afterExact, derr := grantDeposit(c, w.Org(), subject, currency, notes, tag, source, req.AmountCents)
	if derr != nil {
		// The grant did not land — record the FAILED attempt (accountability), then
		// surface the error. Never report a grant that failed as success.
		EmitAudit(s, c, "admin.customer.credit", "credit", org,
			map[string]any{"balanceCents": before},
			map[string]any{"amountCents": req.AmountCents, "currency": currency, "reason": req.Reason, "source": source, "subject": subject, "error": derr.Error()},
			audit.Outcome{Result: "error", Status: 200, Reason: "grant failed"})
		return &GrantOut{Status: Err, Msg: "grant failed: " + derr.Error()}, nil
	}

	EmitAudit(s, c, "admin.customer.credit", "credit", org,
		map[string]any{"balanceCents": before},
		map[string]any{"balanceCents": after, "grantedCents": req.AmountCents, "currency": currency, "reason": req.Reason, "source": source, "subject": subject, "transactionId": txID},
		audit.Outcome{Result: "success", Status: 200})

	return &GrantOut{Status: OK, Data: &GrantResult{
		Org:           org,
		Subject:       subject,
		GrantedCents:  req.AmountCents,
		Currency:      currency,
		Source:        source,
		BalanceCents:  after,
		BalanceExact:  afterExact,
		TransactionID: txID,
	}}, nil
}

// grantDeposit performs the ONE credit money-move for a grant. It prefers the co-resident
// native finance wallet — the ai prepaid gate and the edge meter read/debit THAT wallet,
// so an admin grant must credit it — and falls back to the commerce HTTP deposit only when
// no finance ledger is co-resident (a split deploy). It returns the pre-balance (so
// ApplyGrant can audit even a FAILED attempt), the entry/transaction id, and the
// post-balance, so the audit + response are one shape regardless of which path moved the
// money.
//
// subject is the ACCOUNT within org's ledger, already resolved by account.Payer: the org
// slug for a pooled org, "<org>/<name>" for a member of a per-member one. Every balance
// read here uses it too — reading the pool around a member's credit would report a
// before/after that never moved and audit a lie.
//
// BOTH PATHS ADDRESS THE SUBJECT. The split-deploy leg used to refuse a member-addressed
// grant, because commerce's HTTP deposit is org-keyed and crediting the pool instead would
// have put the money where that member cannot spend it. plane.CreditIn carries the subject,
// so that refusal became a false negative and is gone: a member of a per-member org is
// credited at their own account whichever process holds the books.
func grantDeposit(c *zip.Ctx, org, subject, currency, notes, tag, source string, amountCents int64) (before int64, txID string, after int64, afterExact string, err error) {
	ctx := c.Context()
	// ONE credit path: prefer the in-proc commerce credit ledger (creditledger) — the
	// SAME injected ledger adapter commerce's POST /v1/billing/credit mints through
	// and the ai prepaid gate reads. An admin grant and a self-serve credit thus move
	// money the ONE way, into the ONE ledger; the admin path no longer carries its own
	// parallel finance.Deposit. The operator-nonce idempotency key rides through so a
	// retried grant dedupes (finance dedups on Ref). Before/after balances are read from
	// the SAME co-resident finance ledger for the audit trail (exact, sub-cent visible).
	if led := creditledger.Get(); led != nil {
		if fin := finance.Current(); fin != nil {
			if bal, berr := fin.Balance(ctx, org, subject, currency, false); berr == nil {
				before = bal.Cents()
			}
		}
		id, balCents, cerr := led.Credit(ctx, creditledger.CreditInput{
			Org:            org,
			Subject:        subject,
			Currency:       currency,
			Reason:         notes,
			Tag:            tag,
			IdempotencyKey: grantRef(c, subject, currency, source, amountCents),
			AmountCents:    amountCents,
		})
		if cerr != nil {
			return before, "", before, "", cerr
		}
		after = balCents
		if fin := finance.Current(); fin != nil {
			if bal, berr := fin.Balance(ctx, org, subject, currency, false); berr == nil {
				afterExact = bal.AttoString() // afterExact = the EXACT balance (sub-cent visible)
			}
		}
		return before, id, after, afterExact, nil
	}
	// Split deploy: the credit ledger is not in THIS process, which is the ORDINARY case
	// and not a gap. Apps are their own binaries, so commerce.Use — and with it the
	// injected credit ledger and finance.Current — runs in the commerce process alone;
	// creditledger.Get() above is permanently nil in admin's. So ASK the process that
	// owns the books, by name, over the internal plane.
	//
	// THIS IS THE WHOLE DEFECT THIS LEG USED TO BE. It called the admin commerce HTTP
	// client, whose base URL comes from CLOUD_COMMERCE_HTTP_URL or from an in-process
	// handler this binary does not publish — neither exists in the admin process — so
	// Client.Ready() was false and every grant answered "commerce not configured" for a
	// ledger one socket away. Pointing that env var at svc/commerce would not have fixed
	// it either: that Service is an alias back to this same pod's public edge, so the
	// deposit would re-enter the binary it left, which is the self-re-entry that killed
	// the billing gate (plane/commerce/commerce.go). A call BY NAME cannot express it.
	//
	// As(c, org) delegates the SuperAdmin the gate already validated and points it at the
	// tenant being credited. The principal travels WHOLE and commerce re-checks it; only
	// the tenant is re-pointed. plane.CreditIn has no org field at all, so the credited
	// org rides the caller and there is no argument a caller could name another tenant's
	// books with — the isolation is structural, not a check anyone has to remember.
	cctx := cloud.As(c, org)
	before, _ = planeBalance(cctx, c, subject, currency)
	cred, derr := commercepeer.FinanceCredit(cctx, &plane.CreditIn{
		Subject: subject,
		// The EXACT decimal, not the minor unit. commerce deposits what it parses,
		// so a grant crosses at full precision and cannot be rounded in transit.
		Amount: plane.Amount(money.FromCents(amountCents).Unwrap()),
		Ref:    grantRef(c, subject, currency, source, amountCents),
		Notes:  notes,
		Tags:   tag,
	})
	if derr != nil {
		// Every error surfaces, including ErrNoPeer. Ask names ErrNoPeer as the only
		// error a caller may read as "fall back", and a grant has nowhere to fall back
		// TO: a deployment that runs no commerce cannot credit anybody, and reporting
		// that as anything other than a failed grant would be the silent success this
		// whole path exists to stop.
		return before, "", before, "", fmt.Errorf("credit %s over the commerce plane: %w", subject, derr)
	}
	if cred == nil {
		// A void reply is not a completed credit. Nothing was written that we know of.
		return before, "", before, "", fmt.Errorf("credit %s: commerce answered nothing", subject)
	}
	// The money HAS moved. The balances below are the receipt's, not the grant's, so a
	// failed read here is reported and never turned into a failed grant — auditing a
	// landed credit as an error would be worse than an unknown balance.
	after, afterExact = planeBalance(cctx, c, subject, currency)
	return before, cred.ID, after, afterExact, nil
}

// planeBalance reads one subject's balance from the process that owns the books, in the
// two renderings the grant receipt carries: whole cents, and the exact 18-decimal value.
//
// BEST-EFFORT, like the co-resident leg's own balance reads, because these figures are the
// RECEIPT — they do not decide anything and nothing is spent against them. A failure is
// logged rather than swallowed, so a zero on a successful grant is diagnosable instead of
// silent.
//
// RoundMinor, not FloorMinor: this is a figure someone READS, and the co-resident leg
// renders the same balance with money.Amount.Cents(), which rounds half-away-from-zero. One
// grant must report one number regardless of which process holds the books, so the two legs
// round the same way. (A balance a gate SPENDS against floors instead — see
// plane.Money.FloorMinor.)
func planeBalance(cctx context.Context, c *zip.Ctx, subject, currency string) (cents int64, exact string) {
	bal, err := commercepeer.FinanceBalance(cctx, &plane.BalanceIn{Subject: subject, Currency: currency})
	if err != nil || bal == nil {
		c.Log().Warn("admin: grant receipt balance unread (the credit itself is unaffected)",
			"subject", subject, "currency", currency, "err", err)
		return 0, ""
	}
	if cents, err = bal.Amount.RoundMinor(); err != nil {
		c.Log().Warn("admin: grant receipt balance unreadable", "subject", subject, "err", err)
		return 0, ""
	}
	amt, err := bal.Amount.Parse()
	if err != nil {
		return cents, ""
	}
	// Take the DECIMAL, never Minor(): the wire's currency declares 2 decimals, so
	// Minor() would answer cents and the exact rendering would understate by 10^16.
	// money.FromDecimal is the one carry into the 18-decimal credit unit.
	return cents, money.FromDecimal(amt.Decimal()).AttoString()
}

// EmitAudit writes ONE compliance record for a management action to cloud's
// tamper-evident trail: who (the validated SuperAdmin from the sanitized identity —
// the gate already proved it), what (action + resource), the redacted before/after, and
// the outcome. This is the "before/after on a config-affecting change" the request-level
// middleware record cannot carry (it never reads bodies). Best-effort: a failure here is
// logged loud, never silent, and never double-fails the response. A nil store
// (unconfigured deployment) is a no-op, like the middleware.
func EmitAudit(s *cloud.Service[State], c *zip.Ctx, action, resType, resID string, before, after any, outcome audit.Outcome) {
	if s.State.AuditStore == nil {
		return
	}
	org, _ := principal.Org(c)
	rec := audit.Record{
		Actor:     audit.Actor{Org: org, Sub: strings.TrimSpace(c.User()), Email: strings.TrimSpace(c.UserEmail())},
		Action:    action,
		Resource:  audit.Resource{Type: resType, ID: resID},
		Auth:      audit.AuthContext{Method: "jwt", IsAdmin: c.IsAdmin()},
		Outcome:   outcome,
		UserAgent: c.Header("User-Agent"),
		RequestID: c.RequestID(),
		Method:    c.Method(),
		Path:      c.Path(),
		Before:    audit.Redact(mustJSON(before)),
		After:     audit.Redact(mustJSON(after)),
	}
	if _, err := s.State.AuditStore.Append(c.Context(), rec); err != nil {
		c.Log().Error("admin: audit emit failed (request-level record still applies)",
			"action", action, "resource", resType, "id", resID, "err", err)
	}
}

// mustJSON marshals v to raw JSON for the audit before/after, returning an empty object
// on the (unexpected) marshal error rather than panicking — a metadata diff must never
// crash a money/access action.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
