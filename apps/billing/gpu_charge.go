package billing

// gpu_charge.go is the ONE money WRITE on the customer billing surface: POST
// /v1/billing/gpu/charge, the prepay-only, card-required GPU debit.
//
// WHY IT IS NOT A PLAIN PROXY ANY MORE. It was, and it was not idempotent: the caller's
// `requestId` rode the body into commerce's transaction metadata, where it deduplicated
// NOTHING. Two identical posts were two debits — a client retry, a proxy replay or a
// double-clicked launch button charged a customer twice — and the endpoint's own
// description said so out loud rather than fixing it. Every other debit in this fleet is
// keyed: the edge meter and x402 both hand the ledger a RequestID, apps/finance dedups
// on it inside the same transaction as the insert, so a retry debits AT MOST ONCE. This
// is that same key, on this endpoint at last.
//
// WHICH WALLET. Co-resident, the customer's prepaid money is cloud's OWN finance ledger
// (the per-org double-entry file balance.go reads, the ai gate admits against, and the
// meter debits). commerce's transaction store is NOT that wallet in this binary — it is
// left empty — so a GPU debit written only there moved nothing a customer or a gate can
// see. The debit therefore lands where the money is, keyed on the caller's requestId.
//
// WHAT DID NOT CHANGE. Both gates stay, both fail CLOSED: a chargeable card must be on
// file (402 card_required — read from commerce's card store, the only place cards live),
// and prepaid alone must cover the charge (402 insufficient_prepaid — read from the
// ledger the debit will post to, so the gate and the debit can never address two
// wallets). A gate that cannot be READ refuses with 502; unknown is never permission.
// The subject stays pinned server-side to the caller's own org, so a forged body can
// never charge another tenant.
//
// SPLIT DEPLOY. With no ledger in this process there is nothing here to key on, and the
// request falls back to the commerce proxy exactly as before — the same fallback
// balance() makes, and the caller's idempotency key is forwarded upstream as
// X-Idempotency-Key so the guard is the upstream's where it cannot be ours.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// gpuChargeRequest is the charge a caller asks for. It mirrors commerce's own
// gpuChargeRequest field for field, because the split-deploy fallback forwards this same
// body — one shape, two paths.
//
// RequestID is the IDEMPOTENCY KEY and it is a field the wire already had. Nothing new
// was invented for this and nothing is derived from the charge's shape: a derived key
// either collapses two genuine launches of the same size into one debit, or expires and
// stops protecting the retry it exists for. Supplied, the charge is exactly-once;
// omitted, the ledger takes a fresh ref and the debit is additive — the SAME rule
// DepositInput.Ref has always stated, so the money plane has one answer to a missing key
// rather than two.
type gpuChargeRequest struct {
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency,omitempty"`
	Notes       string `json:"notes,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
	Tag         string `json:"tag,omitempty"`
}

// gpuChargeResponse is the 201 body, byte-compatible with commerce's so the launch UI
// reads one shape on either path.
type gpuChargeResponse struct {
	TransactionID  string `json:"transactionId"`
	User           string `json:"user"`
	Amount         int64  `json:"amount"`
	Currency       string `json:"currency"`
	Tags           string `json:"tags"`
	PrepaidBalance int64  `json:"prepaidBalance"`
	Status         string `json:"status"`
}

// usageRecorder is the ledger capability this endpoint needs beyond the narrow
// types.FinanceClient: the debit with its idempotency ANSWERED, so a replay can be told
// apart from a first charge without a second store to ask. Resolved by assertion, the
// same widening the co-resident usage and entry reads use.
type usageRecorder interface {
	RecordUsageOnce(ctx context.Context, in types.UsageInput) (entryID string, posted bool, err error)
}

// gpuCharge debits the caller's own org for a GPU, at most once per requestId.
func gpuCharge(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		// A customer's OWN GPU charge — never admin-gate it; an absent identity is a
		// true "not signed in" (401), matching usage/balance.
		return zip.ErrUnauthorized("sign in to charge a GPU")
	}
	if !s.State.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}

	recorder, coResident := finance.Current().(usageRecorder)
	if !coResident {
		return proxyGPUCharge(s, c, org) // split deploy: no ledger here to key on
	}

	var req gpuChargeRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return zip.ErrBadRequest("gpu-charge: invalid request body")
	}
	if req.AmountCents <= 0 {
		return zip.ErrBadRequest("gpu-charge: amountCents must be positive")
	}
	currency := strings.ToLower(strings.TrimSpace(req.Currency))
	if currency == "" {
		currency = "usd"
	}
	// The payer is NOT a field: whatever the body said, the wallet is the caller's own,
	// resolved by the ONE rule the gate resolves it with.
	subject := subjectFor(c, org)
	amount := money.FromCents(req.AmountCents) // exact — cents in, exact 18-decimal out

	// Gate 1: a chargeable card MUST be on file. A GPU is real money, and cards live in
	// commerce's store, so this is the one fact still read from upstream.
	switch card, err := cardOnFile(s, c, org, subject); {
	case err != nil:
		s.Log.Warn("gpu-charge: card gate unreadable", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	case !card:
		return gpuRefusal(c, "card_required", "Add a card on file before launching a GPU", nil)
	}

	// Gate 2: PREPAID alone must cover the charge, read from the wallet this debit will
	// post to. Credits are never consulted — the finance wallet is a single prepaid
	// balance, so a GPU can draw nothing a grant put there that spend has not left.
	available, _, err := availableCents(c.Context(), org, subject)
	if err != nil {
		s.Log.Warn("gpu-charge: prepaid gate unreadable", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	if available < req.AmountCents {
		return gpuRefusal(c, "insufficient_prepaid",
			"Add prepaid funds (GPUs are billed from prepaid real money, not credits)",
			map[string]any{"prepaidAvailable": available, "requiredCents": req.AmountCents})
	}

	// The debit, keyed on the caller's requestId. A replay finds the original entry
	// inside the ledger's own transaction, moves no money, and answers the SAME
	// transaction id — so a retrying client can never conclude it bought two GPUs.
	tag := gpuTag(req.Tag)
	entryID, _, err := recorder.RecordUsageOnce(c.Context(), types.UsageInput{
		Org: org, Subject: subject, Amount: amount, Currency: currency,
		Model: firstNonEmpty(strings.TrimSpace(req.Notes), tag), Provider: "gpu",
		Service: tag, RequestID: req.RequestID,
	})
	if err != nil {
		s.Log.Error("gpu-charge: ledger debit failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}

	after, _, err := availableCents(c.Context(), org, subject)
	if err != nil {
		// The DEBIT landed; only the read-back did not. Report the charge rather than
		// failing it — a 502 here would send the client into a retry that (correctly)
		// changes nothing, against a wallet that has already paid.
		s.Log.Warn("gpu-charge: balance read-back failed after a committed debit", "org", org, "err", err)
	}
	c.SetHeader("Content-Type", "application/json")
	// A money write's result must never be cached by the browser or an intermediary.
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusCreated, gpuChargeResponse{
		TransactionID: entryID, User: subject, Amount: req.AmountCents,
		Currency: currency, Tags: tag, PrepaidBalance: after, Status: "ok",
	})
}

// gpuRefusal answers a money verdict the launch UI renders as a remedy: 402 with the
// code and the facts behind it, never a 500. Same body shape commerce's gates answer, so
// the client reads one shape on either path.
func gpuRefusal(c *zip.Ctx, code, message string, extra map[string]any) error {
	err := map[string]any{"code": code, "message": message}
	for k, v := range extra {
		err[k] = v
	}
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusPaymentRequired, map[string]any{"error": err})
}

// cardOnFile reports whether the caller's wallet has a CHARGEABLE card vaulted — a card
// descriptor or a vaulted provider reference, the same rule commerce's own gate applies.
// An upstream that answers non-2xx or cannot be reached is an ERROR, never a false: "no
// card" and "could not ask" must not look alike on a gate that refuses money.
func cardOnFile(s *cloud.Service[state], c *zip.Ctx, org, subject string) (bool, error) {
	body, status, err := s.State.commerce.get(c.Context(), "/v1/billing/portal/methods", org, financeSubject(subject, nil))
	if err != nil {
		return false, err
	}
	if status < 200 || status >= 300 {
		return false, zip.Errorf(http.StatusBadGateway, "billing upstream status %d", status)
	}
	for _, raw := range arrayFrom(body, "paymentMethods", "payment_methods", "methods", "data", "rows") {
		var pm commercePaymentMethod
		if json.Unmarshal(raw, &pm) != nil {
			continue
		}
		if pm.Last4 != "" || pm.Card.Last4 != "" || pm.Card.LastFour != "" || strings.TrimSpace(pm.ProviderRef) != "" {
			return true, nil
		}
	}
	return false, nil
}

// gpuTag forces a caller-supplied tag into the gpu bucket, the same normalization
// commerce applies, so a charge on this endpoint is always gpu-classified whichever path
// served it. A blank or non-gpu tag becomes "gpu"; an explicit gpu tag is preserved.
func gpuTag(tag string) string {
	t := strings.TrimSpace(strings.ToLower(tag))
	switch {
	case t == "":
		return "gpu"
	case t == "gpu", strings.HasPrefix(t, "gpu-"), strings.HasPrefix(t, "gpu:"):
		return t
	default:
		return "gpu-" + t
	}
}

// proxyGPUCharge is the split-deploy path: no ledger in this process, so the charge is
// forwarded to the commerce that owns one, byte for byte as it always was. The billing
// SUBJECT is pinned server-side to the caller's OWN org and commerce's status forwards
// VERBATIM (201 ok / 402 card_required|insufficient_prepaid), so the launch UI renders
// the exact remedy and a money verdict is never 500-masked.
//
// The caller's key rides along as X-Idempotency-Key — the header commerce's own money
// moves guard themselves on — because the guarantee has to be made where the write
// happens, and here that is not this process.
func proxyGPUCharge(s *cloud.Service[state], c *zip.Ctx, org string) error {
	body := pinSubjectBody(c.Body(), org)
	var req gpuChargeRequest
	_ = json.Unmarshal(c.Body(), &req)
	respBody, status, err := s.State.commerce.post(c.Context(), "/v1/billing/gpu/charge", org, body, req.RequestID)
	if err != nil {
		s.Log.Warn("commerce gpu-charge failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	c.SetHeader("Content-Type", "application/json")
	c.SetHeader("Cache-Control", "no-store")
	return c.Bytes(status, respBody)
}
