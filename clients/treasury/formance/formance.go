// Package formance is the Formance Ledger adapter for the treasury: it satisfies
// ledger.Backend by posting the reserve fund's double-entry through a live Formance
// Ledger service (Postgres-backed, the production ledger of record) over its v2 HTTP
// API. Formance guarantees the double-entry + the overdraw guard — a transaction
// whose source (@fund:reserve) is underfunded is refused by Numscript source
// semantics (400 INSUFFICIENT_FUND), which this adapter maps to "not backed". We do
// NOT reimplement double-entry here; we drive Formance's.
//
// Integration shape (b) of the finance-stack plan: Formance Ledger runs as its own
// all-Go service against the prod Postgres (hanzo-sql); the unified cloud binary
// CLIENTS to it. Selecting it is one env var (FORMANCE_LEDGER_URL); absent it the
// treasury uses the native engine, so the reserve fund works offline and Formance is
// a drop-in upgrade to the ledger of record.
//
//	FORMANCE_LEDGER_URL     base URL of the Formance Ledger service (e.g.
//	                        http://ledger.finance.svc:8080)
//	FORMANCE_LEDGER_NAME    the ledger name to post into (default "hanzo-treasury")
//	FORMANCE_LEDGER_TOKEN   optional bearer token (client-credentials) for auth
//
// The Hanzo revenue-share POLICY is not a Formance concept (it is Hanzo config, not
// accounting), so it is held in the injected ledger.PolicyStore (the native Base
// store) regardless of which backend owns the journal.
package formance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/clients/treasury/ledger"
)

const (
	// externalAccount is Formance's built-in unbounded external money source — the
	// counter-account for accruals/seeds (money entering the fund).
	externalAccount = "world"
	// assetUSD is 2-decimal USD: amounts are minor units (cents), matching the
	// int64-cents the whole treasury speaks.
	assetUSD = "USD/2"
	// reserveAddress / payout prefix mirror the native chart of accounts EXACTLY, so
	// fund:reserve + payout:<program> read identically across backends.
	reserveAddress = ledger.AccountReserve
)

// knownPrograms are the growth loops whose payout sinks Snapshot rolls up. Adding a
// loop adds its program id here (the ONE place the Formance rollup enumerates them).
var knownPrograms = []string{"referral", "affiliate", "author"}

// Backend drives a Formance Ledger service. It satisfies ledger.Backend.
type Backend struct {
	base   string
	ledger string
	token  string
	http   *http.Client
	policy ledger.PolicyStore
}

// New builds the adapter. base/ledgerName must be non-empty (the caller selects this
// backend only when FORMANCE_LEDGER_URL is set); policy is the native config store
// for the revenue-share policy.
func New(base, ledgerName, token string, policy ledger.PolicyStore) *Backend {
	if ledgerName == "" {
		ledgerName = "hanzo-treasury"
	}
	return &Backend{
		base:   strings.TrimRight(strings.TrimSpace(base), "/"),
		ledger: ledgerName,
		token:  strings.TrimSpace(token),
		http:   &http.Client{Timeout: 20 * time.Second},
		policy: policy,
	}
}

func (b *Backend) Name() string { return "formance" }

// compile-time proof the Formance adapter satisfies the ledger-of-record port.
var _ ledger.Backend = (*Backend)(nil)

// ── policy (native config store; Formance does not model it) ─────────────────

func (b *Backend) Policy(ctx context.Context) (ledger.Policy, error) { return b.policy.Policy(ctx) }

func (b *Backend) SetPolicy(ctx context.Context, bps, now int64) (ledger.Policy, error) {
	if bps < 0 || bps > 10000 {
		return ledger.Policy{}, fmt.Errorf("revenueShareBps must be in [0,10000], got %d", bps)
	}
	p := ledger.Policy{RevenueShareBps: bps, UpdatedAt: now}
	if err := b.policy.SetPolicy(ctx, p); err != nil {
		return ledger.Policy{}, err
	}
	return p, nil
}

// ── journal (Formance) ───────────────────────────────────────────────────────

// Accrue posts the revenue-share for a period (world → fund:reserve), idempotent by
// the Formance transaction reference "accrual:<period>". A zero share is a no-op.
func (b *Backend) Accrue(ctx context.Context, period string, revenueCents, now int64) (ledger.Entry, bool, error) {
	pol, err := b.policy.Policy(ctx)
	if err != nil {
		return ledger.Entry{}, false, fmt.Errorf("policy: %w", err)
	}
	share := revenueCents * pol.RevenueShareBps / 10000
	if share <= 0 {
		return ledger.Entry{}, false, nil
	}
	ref := "accrual:" + period
	memo := fmt.Sprintf("revenue-share %d bps of %d cents (period %s)", pol.RevenueShareBps, revenueCents, period)
	return b.postTransfer(ctx, ledger.KindAccrual, "", ref, memo, externalAccount, reserveAddress, share, now)
}

// Seed posts a bootstrap capital injection (world → fund:reserve), idempotent by ref.
func (b *Backend) Seed(ctx context.Context, ref, memo string, amountCents, now int64) (ledger.Entry, bool, error) {
	if amountCents <= 0 {
		return ledger.Entry{}, false, ledger.ErrNonPositive
	}
	return b.postTransfer(ctx, ledger.KindSeed, "", ref, memo, externalAccount, reserveAddress, amountCents, now)
}

// DebitReserve posts a backed payout (fund:reserve → payout:<program>). Formance
// refuses it (400 INSUFFICIENT_FUND) when the reserve is underfunded — mapped to
// backed=false, nothing posted. A duplicate reference (409) is an idempotent replay
// → backed=true, created=false. This is the reserve overdraw guard, enforced by
// Formance's Numscript source semantics — not reimplemented here.
func (b *Backend) DebitReserve(ctx context.Context, program, ref, memo string, amountCents, now int64) (ledger.Entry, bool, bool, error) {
	if amountCents <= 0 {
		return ledger.Entry{}, false, false, ledger.ErrNonPositive
	}
	entry, created, err := b.postTransfer(ctx, ledger.KindPayout, program, ref, memo, reserveAddress, ledger.PayoutAccount(program), amountCents, now)
	if err != nil {
		if isInsufficientFunds(err) {
			return ledger.Entry{}, false, false, nil // not backed, no error
		}
		return ledger.Entry{}, false, false, err
	}
	return entry, true, created, nil
}

// postTransfer posts a single-posting transaction and maps Formance's outcome:
// 2xx → created; 409 CONFLICT (duplicate reference) → idempotent replay (created
// false); 400 INSUFFICIENT_FUND → surfaced as errInsufficientFunds for the caller to
// interpret.
func (b *Backend) postTransfer(ctx context.Context, kind, program, ref, memo, source, dest string, amountCents, now int64) (ledger.Entry, bool, error) {
	body, _ := json.Marshal(map[string]any{
		"postings": []map[string]any{{
			"amount":      amountCents,
			"asset":       assetUSD,
			"source":      source,
			"destination": dest,
		}},
		"reference": ref,
		"metadata":  map[string]string{"kind": kind, "program": program, "memo": memo},
	})
	raw, status, errCode, err := b.do(ctx, http.MethodPost, "/v2/"+b.ledger+"/transactions", nil, body)
	amt := money.FromCents(amountCents)
	entry := ledger.Entry{
		Kind: kind, Program: program, Ref: ref, Memo: memo, Amount: amt, CreatedAt: now,
		Postings: []ledger.Posting{{Account: source, Amount: amt.Neg()}, {Account: dest, Amount: amt}},
	}
	if err != nil {
		return ledger.Entry{}, false, err
	}
	switch {
	case status >= 200 && status < 300:
		entry.ID = decodeTxID(raw)
		return entry, true, nil
	case status == http.StatusConflict || errCode == "CONFLICT":
		entry.ID = ref // idempotent replay — the reference IS the stable key
		return entry, false, nil
	case errCode == "INSUFFICIENT_FUND":
		return ledger.Entry{}, false, errInsufficientFunds
	default:
		return ledger.Entry{}, false, fmt.Errorf("formance transaction %d: %s", status, string(raw))
	}
}

// Snapshot reads the reserve + per-program payout balances from Formance and
// assembles the same Report the native backend returns.
func (b *Backend) Snapshot(ctx context.Context) (ledger.Report, error) {
	reserve, err := b.Balance(ctx, reserveAddress)
	if err != nil {
		return ledger.Report{}, fmt.Errorf("reserve balance: %w", err)
	}
	byProgram := make(map[string]int64, len(knownPrograms))
	var paid int64
	for _, p := range knownPrograms {
		bal, err := b.Balance(ctx, ledger.PayoutAccount(p))
		if err != nil {
			return ledger.Report{}, fmt.Errorf("payout %s balance: %w", p, err)
		}
		if bal != 0 {
			byProgram[p] = bal
			paid += bal
		}
	}
	pol, err := b.policy.Policy(ctx)
	if err != nil {
		return ledger.Report{}, fmt.Errorf("policy: %w", err)
	}
	return ledger.Report{
		ReserveCents:     reserve,
		AccruedCents:     reserve + paid,
		PaidCents:        paid,
		ByProgramCents:   byProgram,
		Policy:           pol,
		SolventForPayout: reserve > 0,
	}, nil
}

// Balance reads an account's USD balance via the aggregate-balances endpoint (an
// exact address returns just that account's balance). A missing account is 0.
func (b *Backend) Balance(ctx context.Context, account string) (int64, error) {
	q := url.Values{"address": {account}}
	raw, status, _, err := b.do(ctx, http.MethodGet, "/v2/"+b.ledger+"/aggregate/balances", q, nil)
	if err != nil {
		return 0, err
	}
	if status == http.StatusNotFound {
		return 0, nil
	}
	if status < 200 || status >= 300 {
		return 0, fmt.Errorf("formance balance %d: %s", status, string(raw))
	}
	var out struct {
		Data map[string]int64 `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("formance balance decode: %w", err)
	}
	return out.Data[assetUSD], nil
}

// Entries lists recent Formance transactions and maps them to ledger.Entry for the
// admin journal view + the anchor root.
func (b *Backend) Entries(ctx context.Context, limit int) ([]ledger.Entry, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("pageSize", strconv.Itoa(limit))
	}
	raw, status, _, err := b.do(ctx, http.MethodGet, "/v2/"+b.ledger+"/transactions", q, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("formance transactions %d: %s", status, string(raw))
	}
	var out struct {
		Cursor struct {
			Data []struct {
				ID        json.Number       `json:"id"`
				Timestamp time.Time         `json:"timestamp"`
				Reference string            `json:"reference"`
				Metadata  map[string]string `json:"metadata"`
				Postings  []struct {
					Amount      int64  `json:"amount"`
					Asset       string `json:"asset"`
					Source      string `json:"source"`
					Destination string `json:"destination"`
				} `json:"postings"`
			} `json:"data"`
		} `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("formance transactions decode: %w", err)
	}
	entries := make([]ledger.Entry, 0, len(out.Cursor.Data))
	for _, t := range out.Cursor.Data {
		e := ledger.Entry{
			ID:        t.ID.String(),
			Kind:      t.Metadata["kind"],
			Program:   t.Metadata["program"],
			Ref:       t.Reference,
			Memo:      t.Metadata["memo"],
			CreatedAt: t.Timestamp.Unix(),
		}
		for _, p := range t.Postings {
			amt := money.FromCents(p.Amount) // Formance USD/2 asset == cents
			e.Postings = append(e.Postings,
				ledger.Posting{Account: p.Source, Amount: amt.Neg()},
				ledger.Posting{Account: p.Destination, Amount: amt})
			if p.Destination == reserveAddress || strings.HasPrefix(p.Destination, "payout:") {
				e.Amount = amt
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// ReserveCents is the reserve fund's available balance.
func (b *Backend) ReserveCents(ctx context.Context) (int64, error) {
	return b.Balance(ctx, reserveAddress)
}

// AccountsWithPrefix lists accounts and returns account→USD balance for those under
// prefix — the scope-aware read (a per-org caller's "org:<tenant>:" accounts, or the
// house prefixes for SuperAdmin). Best-effort: an unreachable Formance degrades to
// an empty map (honest empty, never a fabricated balance).
func (b *Backend) AccountsWithPrefix(ctx context.Context, prefix string) (map[string]int64, error) {
	q := url.Values{"expand": {"volumes"}, "pageSize": {"1000"}}
	raw, status, _, err := b.do(ctx, http.MethodGet, "/v2/"+b.ledger+"/accounts", q, nil)
	if err != nil || status < 200 || status >= 300 {
		return map[string]int64{}, nil
	}
	var out struct {
		Cursor struct {
			Data []struct {
				Address string `json:"address"`
				Volumes map[string]struct {
					Balance int64 `json:"balance"`
				} `json:"volumes"`
			} `json:"data"`
		} `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]int64{}, nil
	}
	res := make(map[string]int64)
	for _, a := range out.Cursor.Data {
		if prefix != "" && !strings.HasPrefix(a.Address, prefix) {
			continue
		}
		res[a.Address] = a.Volumes[assetUSD].Balance
	}
	return res, nil
}

// Root fetches the whole journal + reserve and commits it with the SHARED root hash,
// so the on-chain anchor is computed the one way regardless of backend.
func (b *Backend) Root(ctx context.Context) ([32]byte, int, error) {
	entries, err := b.Entries(ctx, 0)
	if err != nil {
		return [32]byte{}, 0, err
	}
	reserve, err := b.ReserveCents(ctx)
	if err != nil {
		return [32]byte{}, 0, err
	}
	return ledger.ComputeRoot(entries, money.FromCents(reserve)), len(entries), nil
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

// do performs one Formance API request, returning the body, HTTP status, and the
// Formance errorCode (when the body carries one) so callers can branch on
// CONFLICT / INSUFFICIENT_FUND without re-parsing.
func (b *Backend) do(ctx context.Context, method, path string, q url.Values, body []byte) ([]byte, int, string, error) {
	u := b.base + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("formance unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, "", err
	}
	var e struct {
		ErrorCode string `json:"errorCode"`
	}
	_ = json.Unmarshal(out, &e)
	return out, resp.StatusCode, e.ErrorCode, nil
}

// ── error mapping ────────────────────────────────────────────────────────────

var errInsufficientFunds = fmt.Errorf("formance: insufficient funds")

func isInsufficientFunds(err error) bool { return err == errInsufficientFunds }

// decodeTxID pulls the transaction id out of a Formance create-transaction response
// ({"data":{"id":<number>,...}}), tolerating both numeric and string ids.
func decodeTxID(raw []byte) string {
	var out struct {
		Data struct {
			ID json.Number `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return ""
	}
	return out.Data.ID.String()
}
