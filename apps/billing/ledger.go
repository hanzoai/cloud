package billing

// ledger.go serves GET /v1/billing/ledger — the org's own postings, signed, over
// the window ?range= names.
//
// It is the widest projection of the one ledger this package reads: a DEPOSIT
// credits the wallet and every other posting debits it, so the deposit half is
// what a credits page shows and the debit half is what a usage page rolls up.
// One read, one vocabulary, so no two readings of a customer's money can
// contradict each other.
//
// WHY IT LIVES IN THE BILLING PACKAGE. It does not add a billing system; it
// projects the one that already exists. The wallet — prepaid balance, the
// deposit/withdraw ledger, the saved cards — is commerce's store, and this
// package already owns the per-org subject pinning that keeps a read inside the
// caller's own org (the whole tenant-isolation argument in billing.go).
//
// It answered at /v1/finance/ledger beside five siblings, and the siblings are
// gone rather than moved. /v1/finance was a second spelling of one capability:
// balance and usage were this package's own reads under another name, and
// credits, invoices and payment-methods were addresses commerce already serves
// under /v1/billing. HIP-0139 §7 closes a shared address by fold, never by
// alias, so a fold onto an address somebody already answers is a deletion — the
// weave refuses one METHOD+path declared twice, and it is right to.
//
// TENANT ISOLATION. Identical to the /v1/billing/* reads: the org is the
// VALIDATED IAM owner (principal.OrgFrom, parked by the composer's
// cloud.Bridge), never a client-supplied subject or org, so a caller reads ONLY
// its own org's wallet and can never widen scope.
//
// SHAPE. It is a TYPED op where the rest of /v1/billing/* stays raw, and that is
// the difference worth knowing: the raw routes serve commerce's own bytes, body
// and status; this one owns its shape and can therefore declare it, so the
// document, the MCP tool, the CLI command and the SDK method all follow from one
// declaration.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	commercepeer "github.com/hanzoai/cloud/plane/commerce"
	"github.com/zap-proto/zip"
)

// mountLedger registers the ledger read as a typed op. Called from routes, so it
// ships with the rest of the customer money surface.
func mountLedger(app cloud.Router, o ops) {
	zip.Get(cloud.ZipApp(app), "/v1/billing/ledger", o.ledger,
		zip.WithResponseHeader("Cache-Control")) // per-org double-entry postings over ?range=
}

// ── the contract (typed, USD cents, matching @hanzo/finance-ui types.ts) ──

// noStore is the Cache-Control every per-tenant money answer declares: the
// number is a live wallet read, so neither the browser nor an intermediary may
// replay it. Each finance Out states it via zip.HeaderCoder and each
// registration declares it via zip.WithResponseHeader, so the directive is part
// of the published contract — visible to the document, the SDKs and the tool
// schema — rather than a slot some handler writes on the way out.
func noStore() map[string]string { return map[string]string{"Cache-Control": "no-store"} }

// financeLedgerEntry is one posting of GET /v1/billing/ledger?range= — a signed move
// on the org's wallet (deposit positive, withdraw negative).
type financeLedgerEntry struct {
	ID           string `json:"id"`
	Date         string `json:"date,omitempty"`
	Account      string `json:"account,omitempty"`
	Description  string `json:"description,omitempty"`
	Cents        int64  `json:"cents"`
	Currency     string `json:"currency"`
	BalanceCents *int64 `json:"balanceCents,omitempty"`
}

// postings is the GET /v1/billing/ledger answer — a bare array on the body.
type postings []financeLedgerEntry

func (postings) ResponseHeaders() map[string]string { return noStore() }

// window narrows a finance read to its span.
type window struct {
	// Range is the window: 24h, 7d, 30d or 90d. Anything else — including
	// absent — is 30d, so a typo silently widens the window to a month rather
	// than failing.
	Range string `json:"range"`
}

// ── commerce wire shapes (only the fields we project) ──

// commerceBalance is commerce GET /v1/billing/balance ({balance,holds,available} cents).
//
// Account names WHICH wallet the cents belong to — the billing subject this binary
// resolved with the one rule (account.Payer), the same subject the ai spend gate debits
// and a top-up credits. It is cloud-resolved, never decoded from upstream (commerce does
// not send it), so it is empty on the split-deploy proxy path and omitted from the JSON
// there rather than rendered as a blank account.
//
// It exists because "which account am I funding?" had no answer a client could trust: a
// browser could only guess by decoding its own token, and a guess that disagrees with the
// server is exactly how money lands in an account the gate never reads. Echoing the
// resolved subject lets a checkout SHOW the payer before the customer pays, from the same
// resolution that will actually be credited.
type commerceBalance struct {
	Balance   int64  `json:"balance"`
	Holds     int64  `json:"holds"`
	Available int64  `json:"available"`
	Account   string `json:"account,omitempty"`
}

// commerceTxn is one ledger row as the three projections below read it. Amount is the
// magnitude in cents. This ONE row shape backs credits, usage, and ledger — a single
// read projected three ways, never a second meter.
//
// Kind is the ONE vocabulary (apps/finance owns it), never a string this file spells
// for itself. Two wires deliver these rows — the internal plane, carrying the ledger's
// own kinds, and commerce's S2S HTTP, carrying its own `deposit`/`withdraw` — and each
// is translated into finance.Kind at ITS OWN boundary, so the projections classify on
// one typed value and can never be handed a spelling they silently skip. They were:
// the reader matched commerce's words against the ledger's kinds, so on the peer path
// credits rendered empty, usage totalled 0, and a customer's own grant signed negative.
type commerceTxn struct {
	// Type is commerce's raw HTTP wire word, decoded on the S2S path only. It is
	// translated into Kind by commerceKind and never classified on directly.
	Type      string       `json:"type"`
	Kind      finance.Kind `json:"-"`
	ID        string       `json:"id"`
	Amount    int64        `json:"amount"`
	Currency  string       `json:"currency"`
	Tags      string       `json:"tags"`
	Notes     string       `json:"notes"`
	CreatedAt string       `json:"createdAt"`
}

// commerceKind translates commerce's OWN HTTP wire vocabulary into the ledger's. It is
// the one place those two words appear, because they belong to an upstream this fleet
// does not own; every reader downstream of it sees finance.Kind and nothing else.
func commerceKind(wire string) finance.Kind {
	switch strings.ToLower(strings.TrimSpace(wire)) {
	case "deposit":
		return finance.KindDeposit
	case "withdraw":
		return finance.KindUsage
	default:
		return finance.KindUnknown
	}
}

// kindLabel is the plain word a customer reads for a posting that carries neither
// notes nor tags. Rendering, not classification — which is why it lives here and not
// beside the kinds.
func kindLabel(k finance.Kind) string {
	if k == finance.KindDeposit {
		return "Credit"
	}
	return "Usage"
}

// ── the ops ──

// ledger answers the org's own postings inside `range=`, each as a signed
// entry: a DEPOSIT CREDITS the wallet (positive, account `credits:<org>`) and
// every other posting DEBITS it (negative, account `usage:<org>`), described by
// its notes or its tags. The sign is the posting's own meaning, read through ONE
// vocabulary shared with the ledger that wrote it — a reader with its own
// spelling for `deposit` rendered a customer's grant as a charge.
//
// This is the closest projection of the truth. The org's double-entry postings
// are the source of record — balanced, only ever appended, one file per org —
// and this lane is that list, wider than either half of it: the deposits are the
// grants /v1/billing/credits lists and the debits are the spend /v1/billing/usage
// rolls up. It answers 503 where this deployment runs no ledger, rather than
// reporting an empty wallet.
//
// A row whose timestamp will not parse is KEPT rather than dropped — a malformed
// date must show up in a money list, not vanish from it. `balanceCents` is
// omitted: these are MOVEMENTS, and the standing balance is /v1/billing/balance.
//
// Cents are ROUNDED from the ledger's exact 18-decimal USD. Scoped to the
// caller's own org, where the org's ledger file is the tenant boundary; 401
// without a validated principal.
//
// Example: {"range": "30d"}
func (o ops) ledger(ctx context.Context, in *window) (*postings, error) {
	org, subject, err := payer(ctx)
	if err != nil {
		return nil, err
	}
	if !o.s.State.commerce.configured() {
		return nil, zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	txns, err := financeTxns(o.s, ctx, org, subject)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().Add(-rangeWindow(in.Range))
	rows := make(postings, 0, len(txns))
	for _, t := range txns {
		if ts, perr := parseFinanceTime(t.CreatedAt); perr == nil && ts.Before(cutoff) {
			continue
		}
		deposit := t.Kind == finance.KindDeposit
		cents := abs64(t.Amount)
		account := "usage:" + org
		if deposit {
			account = "credits:" + org
		} else {
			cents = -cents
		}
		rows = append(rows, financeLedgerEntry{
			ID:          cmp.Or(t.ID, "entry"),
			Date:        t.CreatedAt,
			Account:     account,
			Description: cmp.Or(strings.TrimSpace(t.Notes), strings.TrimSpace(t.Tags), kindLabel(t.Kind)),
			Cents:       cents,
			Currency:    cmp.Or(strings.ToLower(t.Currency), "usd"),
		})
	}
	return &rows, nil
}

// ── finance helpers ──

// THE LEDGER ANSWERS FOR THE SUBJECT THE BALANCE ANSWERS FOR. org names the books
// and subject names the wallet inside them — the pair balance.go resolves through
// principal.Subject, handed on unchanged — so a customer's movements and their
// spendable total describe one account. Where the payer IS the org the subject
// resolves to the org and the answer is the pool's; where the payer is a person
// it is that person's. The org rides the caller (cloud.For) and the subject rides
// the argument: an org in the payload would let a caller name another tenant's
// books, while a subject can only address a wallet inside its own.
// financeTxns reads the org's commerce ledger ONCE (the single transactions read the
// credits/usage/ledger projections share). Tolerates the wrapped {transactions:[…]}
// shape and a bare array.
func financeTxns(s *cloud.Service[state], ctx context.Context, org, subject string) ([]commerceTxn, error) {
	// The ledger's own entries, from the process that holds them. Credits, usage and
	// the ledger page are three projections of this one list, and all three answered
	// 501 from a process without the ledger — which is every process but commerce.
	peer, served, err := peerTxns(ctx, org, subject)
	if err != nil {
		s.Log.Warn("finance transactions read failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	if served {
		return peer, nil
	}
	// NOT served means ErrNoPeer, and ErrNoPeer means this deployment runs no
	// commerce at all (peerTxns returns served=true for every other failure). The
	// HTTP fallback that used to sit here could not answer that case: it dialled
	// CLOUD_COMMERCE_HTTP_URL, which production points at commerce.hanzo.svc:8001,
	// and that Service selects `app.kubernetes.io/name: cloud` on targetPort 8000 —
	// this pod's own public edge. So the call left the process, came back through
	// the front door, and re-entered the binary that had already said it has no
	// ledger. That re-entry is what the transport's maxDepth counter exists to
	// survive, and what killed the billing gate once.
	//
	// A ledger this fleet does not run is an outage, not an empty list.
	return nil, zip.Errorf(http.StatusServiceUnavailable, "billing ledger is not available on this deployment")
}

// classify is the S2S boundary: commerce's own wire words become the ONE vocabulary
// the projections read, ONCE, on the way in. Nothing downstream of it sees a raw type
// string, which is what makes a third spelling impossible to introduce quietly.
func classify(rows []commerceTxn) []commerceTxn {
	for i := range rows {
		rows[i].Kind = commerceKind(rows[i].Type)
	}
	return rows
}

// rangeWindow maps a finance range token to a duration; an absent/unknown range
// defaults to 30 days.
func rangeWindow(r string) time.Duration {
	switch strings.TrimSpace(r) {
	case "24h":
		return 24 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	case "90d":
		return 90 * 24 * time.Hour
	default: // "30d" and anything unrecognized
		return 30 * 24 * time.Hour
	}
}

// parseFinanceTime parses a commerce RFC3339 timestamp (with or without sub-seconds).
func parseFinanceTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// peerTxns reads the ledger over the internal plane. ok=false means this deployment
// runs no commerce, and the caller falls back to the configured commerce URL — the
// split deploy, which is a real shape and not a failure. A non-nil err is a REAL read
// failure: the peer is here and it did not answer, which is an outage and must reach
// the customer as one rather than as somebody else's ledger.
//
// Only the ROUTER may state absence (cloud.ErrNoPeer); it owns the manifest. Absence
// inferred from a failed call is how a dead peer became a phantom split deploy.
//
// The amount arrives as its exact 18-decimal integer and is flattened to cents HERE,
// at the boundary where commerceTxn is already a cents-shaped view. The wire keeps
// the precision so the day that view stops being cents-shaped, nothing upstream has
// to be re-plumbed to find it.
func peerTxns(ctx context.Context, org, subject string) ([]commerceTxn, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, txnsPeerTimeout)
	defer cancel()
	// The generated peer client, not three loose strings: it is this call with the
	// app name, the op name and the In/Out pair already fixed to each other, so
	// the compiler checks what only a running fleet could check here.
	reply, err := commercepeer.FinanceTxns(cloud.For(ctx, org), &plane.TxnsIn{Subject: subject})
	if err != nil {
		if errors.Is(err, cloud.ErrNoPeer) {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("transactions: commerce ledger read: %w", err)
	}
	if reply == nil {
		// A void reply is not an empty ledger. Nothing was read, so nothing is known.
		return nil, true, errors.New("transactions: commerce answered nothing")
	}
	out := make([]commerceTxn, 0, len(reply.Rows))
	for _, t := range reply.Rows {
		amt, perr := money.ParseUSD(t.Amount.Decimal)
		if perr != nil {
			// A total we cannot read exactly is not a total we report — and it is the
			// peer's answer that is wrong, not the peer that is absent.
			return nil, true, fmt.Errorf("transactions: %w", perr)
		}
		out = append(out, commerceTxn{
			ID: t.ID,
			// The peer boundary: the ledger's own kind, parsed back into the ONE
			// vocabulary. It arrives as text and stops being text here.
			Kind:      finance.ParseKind(t.Kind),
			Amount:    amt.Cents(),
			Currency:  "usd",
			Tags:      t.Ref,
			Notes:     t.Memo,
			CreatedAt: time.Unix(t.CreatedAt, 0).UTC().Format(time.RFC3339),
		})
	}
	return out, true, nil
}

// txnsPeerTimeout bounds the ledger read behind an interactive billing page.
const txnsPeerTimeout = 10 * time.Second
