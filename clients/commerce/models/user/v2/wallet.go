package user

import (
	"context"
	"errors"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/db"
)

// Wallet errors
var (
	ErrWalletNotFound      = errors.New("wallet: not found")
	ErrWalletAlreadyExists = errors.New("wallet: already exists")
	ErrInsufficientBalance = errors.New("wallet: insufficient balance")
	ErrInvalidAmount       = errors.New("wallet: invalid amount")
	ErrWalletLocked        = errors.New("wallet: locked")
	ErrAddressNotFound     = errors.New("wallet: address not found")
)

// Currency represents a supported currency
type Currency string

const (
	CurrencyUSD  Currency = "USD"
	CurrencyEUR  Currency = "EUR"
	CurrencyGBP  Currency = "GBP"
	CurrencyLUX  Currency = "LUX" // Native Lux token
	CurrencyETH  Currency = "ETH"
	CurrencyBTC  Currency = "BTC"
	CurrencyUSDC Currency = "USDC"
)

// AccountType represents the type of wallet account
type AccountType string

const (
	AccountTypeMain    AccountType = "main"
	AccountTypeRewards AccountType = "rewards"
	AccountTypeEscrow  AccountType = "escrow"
	AccountTypeStaking AccountType = "staking"
)

// Wallet represents a user's wallet with multiple accounts
type Wallet struct {
	ID     string `json:"id"`
	UserID string `json:"userId"`

	// Accounts by type
	Accounts map[AccountType]*Account `json:"accounts"`

	// Blockchain addresses
	Addresses []WalletAddress `json:"addresses,omitempty"`

	// Default currency
	DefaultCurrency Currency `json:"defaultCurrency"`

	// Security
	Locked     bool      `json:"locked"`
	LockedAt   time.Time `json:"lockedAt,omitempty"`
	LockReason string    `json:"lockReason,omitempty"`

	// Timestamps
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Account represents a balance account within a wallet
type Account struct {
	Type     AccountType `json:"type"`
	Balances []Balance   `json:"balances"`
	Holds    []Hold      `json:"holds,omitempty"`
}

// Balance represents a currency balance
type Balance struct {
	Currency  Currency  `json:"currency"`
	Amount    int64     `json:"amount"` // In smallest unit (cents, satoshis, wei, etc.)
	Available int64     `json:"available"`
	Pending   int64     `json:"pending"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Hold represents a held/reserved amount
type Hold struct {
	ID          string    `json:"id"`
	Currency    Currency  `json:"currency"`
	Amount      int64     `json:"amount"`
	Reason      string    `json:"reason"`
	ReferenceID string    `json:"referenceId,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// WalletAddress represents a blockchain address linked to the wallet
type WalletAddress struct {
	ID        string    `json:"id"`
	Chain     string    `json:"chain"` // "lux", "ethereum", "bitcoin"
	Address   string    `json:"address"`
	Label     string    `json:"label,omitempty"`
	IsPrimary bool      `json:"isPrimary"`
	Verified  bool      `json:"verified"`
	AddedAt   time.Time `json:"addedAt"`
}

// Kind implements db.Entity
func (w *Wallet) Kind() string {
	return "wallet"
}

// GetAccount returns an account by type, creating if needed
func (w *Wallet) GetAccount(accountType AccountType) *Account {
	if w.Accounts == nil {
		w.Accounts = make(map[AccountType]*Account)
	}

	if account, ok := w.Accounts[accountType]; ok {
		return account
	}

	account := &Account{
		Type:     accountType,
		Balances: make([]Balance, 0),
		Holds:    make([]Hold, 0),
	}
	w.Accounts[accountType] = account
	return account
}

// GetBalance returns the balance for a currency in an account
func (w *Wallet) GetBalance(accountType AccountType, currency Currency) *Balance {
	account := w.GetAccount(accountType)

	for i := range account.Balances {
		if account.Balances[i].Currency == currency {
			return &account.Balances[i]
		}
	}

	// Create new balance entry
	balance := Balance{
		Currency:  currency,
		Amount:    0,
		Available: 0,
		Pending:   0,
		UpdatedAt: time.Now(),
	}
	account.Balances = append(account.Balances, balance)
	return &account.Balances[len(account.Balances)-1]
}

// GetAvailableBalance returns the available balance (total - holds)
func (w *Wallet) GetAvailableBalance(accountType AccountType, currency Currency) int64 {
	balance := w.GetBalance(accountType, currency)
	return balance.Available
}

// GetPrimaryAddress returns the primary address for a chain
func (w *Wallet) GetPrimaryAddress(chain string) *WalletAddress {
	for i := range w.Addresses {
		if w.Addresses[i].Chain == chain && w.Addresses[i].IsPrimary {
			return &w.Addresses[i]
		}
	}
	return nil
}

// GetAddresses returns all addresses for a chain
func (w *Wallet) GetAddresses(chain string) []WalletAddress {
	var addresses []WalletAddress
	for _, addr := range w.Addresses {
		if addr.Chain == chain {
			addresses = append(addresses, addr)
		}
	}
	return addresses
}

// WalletRepository provides data access for Wallet entities
type WalletRepository struct {
	db db.DB
}

// NewWalletRepository creates a new Wallet repository
func NewWalletRepository(database db.DB) *WalletRepository {
	return &WalletRepository{db: database}
}

// Get retrieves a wallet by ID
func (r *WalletRepository) Get(ctx context.Context, walletID string) (*Wallet, error) {
	key := r.db.NewKey("wallet", walletID, 0, nil)
	wallet := &Wallet{}

	if err := r.db.Get(ctx, key, wallet); err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return nil, ErrWalletNotFound
		}
		return nil, err
	}

	return wallet, nil
}

// GetByUserID retrieves a wallet by user ID
func (r *WalletRepository) GetByUserID(ctx context.Context, userID string) (*Wallet, error) {
	wallet := &Wallet{}
	_, err := r.db.Query("wallet").Filter("UserID=", userID).First(ctx, wallet)
	if err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return nil, ErrWalletNotFound
		}
		return nil, err
	}

	return wallet, nil
}

// GetOrCreate retrieves a wallet or creates a new one
func (r *WalletRepository) GetOrCreate(ctx context.Context, userID string) (*Wallet, error) {
	wallet, err := r.GetByUserID(ctx, userID)
	if err == nil {
		return wallet, nil
	}

	if !errors.Is(err, ErrWalletNotFound) {
		return nil, err
	}

	// Create new wallet
	wallet = &Wallet{
		UserID:          userID,
		Accounts:        make(map[AccountType]*Account),
		Addresses:       make([]WalletAddress, 0),
		DefaultCurrency: CurrencyUSD,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}

	// Initialize main account
	wallet.GetAccount(AccountTypeMain)
	wallet.GetAccount(AccountTypeRewards)

	if err := r.Create(ctx, wallet); err != nil {
		return nil, err
	}

	return wallet, nil
}

// Create creates a new wallet
func (r *WalletRepository) Create(ctx context.Context, wallet *Wallet) error {
	now := time.Now()
	if wallet.CreatedAt.IsZero() {
		wallet.CreatedAt = now
	}
	wallet.UpdatedAt = now

	if wallet.ID == "" {
		key := r.db.NewIncompleteKey("wallet", nil)
		wallet.ID = key.Encode()
	}

	if wallet.Accounts == nil {
		wallet.Accounts = make(map[AccountType]*Account)
	}
	if wallet.Addresses == nil {
		wallet.Addresses = make([]WalletAddress, 0)
	}

	key := r.db.NewKey("wallet", wallet.ID, 0, nil)
	_, err := r.db.Put(ctx, key, wallet)
	return err
}

// Update updates an existing wallet
func (r *WalletRepository) Update(ctx context.Context, wallet *Wallet) error {
	wallet.UpdatedAt = time.Now()

	key := r.db.NewKey("wallet", wallet.ID, 0, nil)
	_, err := r.db.Put(ctx, key, wallet)
	return err
}

// Delete deletes a wallet
func (r *WalletRepository) Delete(ctx context.Context, walletID string) error {
	key := r.db.NewKey("wallet", walletID, 0, nil)
	return r.db.Delete(ctx, key)
}

// WalletService provides high-level wallet operations
type WalletService struct {
	service *Service
}

// NewWalletService creates a new wallet service
func NewWalletService(service *Service) *WalletService {
	return &WalletService{service: service}
}

// GetOrCreate retrieves or creates a user's wallet
func (s *WalletService) GetOrCreate(ctx context.Context, userID string) (*Wallet, error) {
	database, err := s.service.UserDB(userID)
	if err != nil {
		return nil, err
	}

	repo := NewWalletRepository(database)
	return repo.GetOrCreate(ctx, userID)
}

// Get retrieves a user's wallet
func (s *WalletService) Get(ctx context.Context, userID string) (*Wallet, error) {
	database, err := s.service.UserDB(userID)
	if err != nil {
		return nil, err
	}

	repo := NewWalletRepository(database)
	return repo.GetByUserID(ctx, userID)
}

// Credit adds funds to a wallet account
func (s *WalletService) Credit(ctx context.Context, userID string, accountType AccountType, currency Currency, amount int64, reason string) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}

	wallet, err := s.GetOrCreate(ctx, userID)
	if err != nil {
		return err
	}

	if wallet.Locked {
		return ErrWalletLocked
	}

	balance := wallet.GetBalance(accountType, currency)
	balance.Amount += amount
	balance.Available += amount
	balance.UpdatedAt = time.Now()

	database, err := s.service.UserDB(userID)
	if err != nil {
		return err
	}

	repo := NewWalletRepository(database)
	return repo.Update(ctx, wallet)
}

// Debit removes funds from a wallet account
func (s *WalletService) Debit(ctx context.Context, userID string, accountType AccountType, currency Currency, amount int64, reason string) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}

	wallet, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}

	if wallet.Locked {
		return ErrWalletLocked
	}

	balance := wallet.GetBalance(accountType, currency)
	if balance.Available < amount {
		return ErrInsufficientBalance
	}

	balance.Amount -= amount
	balance.Available -= amount
	balance.UpdatedAt = time.Now()

	database, err := s.service.UserDB(userID)
	if err != nil {
		return err
	}

	repo := NewWalletRepository(database)
	return repo.Update(ctx, wallet)
}

// PlaceHold places a hold on funds
func (s *WalletService) PlaceHold(ctx context.Context, userID string, accountType AccountType, currency Currency, amount int64, reason string, referenceID string, expiresAt time.Time) (*Hold, error) {
	if amount <= 0 {
		return nil, ErrInvalidAmount
	}

	wallet, err := s.Get(ctx, userID)
	if err != nil {
		return nil, err
	}

	if wallet.Locked {
		return nil, ErrWalletLocked
	}

	balance := wallet.GetBalance(accountType, currency)
	if balance.Available < amount {
		return nil, ErrInsufficientBalance
	}

	// Create hold
	hold := Hold{
		ID:          generateHoldID(),
		Currency:    currency,
		Amount:      amount,
		Reason:      reason,
		ReferenceID: referenceID,
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now(),
	}

	account := wallet.GetAccount(accountType)
	account.Holds = append(account.Holds, hold)

	// Update available balance
	balance.Available -= amount
	balance.UpdatedAt = time.Now()

	database, err := s.service.UserDB(userID)
	if err != nil {
		return nil, err
	}

	repo := NewWalletRepository(database)
	if err := repo.Update(ctx, wallet); err != nil {
		return nil, err
	}

	return &hold, nil
}

// ReleaseHold releases a hold
func (s *WalletService) ReleaseHold(ctx context.Context, userID string, accountType AccountType, holdID string) error {
	wallet, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}

	account := wallet.GetAccount(accountType)

	var hold *Hold
	holdIndex := -1
	for i := range account.Holds {
		if account.Holds[i].ID == holdID {
			hold = &account.Holds[i]
			holdIndex = i
			break
		}
	}

	if hold == nil {
		return errors.New("hold not found")
	}

	// Release the hold - add back to available
	balance := wallet.GetBalance(accountType, hold.Currency)
	balance.Available += hold.Amount
	balance.UpdatedAt = time.Now()

	// Remove hold from list
	account.Holds = append(account.Holds[:holdIndex], account.Holds[holdIndex+1:]...)

	database, err := s.service.UserDB(userID)
	if err != nil {
		return err
	}

	repo := NewWalletRepository(database)
	return repo.Update(ctx, wallet)
}

// CaptureHold captures (deducts) a held amount
func (s *WalletService) CaptureHold(ctx context.Context, userID string, accountType AccountType, holdID string) error {
	wallet, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}

	account := wallet.GetAccount(accountType)

	var hold *Hold
	holdIndex := -1
	for i := range account.Holds {
		if account.Holds[i].ID == holdID {
			hold = &account.Holds[i]
			holdIndex = i
			break
		}
	}

	if hold == nil {
		return errors.New("hold not found")
	}

	// Capture the hold - deduct from total amount
	balance := wallet.GetBalance(accountType, hold.Currency)
	balance.Amount -= hold.Amount
	balance.UpdatedAt = time.Now()

	// Remove hold from list
	account.Holds = append(account.Holds[:holdIndex], account.Holds[holdIndex+1:]...)

	database, err := s.service.UserDB(userID)
	if err != nil {
		return err
	}

	repo := NewWalletRepository(database)
	return repo.Update(ctx, wallet)
}

// AddAddress adds a blockchain address to the wallet
func (s *WalletService) AddAddress(ctx context.Context, userID string, address WalletAddress) error {
	wallet, err := s.GetOrCreate(ctx, userID)
	if err != nil {
		return err
	}

	address.AddedAt = time.Now()

	// If this is the first address for this chain, make it primary
	existing := wallet.GetAddresses(address.Chain)
	if len(existing) == 0 {
		address.IsPrimary = true
	}

	wallet.Addresses = append(wallet.Addresses, address)

	database, err := s.service.UserDB(userID)
	if err != nil {
		return err
	}

	repo := NewWalletRepository(database)
	return repo.Update(ctx, wallet)
}

// RemoveAddress removes a blockchain address from the wallet
func (s *WalletService) RemoveAddress(ctx context.Context, userID string, addressID string) error {
	wallet, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}

	addresses := make([]WalletAddress, 0, len(wallet.Addresses))
	for _, addr := range wallet.Addresses {
		if addr.ID != addressID {
			addresses = append(addresses, addr)
		}
	}
	wallet.Addresses = addresses

	database, err := s.service.UserDB(userID)
	if err != nil {
		return err
	}

	repo := NewWalletRepository(database)
	return repo.Update(ctx, wallet)
}

// SetPrimaryAddress sets an address as primary for its chain
func (s *WalletService) SetPrimaryAddress(ctx context.Context, userID string, addressID string) error {
	wallet, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}

	var targetChain string
	for i := range wallet.Addresses {
		if wallet.Addresses[i].ID == addressID {
			targetChain = wallet.Addresses[i].Chain
			break
		}
	}

	if targetChain == "" {
		return ErrAddressNotFound
	}

	// Clear existing primary and set new one
	for i := range wallet.Addresses {
		if wallet.Addresses[i].Chain == targetChain {
			wallet.Addresses[i].IsPrimary = wallet.Addresses[i].ID == addressID
		}
	}

	database, err := s.service.UserDB(userID)
	if err != nil {
		return err
	}

	repo := NewWalletRepository(database)
	return repo.Update(ctx, wallet)
}

// LockWallet locks a wallet
func (s *WalletService) LockWallet(ctx context.Context, userID string, reason string) error {
	wallet, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}

	wallet.Locked = true
	wallet.LockedAt = time.Now()
	wallet.LockReason = reason

	database, err := s.service.UserDB(userID)
	if err != nil {
		return err
	}

	repo := NewWalletRepository(database)
	return repo.Update(ctx, wallet)
}

// UnlockWallet unlocks a wallet
func (s *WalletService) UnlockWallet(ctx context.Context, userID string) error {
	wallet, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}

	wallet.Locked = false
	wallet.LockedAt = time.Time{}
	wallet.LockReason = ""

	database, err := s.service.UserDB(userID)
	if err != nil {
		return err
	}

	repo := NewWalletRepository(database)
	return repo.Update(ctx, wallet)
}

// generateHoldID generates a unique hold ID
func generateHoldID() string {
	return time.Now().Format("20060102150405") + "-" + randomString(8)
}

// randomString generates a random alphanumeric string
func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[time.Now().UnixNano()%int64(len(letters))]
	}
	return string(b)
}

// Transaction represents a wallet transaction
type Transaction struct {
	ID            string                 `json:"id"`
	WalletID      string                 `json:"walletId"`
	UserID        string                 `json:"userId"`
	AccountType   AccountType            `json:"accountType"`
	Type          string                 `json:"type"` // "credit", "debit", "hold", "release", "capture"
	Currency      Currency               `json:"currency"`
	Amount        int64                  `json:"amount"`
	BalanceAfter  int64                  `json:"balanceAfter"`
	Reason        string                 `json:"reason,omitempty"`
	ReferenceID   string                 `json:"referenceId,omitempty"`
	ReferenceType string                 `json:"referenceType,omitempty"` // "order", "refund", "reward", etc.
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt     time.Time              `json:"createdAt"`
}

// Kind implements db.Entity
func (t *Transaction) Kind() string {
	return "wallet_transaction"
}
