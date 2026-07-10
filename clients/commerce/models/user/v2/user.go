// Package user provides the User model using the new db.DB interface.
// This modernized implementation supports:
// - Per-user SQLite databases (data/users/{userID}/data.db)
// - OAuth2 tokens from hanzo.id (IAM)
// - Profile and settings management
// - Wallet integration via Lux blockchain
package user

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/db"
)

// Entity kind for User
const Kind = "user"

// Common errors
var (
	ErrUserNotFound    = errors.New("user: not found")
	ErrInvalidEmail    = errors.New("user: invalid email")
	ErrInvalidPassword = errors.New("user: invalid password")
	ErrEmailExists     = errors.New("user: email already exists")
	ErrUsernameExists  = errors.New("user: username already exists")
	ErrUnauthorized    = errors.New("user: unauthorized")
	ErrAccountDisabled = errors.New("user: account disabled")
	ErrKYCRequired     = errors.New("user: KYC verification required")
)

// KYCStatus represents the Know Your Customer verification status
type KYCStatus string

const (
	KYCStatusInitiated KYCStatus = "initiated"
	KYCStatusPending   KYCStatus = "pending"
	KYCStatusApproved  KYCStatus = "approved"
	KYCStatusDenied    KYCStatus = "denied"
)

// Address represents a physical address
type Address struct {
	Line1      string `json:"line1,omitempty"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
	Country    string `json:"country,omitempty"`
}

// KYCData holds Know Your Customer verification data
type KYCData struct {
	Flagged      bool      `json:"flagged,omitempty"`
	Frozen       bool      `json:"frozen,omitempty"`
	DateApproved time.Time `json:"dateApproved,omitempty"`

	WalletAddresses []string `json:"walletAddresses,omitempty"`
	Address         Address  `json:"address,omitempty"`
	Documents       []string `json:"documents,omitempty"`

	TaxID     string    `json:"taxId,omitempty"`
	Phone     string    `json:"phone,omitempty"`
	Birthdate time.Time `json:"birthdate,omitempty"`
	Gender    string    `json:"gender,omitempty"`

	// Blockchain addresses
	EthereumAddress string `json:"ethereumAddress,omitempty"`
	LuxAddress      string `json:"luxAddress,omitempty"`
	EOSPublicKey    string `json:"eosPublicKey,omitempty"`
}

// KYC holds the full KYC verification state
type KYC struct {
	KYCData
	Status KYCStatus `json:"status,omitempty"`
	Hash   string    `json:"hash,omitempty"`
}

// Facebook holds Facebook OAuth data
type Facebook struct {
	AccessToken string `json:"-"`
	UserID      string `json:"userId,omitempty"`
	FirstName   string `json:"firstName,omitempty"`
	LastName    string `json:"lastName,omitempty"`
	MiddleName  string `json:"middleName,omitempty"`
	Name        string `json:"-"`
	NameFormat  string `json:"nameFormat,omitempty"`
	Email       string `json:"-"`
	Verified    bool   `json:"-"`
}

// OAuthProvider represents supported OAuth providers
type OAuthProvider string

const (
	OAuthProviderHanzo    OAuthProvider = "hanzo"
	OAuthProviderGoogle   OAuthProvider = "google"
	OAuthProviderGitHub   OAuthProvider = "github"
	OAuthProviderFacebook OAuthProvider = "facebook"
	OAuthProviderApple    OAuthProvider = "apple"
)

// OAuthToken represents an OAuth2 token from a provider
type OAuthToken struct {
	Provider     OAuthProvider `json:"provider"`
	AccessToken  string        `json:"-"`
	RefreshToken string        `json:"-"`
	TokenType    string        `json:"tokenType,omitempty"`
	ExpiresAt    time.Time     `json:"expiresAt,omitempty"`
	Scope        string        `json:"scope,omitempty"`
	ProviderUID  string        `json:"providerUid,omitempty"`
}

// User represents a user in the commerce system.
// It uses per-user SQLite databases for personal data storage.
type User struct {
	// Core identity
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Deleted   bool      `json:"deleted,omitempty"`

	// Basic info
	Username  string `json:"username,omitempty"`
	Email     string `json:"email"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Company   string `json:"company,omitempty"`
	Phone     string `json:"phone,omitempty"`

	// Addresses
	BillingAddress  Address `json:"billingAddress,omitempty"`
	ShippingAddress Address `json:"shippingAddress,omitempty"`

	// Auth
	PasswordHash []byte `json:"-"`
	Enabled      bool   `json:"enabled"`
	PreApproved  bool   `json:"preApproved,omitempty"`

	// OAuth integration
	OAuthTokens []OAuthToken `json:"-"`
	Facebook    Facebook     `json:"-"`

	// Hanzo.id IAM integration
	HanzoID         string `json:"hanzoId,omitempty"`
	HanzoIDVerified bool   `json:"hanzoIdVerified,omitempty"`

	// KYC verification
	KYC KYC `json:"kyc,omitempty"`

	// Organization membership
	Organizations []string `json:"-"`

	// Commerce
	StoreID    string `json:"storeId,omitempty"`
	ReferrerID string `json:"referrerId,omitempty"`
	FormID     string `json:"formId,omitempty"`

	// Wallet integration (Lux blockchain)
	WalletID         string `json:"walletId,omitempty"`
	WalletPassphrase string `json:"-"`

	// Affiliate
	IsAffiliate bool   `json:"isAffiliate,omitempty"`
	AffiliateID string `json:"affiliateId,omitempty"`

	// Payment
	PaypalEmail string `json:"paypalEmail,omitempty"`

	// Metadata
	Metadata map[string]interface{} `json:"metadata,omitempty"`

	// Flags
	Test    bool `json:"test,omitempty"`
	IsOwner bool `json:"owner,omitempty"`

	// History tracking
	History []Event `json:"-"`
}

// Event represents a historical event for audit trail
type Event struct {
	Type      string                 `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Actor     string                 `json:"actor,omitempty"`
}

// Kind implements db.Entity
func (u *User) Kind() string {
	return Kind
}

// Name returns the user's full name
func (u *User) Name() string {
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

// HasPassword returns true if the user has a password set
func (u *User) HasPassword() bool {
	return len(u.PasswordHash) > 0
}

// ComparePassword compares the given password against the stored hash
func (u *User) ComparePassword(pass string) bool {
	if !u.HasPassword() {
		return false
	}
	return password.HashAndCompare(u.PasswordHash, pass)
}

// SetPassword hashes and stores a new password
func (u *User) SetPassword(newPassword string) error {
	hash, err := password.Hash(newPassword)
	if err != nil {
		return err
	}
	u.PasswordHash = hash
	return nil
}

// InOrganization checks if the user belongs to an organization
func (u *User) InOrganization(orgID string) bool {
	for _, org := range u.Organizations {
		if org == orgID {
			return true
		}
	}
	return false
}

// AddOrganization adds an organization to the user's membership
func (u *User) AddOrganization(orgID string) {
	if !u.InOrganization(orgID) {
		u.Organizations = append(u.Organizations, orgID)
	}
}

// RemoveOrganization removes an organization from the user's membership
func (u *User) RemoveOrganization(orgID string) {
	orgs := make([]string, 0, len(u.Organizations))
	for _, org := range u.Organizations {
		if org != orgID {
			orgs = append(orgs, org)
		}
	}
	u.Organizations = orgs
}

// Buyer returns buyer information for orders
func (u *User) Buyer() Buyer {
	return Buyer{
		Email:           u.Email,
		UserID:          u.ID,
		FirstName:       u.FirstName,
		LastName:        u.LastName,
		Company:         u.Company,
		Phone:           u.Phone,
		ShippingAddress: u.ShippingAddress,
		BillingAddress:  u.BillingAddress,
	}
}

// Buyer represents buyer information for an order
type Buyer struct {
	Email           string  `json:"email"`
	UserID          string  `json:"userId"`
	FirstName       string  `json:"firstName"`
	LastName        string  `json:"lastName"`
	Company         string  `json:"company,omitempty"`
	Phone           string  `json:"phone,omitempty"`
	ShippingAddress Address `json:"shippingAddress,omitempty"`
	BillingAddress  Address `json:"billingAddress,omitempty"`
}

// AddEvent adds a historical event to the user
func (u *User) AddEvent(eventType string, data map[string]interface{}, actor string) {
	u.History = append(u.History, Event{
		Type:      eventType,
		Timestamp: time.Now(),
		Data:      data,
		Actor:     actor,
	})
}

// SetMetadata sets a metadata key-value pair
func (u *User) SetMetadata(key string, value interface{}) {
	if u.Metadata == nil {
		u.Metadata = make(map[string]interface{})
	}
	u.Metadata[key] = value
}

// GetMetadata gets a metadata value by key
func (u *User) GetMetadata(key string) (interface{}, bool) {
	if u.Metadata == nil {
		return nil, false
	}
	v, ok := u.Metadata[key]
	return v, ok
}

// IsKYCApproved returns true if the user has completed KYC verification
func (u *User) IsKYCApproved() bool {
	return u.KYC.Status == KYCStatusApproved
}

// IsKYCRequired returns true if KYC verification is required but not completed
func (u *User) IsKYCRequired() bool {
	return u.KYC.Status == KYCStatusInitiated || u.KYC.Status == KYCStatusPending
}

// HasHanzoID returns true if the user is linked to hanzo.id
func (u *User) HasHanzoID() bool {
	return u.HanzoID != "" && u.HanzoIDVerified
}

// GetOAuthToken returns the OAuth token for a provider
func (u *User) GetOAuthToken(provider OAuthProvider) *OAuthToken {
	for i := range u.OAuthTokens {
		if u.OAuthTokens[i].Provider == provider {
			return &u.OAuthTokens[i]
		}
	}
	return nil
}

// SetOAuthToken sets or updates an OAuth token
func (u *User) SetOAuthToken(token OAuthToken) {
	for i := range u.OAuthTokens {
		if u.OAuthTokens[i].Provider == token.Provider {
			u.OAuthTokens[i] = token
			return
		}
	}
	u.OAuthTokens = append(u.OAuthTokens, token)
}

// RemoveOAuthToken removes an OAuth token for a provider
func (u *User) RemoveOAuthToken(provider OAuthProvider) {
	tokens := make([]OAuthToken, 0, len(u.OAuthTokens))
	for _, t := range u.OAuthTokens {
		if t.Provider != provider {
			tokens = append(tokens, t)
		}
	}
	u.OAuthTokens = tokens
}

// Validate validates the user data
func (u *User) Validate() error {
	if u.Email == "" {
		return ErrInvalidEmail
	}
	u.Email = strings.ToLower(strings.TrimSpace(u.Email))
	if !isValidEmail(u.Email) {
		return ErrInvalidEmail
	}
	return nil
}

// SyncToDatastore returns true - users should be synced to analytics
func (u *User) SyncToDatastore() bool {
	return true
}

// MarshalJSON customizes JSON marshaling
func (u *User) MarshalJSON() ([]byte, error) {
	type Alias User
	return json.Marshal(&struct {
		*Alias
		// Exclude sensitive fields
		PasswordHash []byte       `json:"-"`
		OAuthTokens  []OAuthToken `json:"-"`
	}{
		Alias: (*Alias)(u),
	})
}

// isValidEmail performs basic email validation
func isValidEmail(email string) bool {
	// Basic validation - contains @ and has domain
	parts := strings.Split(email, "@")
	if len(parts) != 2 {
		return false
	}
	if len(parts[0]) == 0 || len(parts[1]) == 0 {
		return false
	}
	if !strings.Contains(parts[1], ".") {
		return false
	}
	return true
}

// Repository provides data access for User entities
type Repository struct {
	db db.DB
}

// NewRepository creates a new User repository
func NewRepository(database db.DB) *Repository {
	return &Repository{db: database}
}

// Create creates a new user
func (r *Repository) Create(ctx context.Context, user *User) error {
	if err := user.Validate(); err != nil {
		return err
	}

	// Sanitize email
	user.Email = strings.ToLower(strings.TrimSpace(user.Email))

	// Check if email already exists
	existing, err := r.GetByEmail(ctx, user.Email)
	if err != nil && !errors.Is(err, ErrUserNotFound) {
		return err
	}
	if existing != nil {
		return ErrEmailExists
	}

	// Check if username exists (if provided)
	if user.Username != "" {
		existing, err = r.GetByUsername(ctx, user.Username)
		if err != nil && !errors.Is(err, ErrUserNotFound) {
			return err
		}
		if existing != nil {
			return ErrUsernameExists
		}
	}

	now := time.Now()
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now
	}
	user.UpdatedAt = now

	// Initialize defaults
	if user.Metadata == nil {
		user.Metadata = make(map[string]interface{})
	}
	if user.History == nil {
		user.History = make([]Event, 0)
	}
	if user.Organizations == nil {
		user.Organizations = make([]string, 0)
	}
	if user.KYC.Status == "" {
		user.KYC.Status = KYCStatusInitiated
	}
	if user.KYC.Documents == nil {
		user.KYC.Documents = make([]string, 0)
	}

	// Generate ID if not set
	if user.ID == "" {
		key := r.db.NewIncompleteKey(Kind, nil)
		user.ID = key.Encode()
	}

	key := r.db.NewKey(Kind, user.ID, 0, nil)
	_, err = r.db.Put(ctx, key, user)
	if err != nil {
		return err
	}

	// Add creation event
	user.AddEvent("created", nil, "system")

	return nil
}

// Get retrieves a user by ID
func (r *Repository) Get(ctx context.Context, id string) (*User, error) {
	key := r.db.NewKey(Kind, id, 0, nil)
	user := &User{}

	if err := r.db.Get(ctx, key, user); err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	return user, nil
}

// Update updates an existing user
func (r *Repository) Update(ctx context.Context, user *User) error {
	if err := user.Validate(); err != nil {
		return err
	}

	user.Email = strings.ToLower(strings.TrimSpace(user.Email))
	user.UpdatedAt = time.Now()

	key := r.db.NewKey(Kind, user.ID, 0, nil)
	_, err := r.db.Put(ctx, key, user)
	return err
}

// Delete soft-deletes a user
func (r *Repository) Delete(ctx context.Context, id string) error {
	user, err := r.Get(ctx, id)
	if err != nil {
		return err
	}

	user.Deleted = true
	user.UpdatedAt = time.Now()
	user.AddEvent("deleted", nil, "system")

	return r.Update(ctx, user)
}

// HardDelete permanently deletes a user
func (r *Repository) HardDelete(ctx context.Context, id string) error {
	key := r.db.NewKey(Kind, id, 0, nil)
	return r.db.Delete(ctx, key)
}

// GetByEmail retrieves a user by email
func (r *Repository) GetByEmail(ctx context.Context, email string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	user := &User{}
	_, err := r.db.Query(Kind).Filter("Email=", email).First(ctx, user)
	if err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	return user, nil
}

// GetByUsername retrieves a user by username
func (r *Repository) GetByUsername(ctx context.Context, username string) (*User, error) {
	username = strings.ToLower(strings.TrimSpace(username))

	user := &User{}
	_, err := r.db.Query(Kind).Filter("Username=", username).First(ctx, user)
	if err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	return user, nil
}

// GetByHanzoID retrieves a user by their hanzo.id
func (r *Repository) GetByHanzoID(ctx context.Context, hanzoID string) (*User, error) {
	user := &User{}
	_, err := r.db.Query(Kind).Filter("HanzoID=", hanzoID).First(ctx, user)
	if err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	return user, nil
}

// List retrieves users with pagination
func (r *Repository) List(ctx context.Context, opts *ListOptions) ([]*User, error) {
	if opts == nil {
		opts = &ListOptions{Limit: 100}
	}

	query := r.db.Query(Kind)

	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}
	if opts.OrderBy != "" {
		if opts.Descending {
			query = query.OrderDesc(opts.OrderBy)
		} else {
			query = query.Order(opts.OrderBy)
		}
	}

	// Apply filters
	for field, value := range opts.Filters {
		query = query.Filter(field+"=", value)
	}

	var users []*User
	_, err := query.GetAll(ctx, &users)
	if err != nil {
		return nil, err
	}

	return users, nil
}

// ListByOrganization retrieves users belonging to an organization
func (r *Repository) ListByOrganization(ctx context.Context, orgID string, opts *ListOptions) ([]*User, error) {
	if opts == nil {
		opts = &ListOptions{Limit: 100}
	}
	if opts.Filters == nil {
		opts.Filters = make(map[string]interface{})
	}

	// Note: This requires array-contains support in the query layer
	// For now, we fetch all and filter in memory
	allUsers, err := r.List(ctx, &ListOptions{Limit: 10000})
	if err != nil {
		return nil, err
	}

	var result []*User
	for _, user := range allUsers {
		if user.InOrganization(orgID) {
			result = append(result, user)
			if opts.Limit > 0 && len(result) >= opts.Limit {
				break
			}
		}
	}

	return result, nil
}

// Count returns the total number of users
func (r *Repository) Count(ctx context.Context) (int, error) {
	return r.db.Query(Kind).Count(ctx)
}

// Authenticate validates credentials and returns the user
func (r *Repository) Authenticate(ctx context.Context, email, password string) (*User, error) {
	user, err := r.GetByEmail(ctx, email)
	if err != nil {
		return nil, ErrUnauthorized
	}

	if !user.Enabled {
		return nil, ErrAccountDisabled
	}

	if !user.ComparePassword(password) {
		return nil, ErrUnauthorized
	}

	return user, nil
}

// ListOptions for paginated queries
type ListOptions struct {
	Limit      int
	Offset     int
	OrderBy    string
	Descending bool
	Filters    map[string]interface{}
}
