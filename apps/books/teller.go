package books

// teller.go — the Teller connector. Teller is a developer-friendly Plaid alternative (Bank
// of America among its supported institutions). Auth is per-enrollment: a long-lived
// access_token — stored in and fetched from KMS, NEVER books.db — presented as the HTTP
// Basic-auth username over a mutually-authenticated TLS channel whose client certificate
// and private key also live in KMS. Fetch resolves those credentials at sync time, calls
// Teller's read-only transactions API over a per-account cursor, and normalizes each
// settled row into a BankTxn (amounts parsed with parseCents — exact int64 cents, never
// float64). It does NOT edit bank.go.
//
// READ-ONLY: this connector only GETs /accounts and /accounts/{id}/transactions. There is
// no Teller Payments call here and there never will be — the bank engine records money that
// already moved, it never moves money.
//
// SIGN CONVENTION: on a Teller depository account (checking/savings — BoA) an amount is
// NEGATIVE for money leaving the account (an Outflow) and POSITIVE for money arriving (an
// Inflow). parseCents preserves the sign; the magnitude becomes AmountCents and the sign
// selects the Direction. Teller carries no reliable own-account "transfer" signal, so a
// transfer is NOT guessed here — it maps by sign like any other row and a human reclassifies
// it downstream (mirroring categorize()'s "never invent" stance). Only status=="posted"
// rows book; pending rows are volatile (they change id or vanish on settlement) and are
// skipped until they post.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// tellerBase is Teller's production API host. Tests override it via tellerConn.base.
const tellerBase = "https://api.teller.io"

// KMS credential refs. The per-enrollment access_token is org-scoped (one linked bank per
// org); the mTLS client certificate + private key are application-wide (issued once for the
// app in the Teller dashboard). NONE of these ever touches books.db — KMS is their only
// home, resolved fresh inside Fetch.
const (
	tellerCertRef = "books/teller/client-cert" // client certificate PEM (mTLS)
	tellerKeyRef  = "books/teller/client-key"  // client private key PEM (mTLS) — SECRET
)

// tellerTokenRef is the org-scoped KMS ref for one enrollment's access_token.
func tellerTokenRef(org string) string { return "books/teller/" + org + "/access-token" }

// Teller paging + response bounds. Transactions come newest-first; a page shorter than the
// requested count is the end of history. tellerMaxPages bounds a first-ever sync so a
// pathological feed can never loop unbounded — idempotency, not the cursor, is the
// correctness guarantee, so a bounded walk is always safe.
const (
	tellerPageCount    = 250
	tellerMaxPages     = 100
	tellerMaxRespBytes = 8 << 20 // 8 MiB ceiling on a single Teller response body
)

// tellerDoer is the HTTP seam: the production path uses an mTLS-configured *http.Client;
// tests inject a mock so the connector is exercised with no network and no real cert.
type tellerDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// tellerConn is the Teller connector. Its three seams (kms, doer, base) default to
// production values and are overridden only in tests — the zero value newTeller() returns is
// the real connector.
type tellerConn struct {
	kms  func() cloud.KMSClient // credential store accessor; nil → bankKMS()
	doer tellerDoer             // HTTP transport; nil → mTLS client built from the KMS cert
	base string                 // API base; "" → tellerBase
}

func newTeller() *tellerConn { return &tellerConn{} }

func (*tellerConn) Name() string { return "teller" }

// kmsClient resolves the credential store — the injected accessor in tests, else the shared
// bankKMS(). A nil result means KMS is not wired: a credentialed connector then fails closed
// (no token → no data), never plaintext.
func (tc *tellerConn) kmsClient() cloud.KMSClient {
	if tc.kms != nil {
		return tc.kms()
	}
	return bankKMS()
}

func (tc *tellerConn) baseURL() string {
	if tc.base != "" {
		return tc.base
	}
	return tellerBase
}

// Fetch pulls every account on the enrollment for transactions newer than cursor, normalizes
// the settled rows into BankTxns, and returns the advanced per-account cursor. It resolves
// the access_token + mTLS material from KMS on every call and holds them only in memory for
// the duration of the sync — nothing credential-bearing is returned, logged, or persisted.
// Absent credentials (KMS not wired, or this org has not linked a bank) is the normal
// pre-link state, not an error: Fetch returns no rows and the unchanged cursor so the sync
// loop moves on.
func (tc *tellerConn) Fetch(ctx context.Context, org, cursor string) ([]BankTxn, string, error) {
	kms := tc.kmsClient()
	if kms == nil {
		return nil, cursor, nil // no credential store → fail closed, nothing to pull
	}
	secret, err := kms.GetSecret(ctx, tellerTokenRef(org))
	if err != nil || len(secret) == 0 {
		return nil, cursor, nil // not linked yet → nothing to pull (never a plaintext fallback)
	}
	token := strings.TrimSpace(string(secret))
	if token == "" {
		return nil, cursor, nil
	}
	doer, err := tc.transport(ctx, kms)
	if err != nil {
		return nil, cursor, err
	}

	var accounts []tellerAccount
	if err := tc.get(ctx, doer, token, tc.baseURL()+"/accounts", &accounts); err != nil {
		return nil, cursor, err
	}

	seen := decodeTellerCursor(cursor)
	next := map[string]string{}
	var out []BankTxn
	for _, a := range accounts {
		last := seen[a.ID]
		txns, newest, err := tc.fetchAccount(ctx, doer, token, a, last)
		if err != nil {
			return nil, cursor, err
		}
		out = append(out, txns...)
		if newest != "" {
			next[a.ID] = newest
		} else {
			next[a.ID] = last
		}
	}
	// Preserve cursor entries for any account no longer listed (defensive; harmless).
	for id, v := range seen {
		if _, ok := next[id]; !ok {
			next[id] = v
		}
	}
	if len(out) == 0 {
		return nil, cursor, nil // nothing new → do not advance the cursor
	}
	return out, encodeTellerCursor(next), nil
}

// fetchAccount pages one account's transactions (newest-first) collecting every settled row
// newer than lastID, and returns them plus the newest transaction id seen (the account's new
// cursor). It stops at lastID (already ingested), at a short page (end of history), or at the
// page bound. parseCents failures surface as errors — a bank amount we cannot parse is never
// silently dropped.
func (tc *tellerConn) fetchAccount(ctx context.Context, doer tellerDoer, token string, acct tellerAccount, lastID string) ([]BankTxn, string, error) {
	base := tc.baseURL() + "/accounts/" + url.PathEscape(acct.ID) + "/transactions"
	fromID, newest := "", ""
	var out []BankTxn
	for page := 0; page < tellerMaxPages; page++ {
		u := base + "?count=" + strconv.Itoa(tellerPageCount)
		if fromID != "" {
			u += "&from_id=" + url.QueryEscape(fromID)
		}
		var rows []tellerTxn
		if err := tc.get(ctx, doer, token, u, &rows); err != nil {
			return nil, "", err
		}
		if len(rows) == 0 {
			break
		}
		stop := false
		for _, r := range rows {
			if newest == "" {
				newest = r.ID // first row of the first page is the newest transaction
			}
			if lastID != "" && r.ID == lastID {
				stop = true
				break
			}
			bt, ok, err := normalizeTeller(r, acct)
			if err != nil {
				return nil, "", err
			}
			if ok {
				out = append(out, bt)
			}
		}
		if stop || len(rows) < tellerPageCount {
			break
		}
		fromID = rows[len(rows)-1].ID
	}
	return out, newest, nil
}

// get performs one authenticated Teller GET. The access_token is the Basic-auth username
// (empty password) sent only in the request header over mTLS — never in the URL, never
// logged. An error carries the request path and status, never the token or the raw body.
func (tc *tellerConn) get(ctx context.Context, doer tellerDoer, token, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("teller: build request: %w", err)
	}
	req.SetBasicAuth(token, "")
	req.Header.Set("Accept", "application/json")
	resp, err := doer.Do(req)
	if err != nil {
		return fmt.Errorf("teller: %s: %w", requestPath(rawURL), err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, tellerMaxRespBytes))
	if err != nil {
		return fmt.Errorf("teller: %s: read body: %w", requestPath(rawURL), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("teller: %s: status %d", requestPath(rawURL), resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("teller: %s: decode: %w", requestPath(rawURL), err)
	}
	return nil
}

// transport resolves the HTTP doer: the injected mock in tests, else an mTLS client built
// from the client certificate + private key in KMS. Building it per-Fetch keeps the secret
// out of any long-lived field and re-reads a rotated cert on the next sync.
func (tc *tellerConn) transport(ctx context.Context, kms cloud.KMSClient) (tellerDoer, error) {
	if tc.doer != nil {
		return tc.doer, nil
	}
	certPEM, err := kms.GetSecret(ctx, tellerCertRef)
	if err != nil {
		return nil, fmt.Errorf("teller: client certificate unavailable: %w", err)
	}
	keyPEM, err := kms.GetSecret(ctx, tellerKeyRef)
	if err != nil {
		return nil, fmt.Errorf("teller: client key unavailable: %w", err)
	}
	return buildTellerClient(certPEM, keyPEM)
}

// buildTellerClient assembles the mutually-authenticated TLS client Teller requires: our
// client certificate proves the app's identity, TLS 1.2 is the floor, and a finite timeout
// means a stalled bank never hangs the sync loop.
func buildTellerClient(certPEM, keyPEM []byte) (tellerDoer, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("teller: load client cert: %w", err)
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			},
		},
	}, nil
}

// exchange is the link-flow's server side: it persists a Teller enrollment's access_token to
// KMS for one org. Symmetric to Plaid's public-token → access-token exchange, this is the
// ONLY write of the token, and it goes to KMS — never books.db. The route handler
// (bankExchangeHandler) calls this after Teller Connect returns the enrollment.
func (tc *tellerConn) exchange(ctx context.Context, org, accessToken string) error {
	org = strings.TrimSpace(org)
	accessToken = strings.TrimSpace(accessToken)
	if org == "" {
		return fmt.Errorf("teller: empty org")
	}
	if accessToken == "" {
		return fmt.Errorf("teller: empty access token")
	}
	kms := tc.kmsClient()
	if kms == nil {
		return fmt.Errorf("teller: KMS not wired; refusing to store access token in plaintext")
	}
	return kms.PutSecret(ctx, tellerTokenRef(org), []byte(accessToken))
}

// TellerLink is the client-side config for Teller Connect (the browser widget that produces
// an enrollment). application_id is PUBLIC (not a secret); environment selects sandbox vs
// production. This is the link-token analog — Teller Connect runs client-side, so there is
// no server-minted token, only this public config.
type TellerLink struct {
	ApplicationID string `json:"applicationId"`
	Environment   string `json:"environment"`
}

// linkConfig returns the public Teller Connect config from env — the link-token handler's
// payload. It reads no secret: the application id is public and the token only ever arrives
// afterward, via exchange, straight into KMS.
func (tc *tellerConn) linkConfig() (TellerLink, error) {
	app := strings.TrimSpace(os.Getenv("TELLER_APPLICATION_ID"))
	if app == "" {
		return TellerLink{}, fmt.Errorf("teller: TELLER_APPLICATION_ID not configured")
	}
	env := strings.TrimSpace(os.Getenv("TELLER_ENVIRONMENT"))
	if env == "" {
		env = "production"
	}
	return TellerLink{ApplicationID: app, Environment: env}, nil
}

// tellerAccount is the subset of a Teller account we consume: its id (for the transactions
// URL) and currency (stamped onto every BankTxn from it).
type tellerAccount struct {
	ID       string `json:"id"`
	Currency string `json:"currency"`
	Type     string `json:"type"`
}

// tellerTxn is the subset of a Teller transaction we normalize. Amount is a signed decimal
// STRING ("-12.34"); Status gates whether it books ("posted" only); Details carries the
// counterparty name we prefer as the merchant.
type tellerTxn struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	Amount      string `json:"amount"`
	Date        string `json:"date"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Details     struct {
		Category     string `json:"category"`
		Counterparty struct {
			Name string `json:"name"`
		} `json:"counterparty"`
	} `json:"details"`
}

// normalizeTeller maps one Teller transaction to a BankTxn. It returns ok=false to SKIP a row
// that must not book (a pending/volatile row, or a zero-amount non-movement) and an error
// only for a malformed amount — money we cannot parse is surfaced, never silently dropped.
func normalizeTeller(r tellerTxn, acct tellerAccount) (BankTxn, bool, error) {
	if !strings.EqualFold(strings.TrimSpace(r.Status), "posted") {
		return BankTxn{}, false, nil // pending rows change id or vanish on settlement — skip until posted
	}
	signed, err := parseCents(r.Amount)
	if err != nil {
		return BankTxn{}, false, fmt.Errorf("teller: txn %s amount %q: %w", r.ID, r.Amount, err)
	}
	if signed == 0 {
		return BankTxn{}, false, nil // zero is not a money movement
	}
	dir, mag := Inflow, signed
	if signed < 0 {
		dir, mag = Outflow, -signed
	}
	merchant := strings.TrimSpace(r.Details.Counterparty.Name)
	if merchant == "" {
		merchant = strings.TrimSpace(r.Description)
	}
	currency := strings.ToLower(strings.TrimSpace(acct.Currency))
	if currency == "" {
		currency = "usd"
	}
	return BankTxn{
		Connector:   "teller",
		ExternalID:  strings.TrimSpace(r.ID),
		PostedAt:    tellerTime(r.Date),
		AmountCents: mag,
		Currency:    currency,
		Description: strings.TrimSpace(r.Description),
		Merchant:    merchant,
		Direction:   dir,
	}, true, nil
}

// tellerTime normalizes a Teller date ("2026-07-05", date-only) to a ledger-comparable
// RFC3339 posting time at UTC midnight — the form matchInflow's window comparison and the
// rest of the engine expect.
func tellerTime(date string) string {
	d := strings.TrimSpace(date)
	if d == "" {
		return "" // validateTxn rejects an empty posted_at downstream
	}
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, d); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return d
}

// decodeTellerCursor reads the per-account high-water map from the stored cursor. A missing
// or unparseable cursor yields an empty map — a full (idempotent) resync, never a crash.
func decodeTellerCursor(cursor string) map[string]string {
	m := map[string]string{}
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return m
	}
	if err := json.Unmarshal([]byte(cursor), &m); err != nil {
		return map[string]string{}
	}
	return m
}

// encodeTellerCursor serializes the per-account high-water map for persistence.
func encodeTellerCursor(m map[string]string) string {
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// requestPath strips a URL to method-scope path for safe error text — never the query (which
// could echo a from_id) and never the token (which lives only in the header).
func requestPath(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return u.Path
	}
	return "request"
}
