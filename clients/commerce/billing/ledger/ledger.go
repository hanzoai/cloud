// Package ledger implements a double-entry accounting ledger for the billing engine.
//
// Invariants:
//  1. Every Entry has Postings that sum to exactly zero.
//  2. Idempotency keys prevent duplicate entries within a tenant.
//  3. Balances are derived exclusively from postings and holds.
//  4. Holds track the authorize -> capture/void lifecycle.
//  5. All operations are tenant-isolated.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	ErrPostingsNotBalanced = errors.New("ledger: postings do not sum to zero")
	ErrDuplicateEntry      = errors.New("ledger: duplicate idempotency key")
	ErrAccountNotFound     = errors.New("ledger: account not found")
	ErrEntryNotFound       = errors.New("ledger: entry not found")
	ErrHoldNotFound        = errors.New("ledger: hold not found")
	ErrHoldNotPending      = errors.New("ledger: hold is not in pending status")
	ErrCaptureExceedsHold  = errors.New("ledger: capture amount exceeds hold")
	ErrInsufficientBalance = errors.New("ledger: insufficient available balance")
	ErrInvalidAmount       = errors.New("ledger: amount must be positive")
	ErrEmptyPostings       = errors.New("ledger: entry must have at least two postings")
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// AccountType classifies an account in the chart of accounts.
type AccountType string

const (
	Asset     AccountType = "asset"
	Liability AccountType = "liability"
	Equity    AccountType = "equity"
	Revenue   AccountType = "revenue"
	Expense   AccountType = "expense"
)

// NormalBalance returns whether the natural balance of this account type
// increases with debits or credits.
func (t AccountType) NormalBalance() string {
	switch t {
	case Asset, Expense:
		return "debit"
	default:
		return "credit"
	}
}

// HoldStatus represents the lifecycle state of an authorization hold.
type HoldStatus string

const (
	HoldPending  HoldStatus = "pending"
	HoldCaptured HoldStatus = "captured"
	HoldVoided   HoldStatus = "voided"
	HoldExpired  HoldStatus = "expired"
)

// Account represents a named account in the chart of accounts.
type Account struct {
	ID            string                 `json:"id"`
	TenantID      string                 `json:"tenantId"`
	Name          string                 `json:"name"`
	Type          AccountType            `json:"type"`
	Currency      string                 `json:"currency"`
	NormalBalance string                 `json:"normalBalance"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt     time.Time              `json:"createdAt"`
}

// Entry is a journal entry grouping one or more postings.
type Entry struct {
	ID              string                 `json:"id"`
	TenantID        string                 `json:"tenantId"`
	IdempotencyKey  string                 `json:"idempotencyKey"`
	Description     string                 `json:"description"`
	PaymentIntentID string                 `json:"paymentIntentId,omitempty"`
	RefundID        string                 `json:"refundId,omitempty"`
	PayoutID        string                 `json:"payoutId,omitempty"`
	TransferID      string                 `json:"transferId,omitempty"`
	DisputeID       string                 `json:"disputeId,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	Postings        []Posting              `json:"postings"`
	CreatedAt       time.Time              `json:"createdAt"`
}

// Posting is a single debit or credit leg of a journal entry.
// Positive amount = debit, negative amount = credit.
type Posting struct {
	ID        string    `json:"id"`
	EntryID   string    `json:"entryId"`
	AccountID string    `json:"accountId"`
	Amount    int64     `json:"amount"` // positive=debit, negative=credit
	Currency  string    `json:"currency"`
	CreatedAt time.Time `json:"createdAt"`
}

// Hold represents a pending authorization hold against an account.
type Hold struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenantId"`
	AccountID       string     `json:"accountId"`
	Amount          int64      `json:"amount"` // always positive
	Currency        string     `json:"currency"`
	Status          HoldStatus `json:"status"`
	PaymentIntentID string     `json:"paymentIntentId,omitempty"`
	CapturedEntryID string     `json:"capturedEntryId,omitempty"`
	ExpiresAt       time.Time  `json:"expiresAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// Balance is the materialized balance for an account+currency pair.
type Balance struct {
	AccountID        string    `json:"accountId"`
	Currency         string    `json:"currency"`
	PostedBalance    int64     `json:"postedBalance"`
	PendingBalance   int64     `json:"pendingBalance"`
	HeldBalance      int64     `json:"heldBalance"`
	AvailableBalance int64     `json:"availableBalance"` // posted - held
	UpdatedAt        time.Time `json:"updatedAt"`
}

// EntryFilter controls listing of entries.
type EntryFilter struct {
	TenantID        string
	AccountID       string
	PaymentIntentID string
	RefundID        string
	PayoutID        string
	DisputeID       string
	CreatedAfter    time.Time
	CreatedBefore   time.Time
	Limit           int
}

// ---------------------------------------------------------------------------
// Interface
// ---------------------------------------------------------------------------

// Ledger defines the double-entry ledger operations.
type Ledger interface {
	// Accounts
	CreateAccount(ctx context.Context, account *Account) error
	GetAccount(ctx context.Context, id string) (*Account, error)

	// Entries (double-entry postings)
	PostEntry(ctx context.Context, entry *Entry) error // validates sum=0, idempotency
	GetEntry(ctx context.Context, id string) (*Entry, error)
	ListEntries(ctx context.Context, filter EntryFilter) ([]*Entry, error)

	// Holds (auth captures)
	CreateHold(ctx context.Context, hold *Hold) error
	CaptureHold(ctx context.Context, holdID string, amount int64) (*Entry, error)
	VoidHold(ctx context.Context, holdID string) error

	// Balances
	GetBalance(ctx context.Context, accountID string, currency string) (*Balance, error)

	// High-level operations
	RecordPayment(ctx context.Context, tenantID, paymentIntentID string, amount int64, currency string, customerID string, fees int64) (*Entry, error)
	RecordRefund(ctx context.Context, tenantID, refundID string, amount int64, currency string, customerID string) (*Entry, error)
	RecordPayout(ctx context.Context, tenantID, payoutID string, amount int64, currency string, merchantID string) (*Entry, error)
	RecordDispute(ctx context.Context, tenantID, disputeID string, amount int64, currency string, customerID string) (*Entry, error)
}

// ---------------------------------------------------------------------------
// In-Memory Implementation
// ---------------------------------------------------------------------------

// MemLedger is an in-memory Ledger for testing and development.
type MemLedger struct {
	mu       sync.RWMutex
	accounts map[string]*Account            // id -> account
	entries  map[string]*Entry              // id -> entry
	holds    map[string]*Hold               // id -> hold
	balances map[string]*Balance            // "accountID:currency" -> balance
	idemp    map[string]string              // "tenantID:idempotencyKey" -> entryID
	postings map[string][]Posting           // entryID -> postings
	acctName map[string]string              // "tenantID:name" -> accountID
}

// NewMemLedger creates a new in-memory ledger.
func NewMemLedger() *MemLedger {
	return &MemLedger{
		accounts: make(map[string]*Account),
		entries:  make(map[string]*Entry),
		holds:    make(map[string]*Hold),
		balances: make(map[string]*Balance),
		idemp:    make(map[string]string),
		postings: make(map[string][]Posting),
		acctName: make(map[string]string),
	}
}

func newID() string {
	return uuid.New().String()
}

func balKey(accountID, currency string) string {
	return accountID + ":" + currency
}

func idempKey(tenantID, key string) string {
	return tenantID + ":" + key
}

func acctNameKey(tenantID, name string) string {
	return tenantID + ":" + name
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

func (m *MemLedger) CreateAccount(_ context.Context, a *Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if a.ID == "" {
		a.ID = newID()
	}
	if a.Currency == "" {
		a.Currency = "usd"
	}
	if a.NormalBalance == "" {
		a.NormalBalance = a.Type.NormalBalance()
	}
	a.CreatedAt = time.Now()

	nk := acctNameKey(a.TenantID, a.Name)
	if _, exists := m.acctName[nk]; exists {
		return fmt.Errorf("ledger: account %q already exists for tenant %s", a.Name, a.TenantID)
	}

	m.accounts[a.ID] = a
	m.acctName[nk] = a.ID
	return nil
}

func (m *MemLedger) GetAccount(_ context.Context, id string) (*Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	a, ok := m.accounts[id]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return a, nil
}

// getAccountByName is a helper to find an account by tenant+name (lock must be held).
func (m *MemLedger) getAccountByName(tenantID, name string) (*Account, error) {
	id, ok := m.acctName[acctNameKey(tenantID, name)]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return m.accounts[id], nil
}

// EnsureAccount finds or creates a named account within a tenant.
func (m *MemLedger) EnsureAccount(ctx context.Context, tenantID, name string, acctType AccountType, currency string) (*Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	nk := acctNameKey(tenantID, name)
	if id, ok := m.acctName[nk]; ok {
		return m.accounts[id], nil
	}

	a := &Account{
		ID:            newID(),
		TenantID:      tenantID,
		Name:          name,
		Type:          acctType,
		Currency:      currency,
		NormalBalance: acctType.NormalBalance(),
		CreatedAt:     time.Now(),
	}
	m.accounts[a.ID] = a
	m.acctName[nk] = a.ID
	return a, nil
}

// ---------------------------------------------------------------------------
// Entries
// ---------------------------------------------------------------------------

func (m *MemLedger) PostEntry(_ context.Context, e *Entry) error {
	if len(e.Postings) < 2 {
		return ErrEmptyPostings
	}

	// Validate zero-sum
	var sum int64
	for _, p := range e.Postings {
		sum += p.Amount
	}
	if sum != 0 {
		return fmt.Errorf("%w: sum=%d", ErrPostingsNotBalanced, sum)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Validate accounts exist
	for i := range e.Postings {
		if _, ok := m.accounts[e.Postings[i].AccountID]; !ok {
			return fmt.Errorf("%w: %s", ErrAccountNotFound, e.Postings[i].AccountID)
		}
	}

	// Idempotency check
	ik := idempKey(e.TenantID, e.IdempotencyKey)
	if existingID, ok := m.idemp[ik]; ok {
		// Return the existing entry (idempotent replay)
		existing := m.entries[existingID]
		*e = *existing
		return ErrDuplicateEntry
	}

	now := time.Now()
	if e.ID == "" {
		e.ID = newID()
	}
	e.CreatedAt = now

	// Assign IDs to postings
	for i := range e.Postings {
		e.Postings[i].ID = newID()
		e.Postings[i].EntryID = e.ID
		if e.Postings[i].Currency == "" {
			e.Postings[i].Currency = "usd"
		}
		e.Postings[i].CreatedAt = now
	}

	m.entries[e.ID] = e
	m.idemp[ik] = e.ID
	m.postings[e.ID] = e.Postings

	// Update balances
	for _, p := range e.Postings {
		m.applyPosting(p)
	}

	return nil
}

// applyPosting updates the materialized balance for a posting (lock must be held).
func (m *MemLedger) applyPosting(p Posting) {
	bk := balKey(p.AccountID, p.Currency)
	b, ok := m.balances[bk]
	if !ok {
		b = &Balance{
			AccountID: p.AccountID,
			Currency:  p.Currency,
		}
		m.balances[bk] = b
	}
	b.PostedBalance += p.Amount
	b.AvailableBalance = b.PostedBalance - b.HeldBalance
	b.UpdatedAt = time.Now()
}

func (m *MemLedger) GetEntry(_ context.Context, id string) (*Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	e, ok := m.entries[id]
	if !ok {
		return nil, ErrEntryNotFound
	}
	return e, nil
}

func (m *MemLedger) ListEntries(_ context.Context, f EntryFilter) ([]*Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*Entry
	for _, e := range m.entries {
		if f.TenantID != "" && e.TenantID != f.TenantID {
			continue
		}
		if f.PaymentIntentID != "" && e.PaymentIntentID != f.PaymentIntentID {
			continue
		}
		if f.RefundID != "" && e.RefundID != f.RefundID {
			continue
		}
		if f.PayoutID != "" && e.PayoutID != f.PayoutID {
			continue
		}
		if f.DisputeID != "" && e.DisputeID != f.DisputeID {
			continue
		}
		if !f.CreatedAfter.IsZero() && !e.CreatedAt.After(f.CreatedAfter) {
			continue
		}
		if !f.CreatedBefore.IsZero() && !e.CreatedAt.Before(f.CreatedBefore) {
			continue
		}
		result = append(result, e)
		if f.Limit > 0 && len(result) >= f.Limit {
			break
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Holds
// ---------------------------------------------------------------------------

func (m *MemLedger) CreateHold(_ context.Context, h *Hold) error {
	if h.Amount <= 0 {
		return ErrInvalidAmount
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.accounts[h.AccountID]; !ok {
		return fmt.Errorf("%w: %s", ErrAccountNotFound, h.AccountID)
	}

	now := time.Now()
	if h.ID == "" {
		h.ID = newID()
	}
	h.Status = HoldPending
	h.CreatedAt = now
	h.UpdatedAt = now

	m.holds[h.ID] = h

	// Increase held balance
	bk := balKey(h.AccountID, h.Currency)
	b, ok := m.balances[bk]
	if !ok {
		b = &Balance{
			AccountID: h.AccountID,
			Currency:  h.Currency,
		}
		m.balances[bk] = b
	}
	b.HeldBalance += h.Amount
	b.AvailableBalance = b.PostedBalance - b.HeldBalance
	b.UpdatedAt = now

	return nil
}

func (m *MemLedger) CaptureHold(ctx context.Context, holdID string, amount int64) (*Entry, error) {
	m.mu.Lock()
	h, ok := m.holds[holdID]
	if !ok {
		m.mu.Unlock()
		return nil, ErrHoldNotFound
	}
	if h.Status != HoldPending {
		m.mu.Unlock()
		return nil, ErrHoldNotPending
	}
	if amount <= 0 {
		amount = h.Amount
	}
	if amount > h.Amount {
		m.mu.Unlock()
		return nil, ErrCaptureExceedsHold
	}

	// Release the hold
	h.Status = HoldCaptured
	h.UpdatedAt = time.Now()

	bk := balKey(h.AccountID, h.Currency)
	if b, ok := m.balances[bk]; ok {
		b.HeldBalance -= h.Amount
		b.AvailableBalance = b.PostedBalance - b.HeldBalance
		b.UpdatedAt = time.Now()
	}

	m.mu.Unlock()

	// Create a journal entry for the captured amount.
	// Debit the held account, credit platform cash.
	// The caller should have set up platform:cash; we look it up by tenant.
	acct := m.holds[holdID]
	tenantID := acct.TenantID

	// Find platform:cash for this tenant
	cashAcct, err := m.findOrFailAccount(tenantID, "platform:cash")
	if err != nil {
		return nil, fmt.Errorf("ledger: cannot capture hold without platform:cash account: %w", err)
	}

	entry := &Entry{
		TenantID:        tenantID,
		IdempotencyKey:  "capture:" + holdID,
		Description:     fmt.Sprintf("Capture hold %s for %d", holdID, amount),
		PaymentIntentID: h.PaymentIntentID,
		Postings: []Posting{
			{AccountID: h.AccountID, Amount: -amount, Currency: h.Currency}, // credit source
			{AccountID: cashAcct.ID, Amount: amount, Currency: h.Currency},  // debit platform cash
		},
	}

	if err := m.PostEntry(ctx, entry); err != nil {
		return nil, err
	}

	m.mu.Lock()
	h.CapturedEntryID = entry.ID
	m.mu.Unlock()

	return entry, nil
}

func (m *MemLedger) VoidHold(_ context.Context, holdID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	h, ok := m.holds[holdID]
	if !ok {
		return ErrHoldNotFound
	}
	if h.Status != HoldPending {
		return ErrHoldNotPending
	}

	h.Status = HoldVoided
	h.UpdatedAt = time.Now()

	// Release held balance
	bk := balKey(h.AccountID, h.Currency)
	if b, ok := m.balances[bk]; ok {
		b.HeldBalance -= h.Amount
		b.AvailableBalance = b.PostedBalance - b.HeldBalance
		b.UpdatedAt = time.Now()
	}

	return nil
}

// findOrFailAccount looks up an account by name (no lock held).
func (m *MemLedger) findOrFailAccount(tenantID, name string) (*Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getAccountByName(tenantID, name)
}

// ---------------------------------------------------------------------------
// Balances
// ---------------------------------------------------------------------------

func (m *MemLedger) GetBalance(_ context.Context, accountID string, currency string) (*Balance, error) {
	if currency == "" {
		currency = "usd"
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.accounts[accountID]; !ok {
		return nil, ErrAccountNotFound
	}

	bk := balKey(accountID, currency)
	b, ok := m.balances[bk]
	if !ok {
		return &Balance{
			AccountID:        accountID,
			Currency:         currency,
			AvailableBalance: 0,
			UpdatedAt:        time.Now(),
		}, nil
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// High-Level Operations
// ---------------------------------------------------------------------------

// RecordPayment creates a journal entry for a successful payment:
//
//	Debit  platform:cash              (amount)
//	Credit customer_balance:{cust}    (amount - fees)
//	Credit platform:fees              (fees)
//
// If fees == 0, the full amount credits the customer balance account.
func (m *MemLedger) RecordPayment(ctx context.Context, tenantID, paymentIntentID string, amount int64, cur string, customerID string, fees int64) (*Entry, error) {
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if cur == "" {
		cur = "usd"
	}

	cashAcct, err := m.EnsureAccount(ctx, tenantID, "platform:cash", Asset, cur)
	if err != nil {
		return nil, err
	}
	custAcct, err := m.EnsureAccount(ctx, tenantID, "customer_balance:"+customerID, Liability, cur)
	if err != nil {
		return nil, err
	}

	postings := []Posting{
		{AccountID: cashAcct.ID, Amount: amount, Currency: cur},              // debit cash
		{AccountID: custAcct.ID, Amount: -(amount - fees), Currency: cur},    // credit customer
	}

	if fees > 0 {
		feeAcct, err := m.EnsureAccount(ctx, tenantID, "platform:fees", Revenue, cur)
		if err != nil {
			return nil, err
		}
		postings[1].Amount = -(amount - fees)
		postings = append(postings, Posting{
			AccountID: feeAcct.ID, Amount: -fees, Currency: cur, // credit fees
		})
	}

	entry := &Entry{
		TenantID:        tenantID,
		IdempotencyKey:  "payment:" + paymentIntentID,
		Description:     fmt.Sprintf("Payment %s: %d %s (fees %d)", paymentIntentID, amount, cur, fees),
		PaymentIntentID: paymentIntentID,
		Postings:        postings,
	}

	if err := m.PostEntry(ctx, entry); err != nil && !errors.Is(err, ErrDuplicateEntry) {
		return nil, err
	}
	return entry, nil
}

// RecordRefund creates a journal entry reversing a payment:
//
//	Debit  customer_balance:{cust}    (amount)
//	Credit platform:cash              (amount)
func (m *MemLedger) RecordRefund(ctx context.Context, tenantID, refundID string, amount int64, cur string, customerID string) (*Entry, error) {
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if cur == "" {
		cur = "usd"
	}

	cashAcct, err := m.EnsureAccount(ctx, tenantID, "platform:cash", Asset, cur)
	if err != nil {
		return nil, err
	}
	custAcct, err := m.EnsureAccount(ctx, tenantID, "customer_balance:"+customerID, Liability, cur)
	if err != nil {
		return nil, err
	}

	entry := &Entry{
		TenantID:       tenantID,
		IdempotencyKey: "refund:" + refundID,
		Description:    fmt.Sprintf("Refund %s: %d %s to customer %s", refundID, amount, cur, customerID),
		RefundID:       refundID,
		Postings: []Posting{
			{AccountID: custAcct.ID, Amount: amount, Currency: cur},   // debit customer (reduce liability)
			{AccountID: cashAcct.ID, Amount: -amount, Currency: cur},  // credit cash (reduce asset)
		},
	}

	if err := m.PostEntry(ctx, entry); err != nil && !errors.Is(err, ErrDuplicateEntry) {
		return nil, err
	}
	return entry, nil
}

// RecordPayout creates a journal entry for a merchant payout:
//
//	Debit  merchant_settlement:{merchant}    (amount)
//	Credit platform:cash                     (amount)
func (m *MemLedger) RecordPayout(ctx context.Context, tenantID, payoutID string, amount int64, cur string, merchantID string) (*Entry, error) {
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if cur == "" {
		cur = "usd"
	}

	cashAcct, err := m.EnsureAccount(ctx, tenantID, "platform:cash", Asset, cur)
	if err != nil {
		return nil, err
	}
	merchAcct, err := m.EnsureAccount(ctx, tenantID, "merchant_settlement:"+merchantID, Liability, cur)
	if err != nil {
		return nil, err
	}

	entry := &Entry{
		TenantID:       tenantID,
		IdempotencyKey: "payout:" + payoutID,
		Description:    fmt.Sprintf("Payout %s: %d %s to merchant %s", payoutID, amount, cur, merchantID),
		PayoutID:       payoutID,
		Postings: []Posting{
			{AccountID: merchAcct.ID, Amount: amount, Currency: cur},  // debit merchant (reduce liability)
			{AccountID: cashAcct.ID, Amount: -amount, Currency: cur},  // credit cash (reduce asset)
		},
	}

	if err := m.PostEntry(ctx, entry); err != nil && !errors.Is(err, ErrDuplicateEntry) {
		return nil, err
	}
	return entry, nil
}

// RecordDispute creates a journal entry moving funds into a dispute hold account:
//
//	Debit  platform:disputes_held            (amount)
//	Credit platform:cash                     (amount)
func (m *MemLedger) RecordDispute(ctx context.Context, tenantID, disputeID string, amount int64, cur string, customerID string) (*Entry, error) {
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if cur == "" {
		cur = "usd"
	}

	cashAcct, err := m.EnsureAccount(ctx, tenantID, "platform:cash", Asset, cur)
	if err != nil {
		return nil, err
	}
	disputeAcct, err := m.EnsureAccount(ctx, tenantID, "platform:disputes_held", Asset, cur)
	if err != nil {
		return nil, err
	}

	entry := &Entry{
		TenantID:       tenantID,
		IdempotencyKey: "dispute:" + disputeID,
		Description:    fmt.Sprintf("Dispute %s: %d %s from customer %s", disputeID, amount, cur, customerID),
		DisputeID:      disputeID,
		Postings: []Posting{
			{AccountID: disputeAcct.ID, Amount: amount, Currency: cur},  // debit disputes held
			{AccountID: cashAcct.ID, Amount: -amount, Currency: cur},    // credit cash
		},
	}

	if err := m.PostEntry(ctx, entry); err != nil && !errors.Is(err, ErrDuplicateEntry) {
		return nil, err
	}
	return entry, nil
}

// Compile-time interface check.
var _ Ledger = (*MemLedger)(nil)
