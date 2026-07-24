package books

// plaid.go — the Plaid connector: the automated live bank feed (Bank of America is a Plaid
// OAuth institution). It does THREE things and touches the bank READ-ONLY:
//
//	LinkToken  POST /link/token/create        → a short-lived link_token the browser Link UI opens
//	Exchange   POST /item/public_token/exchange → trades Link's public_token for a durable
//	                                             access_token, SEALED into KMS keyed per org+item,
//	                                             NEVER written to books.db, logs, or config
//	Fetch      POST /transactions/sync         → cursor-based incremental pull → []BankTxn
//
// KEY CUSTODY. Plaid's client_id/secret are resolved from KMS at call time via the env-named
// references CLOUD_PLAID_CLIENT_ID / CLOUD_PLAID_SECRET (the env var holds the KMS REFERENCE,
// never the value — same discipline as clients/idv). The per-item access_token is sealed into
// KMS on Exchange and fetched fresh on every Fetch; it never lands in the ledger. A deployment
// without KMS wired has no linked item and simply pulls nothing (fail-closed to empty).
//
// MONEY MATH. Plaid amounts are decimal DOLLARS, positive when money LEAVES the account. We
// decode the amount as json.Number (the exact JSON literal, never float64) and hand its string
// to parseCents, so the ledger only ever sees exact int64 cents. The sign becomes Direction
// (positive → Outflow, negative → Inflow) and the magnitude becomes AmountCents.
//
// IDEMPOTENCY. transaction_id is the stable ExternalID; the engine's (connector, external_id)
// UNIQUE guard makes a re-sync a no-op. Pending rows are SKIPPED — only settled transactions
// post — so a pending→posted transition (same transaction_id, surfaced under `modified`) books
// exactly once. This file does NOT edit bank.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// plaidTimeout bounds every Plaid call so a slow upstream can never wedge a sync.
const plaidTimeout = 20 * time.Second

// plaidSyncPageCap bounds one Fetch's paging so a first sync over a long history cannot run
// unbounded; the cursor is resumable, so a capped sync simply continues on the next run.
const plaidSyncPageCap = 50

// plaidSyncCount is the page size for /transactions/sync (Plaid's max is 500).
const plaidSyncCount = 500

// plaidConn is the Plaid connector. Every field is an OPTIONAL seam with a production default
// resolved at call time: base falls back to CLOUD_PLAID_BASE_URL (else Plaid production), kms
// to bankKMS(), env to os.Getenv, and http to a timeout-bounded client. newPlaid() returns the
// zero value (the frozen bank.go wiring), so production reads globals; a test constructs the
// struct literal with its own httptest base, fake KMS, and env map — no bank.go edit needed.
type plaidConn struct {
	base string
	http *http.Client
	kms  cloud.KMSClient
	env  func(string) string
}

func newPlaid() *plaidConn { return &plaidConn{} }

func (*plaidConn) Name() string { return "plaid" }

// baseURL is the Plaid API root: the injected base, else CLOUD_PLAID_BASE_URL, else production.
// The environment selector (sandbox/development/production) is expressed purely as the host.
func (c *plaidConn) baseURL() string {
	if b := strings.TrimSpace(c.base); b != "" {
		return strings.TrimRight(b, "/")
	}
	if b := strings.TrimSpace(c.envOf("CLOUD_PLAID_BASE_URL")); b != "" {
		return strings.TrimRight(b, "/")
	}
	return "https://production.plaid.com"
}

func (c *plaidConn) envOf(name string) string {
	if c.env != nil {
		return c.env(name)
	}
	return os.Getenv(name)
}

func (c *plaidConn) kmsClient() cloud.KMSClient {
	if c.kms != nil {
		return c.kms
	}
	return bankKMS()
}

func (c *plaidConn) client() *http.Client {
	if c.http != nil {
		return c.http
	}
	return &http.Client{Timeout: plaidTimeout}
}

// creds resolves the Plaid client_id/secret from KMS through the env-named references. Both are
// secrets: the env vars carry KMS references, and the values are fetched fresh (never held in
// process env or logged). Fail-closed — a missing KMS, an unset reference, or an unresolvable
// secret is an error, so no call ever proceeds with a blank credential.
func (c *plaidConn) creds(ctx context.Context) (clientID, secret string, err error) {
	k := c.kmsClient()
	if k == nil {
		return "", "", fmt.Errorf("plaid: KMS not wired; cannot resolve credentials")
	}
	idRef := strings.TrimSpace(c.envOf("CLOUD_PLAID_CLIENT_ID"))
	secRef := strings.TrimSpace(c.envOf("CLOUD_PLAID_SECRET"))
	if idRef == "" || secRef == "" {
		return "", "", fmt.Errorf("plaid: CLOUD_PLAID_CLIENT_ID / CLOUD_PLAID_SECRET (KMS references) are not set")
	}
	idb, err := k.GetSecret(ctx, idRef)
	if err != nil {
		return "", "", fmt.Errorf("plaid: client_id ref %q did not resolve: %w", idRef, err)
	}
	secb, err := k.GetSecret(ctx, secRef)
	if err != nil {
		return "", "", fmt.Errorf("plaid: secret ref %q did not resolve: %w", secRef, err)
	}
	clientID, secret = strings.TrimSpace(string(idb)), strings.TrimSpace(string(secb))
	if clientID == "" || secret == "" {
		return "", "", fmt.Errorf("plaid: resolved an empty client_id or secret")
	}
	return clientID, secret, nil
}

// ── KMS reference scheme (org-scoped, item-scoped) ───────────────────────────────────────────
// The per-item access_token is sealed at itemTokenRef; the org's set of linked item ids lives
// at itemIndexRef so Fetch can enumerate what to pull. Both fold :org into the path, so a
// connector can only ever read THIS org's material.

func itemIndexRef(org string) string       { return "orgs/" + org + "/books/plaid/items" }
func itemTokenRef(org, item string) string { return "orgs/" + org + "/books/plaid/items/" + item }

// readItems returns the org's linked Plaid item ids ([] when none/unlinked). A missing index
// (never linked) is not an error — the org simply pulls nothing.
func (c *plaidConn) readItems(ctx context.Context, k cloud.KMSClient, org string) ([]string, error) {
	raw, err := k.GetSecret(ctx, itemIndexRef(org))
	if err != nil || len(raw) == 0 {
		return nil, nil // unlinked: no items, no error
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("plaid: item index for org %q is corrupt: %w", org, err)
	}
	return items, nil
}

// addItem appends an item id to the org's KMS-held index, idempotently (a re-link of the same
// item never duplicates it).
func (c *plaidConn) addItem(ctx context.Context, k cloud.KMSClient, org, item string) error {
	items, err := c.readItems(ctx, k, org)
	if err != nil {
		return err
	}
	for _, existing := range items {
		if existing == item {
			return nil
		}
	}
	items = append(items, item)
	blob, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return k.PutSecret(ctx, itemIndexRef(org), blob)
}

// ── link flow ────────────────────────────────────────────────────────────────────────────────

// linkTokenResp is the /link/token/create response subset we surface.
type linkTokenResp struct {
	LinkToken  string `json:"link_token"`
	Expiration string `json:"expiration"`
}

// LinkToken creates a short-lived link_token for the org's browser Link session (products:
// transactions, country: US — the Bank of America OAuth path). The token is safe to return to
// the client; it authorizes ONLY the Link UI, never account access. Wire it from
// POST /v1/books/bank/link-token (see followup — the handler lives in bank_api.go).
func (c *plaidConn) LinkToken(ctx context.Context, org string) (string, string, error) {
	clientID, secret, err := c.creds(ctx)
	if err != nil {
		return "", "", err
	}
	body := map[string]any{
		"client_id":     clientID,
		"secret":        secret,
		"client_name":   "Hanzo Books",
		"language":      "en",
		"country_codes": []string{"US"},
		"user":          map[string]string{"client_user_id": org},
		"products":      []string{"transactions"},
	}
	var resp linkTokenResp
	if err := c.post(ctx, "/link/token/create", body, &resp); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(resp.LinkToken) == "" {
		return "", "", fmt.Errorf("plaid: link/token/create returned no link_token")
	}
	return resp.LinkToken, resp.Expiration, nil
}

// exchangeResp is the /item/public_token/exchange response. The access_token is the durable
// bank credential — it is SEALED into KMS and never returned to a caller or logged.
type exchangeResp struct {
	AccessToken string `json:"access_token"`
	ItemID      string `json:"item_id"`
}

// Exchange trades Link's one-time public_token for the durable access_token, seals it into KMS
// keyed per org+item, records the item in the org's KMS index, and returns ONLY the item id.
// The access_token never leaves KMS + this function's stack — it is not returned, logged, or
// persisted in books.db. Wire it from POST /v1/books/bank/exchange (followup).
func (c *plaidConn) Exchange(ctx context.Context, org, publicToken string) (string, error) {
	if strings.TrimSpace(publicToken) == "" {
		return "", fmt.Errorf("plaid: empty public_token")
	}
	k := c.kmsClient()
	if k == nil {
		return "", fmt.Errorf("plaid: KMS not wired; cannot seal access_token")
	}
	clientID, secret, err := c.creds(ctx)
	if err != nil {
		return "", err
	}
	var resp exchangeResp
	err = c.post(ctx, "/item/public_token/exchange", map[string]any{
		"client_id":    clientID,
		"secret":       secret,
		"public_token": publicToken,
	}, &resp)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.AccessToken) == "" || strings.TrimSpace(resp.ItemID) == "" {
		return "", fmt.Errorf("plaid: exchange returned an empty access_token or item_id")
	}
	// Seal the token in KMS FIRST — if this fails the item is not indexed, so a later Fetch
	// never references a token that was not stored (fail-closed, never a dangling item).
	if err := k.PutSecret(ctx, itemTokenRef(org, resp.ItemID), []byte(resp.AccessToken)); err != nil {
		return "", fmt.Errorf("plaid: sealing access_token into KMS failed: %w", err)
	}
	if err := c.addItem(ctx, k, org, resp.ItemID); err != nil {
		return "", fmt.Errorf("plaid: recording item in KMS index failed: %w", err)
	}
	return resp.ItemID, nil
}

// ── incremental pull ──────────────────────────────────────────────────────────────────────────

// plaidTxn is the /transactions/sync transaction subset we normalize. Amount is json.Number so
// the exact decimal literal reaches parseCents WITHOUT ever passing through float64.
type plaidTxn struct {
	TransactionID  string      `json:"transaction_id"`
	Amount         json.Number `json:"amount"`
	ISOCurrency    string      `json:"iso_currency_code"`
	UnofficialCurr string      `json:"unofficial_currency_code"`
	Name           string      `json:"name"`
	MerchantName   string      `json:"merchant_name"`
	Date           string      `json:"date"`     // YYYY-MM-DD (settlement date)
	Datetime       string      `json:"datetime"` // RFC3339 when available, else empty
	Pending        bool        `json:"pending"`
}

// syncReq is the /transactions/sync request. An empty cursor means "from the beginning".
type syncReq struct {
	ClientID    string `json:"client_id"`
	Secret      string `json:"secret"`
	AccessToken string `json:"access_token"`
	Cursor      string `json:"cursor,omitempty"`
	Count       int    `json:"count"`
}

// syncResp is the /transactions/sync response. We ingest `added` + `modified` (skipping pending
// rows); `removed` is not un-posted (the ledger is append-only) — see followup.
type syncResp struct {
	Added      []plaidTxn `json:"added"`
	Modified   []plaidTxn `json:"modified"`
	NextCursor string     `json:"next_cursor"`
	HasMore    bool       `json:"has_more"`
}

// Fetch pulls every linked Plaid item for the org incrementally and returns the normalized
// batch plus the advanced cursor. The connector-level cursor is a JSON map of item_id → Plaid
// cursor, so one connector can carry several linked banks each at its own high-water mark. A
// returned cursor equal to the input means nothing advanced (the engine then persists nothing).
// Credentials never appear in the returned data.
func (c *plaidConn) Fetch(ctx context.Context, org, cursor string) ([]BankTxn, string, error) {
	k := c.kmsClient()
	if k == nil {
		return nil, cursor, nil // no KMS → no linked item → nothing to pull (fail-closed empty)
	}
	items, err := c.readItems(ctx, k, org)
	if err != nil {
		return nil, cursor, err
	}
	if len(items) == 0 {
		return nil, cursor, nil // org has not linked a bank yet
	}
	clientID, secret, err := c.creds(ctx)
	if err != nil {
		return nil, cursor, err
	}
	cursors, err := decodeCursors(cursor)
	if err != nil {
		return nil, cursor, err
	}

	var out []BankTxn
	for _, item := range items {
		tokenBytes, err := k.GetSecret(ctx, itemTokenRef(org, item))
		if err != nil {
			return nil, cursor, fmt.Errorf("plaid: access_token for item %q did not resolve: %w", item, err)
		}
		token := strings.TrimSpace(string(tokenBytes))
		if token == "" {
			return nil, cursor, fmt.Errorf("plaid: access_token for item %q is empty", item)
		}
		cur := cursors[item]
		for page := 0; page < plaidSyncPageCap; page++ {
			var resp syncResp
			err := c.post(ctx, "/transactions/sync", syncReq{
				ClientID: clientID, Secret: secret, AccessToken: token, Cursor: cur, Count: plaidSyncCount,
			}, &resp)
			if err != nil {
				return nil, cursor, err
			}
			batch, err := mapPlaidTxns(append(resp.Added, resp.Modified...))
			if err != nil {
				return nil, cursor, err
			}
			out = append(out, batch...)
			cur = resp.NextCursor
			if !resp.HasMore {
				break
			}
		}
		cursors[item] = cur
	}

	next := encodeCursors(cursors)
	return out, next, nil
}

// mapPlaidTxns normalizes a Plaid page into BankTxns: skip pending (only settled posts) and
// zero-amount rows, decode the exact cents (never float64), and derive Direction from the sign.
func mapPlaidTxns(txns []plaidTxn) ([]BankTxn, error) {
	out := make([]BankTxn, 0, len(txns))
	for _, t := range txns {
		if t.Pending {
			continue // a pending row posts later (same transaction_id, under `modified`) — book once
		}
		if strings.TrimSpace(t.TransactionID) == "" {
			return nil, fmt.Errorf("plaid: transaction with no transaction_id")
		}
		signed, err := parseCents(t.Amount.String())
		if err != nil {
			return nil, fmt.Errorf("plaid: bad amount for %s: %w", t.TransactionID, err)
		}
		if signed == 0 {
			continue // a $0 line has no ledger effect
		}
		dir := Outflow // Plaid: positive = money LEAVING the account
		mag := signed
		if signed < 0 {
			dir, mag = Inflow, -signed
		}
		out = append(out, BankTxn{
			Connector:   "plaid",
			ExternalID:  t.TransactionID,
			PostedAt:    plaidPostedAt(t),
			AmountCents: mag,
			Currency:    plaidCurrency(t),
			Description: strings.TrimSpace(t.Name),
			Merchant:    strings.TrimSpace(t.MerchantName),
			Direction:   dir,
		})
	}
	return out, nil
}

// plaidPostedAt yields a ledger-comparable RFC3339 posting time: the exact datetime when Plaid
// gives one, else the settlement date at midnight UTC (so it compares consistently against the
// spine's RFC3339 postings in the reconciliation window).
func plaidPostedAt(t plaidTxn) string {
	if dt := strings.TrimSpace(t.Datetime); dt != "" {
		return dt
	}
	if d := strings.TrimSpace(t.Date); d != "" {
		return d + "T00:00:00Z"
	}
	return ""
}

// plaidCurrency is the row's currency, lowercased to match the spine's "usd", defaulting there.
func plaidCurrency(t plaidTxn) string {
	return strings.ToLower(firstNonEmpty(strings.TrimSpace(t.ISOCurrency), strings.TrimSpace(t.UnofficialCurr), "usd"))
}

// decodeCursors parses the connector cursor (a JSON map of item_id → Plaid cursor). An empty
// string is a fresh start (empty map). A malformed cursor is an error rather than a silent
// reset — resetting would re-pull (and the engine would idempotently skip, but we surface the
// corruption instead of hiding it).
func decodeCursors(cursor string) (map[string]string, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(cursor), &m); err != nil {
		return nil, fmt.Errorf("plaid: corrupt cursor: %w", err)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

// encodeCursors serializes the per-item cursor map. Go marshals map keys sorted, so the output
// is deterministic — an unchanged set of cursors re-encodes identically, and the sync loop then
// sees "no advance" and persists nothing.
func encodeCursors(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// plaidError is the Plaid API error envelope. Only error_code (never the message, which can
// carry account detail) is surfaced, alongside the HTTP status.
type plaidError struct {
	ErrorCode string `json:"error_code"`
	ErrorType string `json:"error_type"`
}

// post performs one Plaid call: POST JSON, enforce 2xx, decode the body into out. Every Plaid
// endpoint authenticates via client_id/secret IN THE BODY (the caller injects them), so there
// is no auth header to leak; the body is never logged. Fail-closed — a non-2xx or undecodable
// response is an error, never a partial success.
func (c *plaidConn) post(ctx context.Context, path string, reqBody any, out any) error {
	blob, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("plaid: encode %s request: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+path, bytes.NewReader(blob))
	if err != nil {
		return fmt.Errorf("plaid: build %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.client().Do(req)
	if err != nil {
		return fmt.Errorf("plaid: %s request: %w", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("plaid: %s read body: %w", path, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var pe plaidError
		_ = json.Unmarshal(data, &pe)
		if pe.ErrorCode != "" {
			return fmt.Errorf("plaid: %s returned HTTP %d (%s)", path, res.StatusCode, pe.ErrorCode)
		}
		return fmt.Errorf("plaid: %s returned HTTP %d", path, res.StatusCode)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("plaid: %s decode response: %w", path, err)
		}
	}
	return nil
}
