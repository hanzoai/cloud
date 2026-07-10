package user

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/db"
)

// Service provides high-level user operations.
// It manages the database connection and provides factory methods.
type Service struct {
	manager *db.Manager
}

// NewService creates a new user service with the database manager
func NewService(manager *db.Manager) *Service {
	return &Service{manager: manager}
}

// UserDB returns a database for a specific user.
// This provides access to the user's personal SQLite database.
func (s *Service) UserDB(userID string) (db.DB, error) {
	return s.manager.User(userID)
}

// OrgDB returns a database for an organization.
// This provides access to the organization's shared SQLite database.
func (s *Service) OrgDB(orgID string) (db.DB, error) {
	return s.manager.Org(orgID)
}

// Repository returns a user repository for the given database
func (s *Service) Repository(database db.DB) *Repository {
	return NewRepository(database)
}

// Get retrieves a user by ID using their personal database
func (s *Service) Get(ctx context.Context, userID string) (*User, error) {
	database, err := s.UserDB(userID)
	if err != nil {
		return nil, err
	}

	repo := s.Repository(database)
	return repo.Get(ctx, userID)
}

// Create creates a new user and initializes their personal database
func (s *Service) Create(ctx context.Context, user *User) error {
	// First, create the user in a system-level database or determine their ID
	if user.ID == "" {
		// Generate a new user ID
		tmpDB, err := s.manager.User("_system")
		if err != nil {
			return err
		}
		key := tmpDB.NewIncompleteKey(Kind, nil)
		user.ID = key.Encode()
	}

	// Now get/create the user's personal database
	database, err := s.UserDB(user.ID)
	if err != nil {
		return err
	}

	repo := s.Repository(database)
	return repo.Create(ctx, user)
}

// Update updates an existing user in their personal database
func (s *Service) Update(ctx context.Context, user *User) error {
	database, err := s.UserDB(user.ID)
	if err != nil {
		return err
	}

	repo := s.Repository(database)
	return repo.Update(ctx, user)
}

// Delete soft-deletes a user
func (s *Service) Delete(ctx context.Context, userID string) error {
	database, err := s.UserDB(userID)
	if err != nil {
		return err
	}

	repo := s.Repository(database)
	return repo.Delete(ctx, userID)
}

// Authenticate validates credentials against the user's database
func (s *Service) Authenticate(ctx context.Context, email, password string) (*User, error) {
	// For authentication, we need to find the user first
	// This requires a lookup in a global index or system database

	// First, try to find the user by email in the system database
	sysDB, err := s.manager.User("_system")
	if err != nil {
		return nil, err
	}

	// Look up user ID by email in the system index
	index := &UserEmailIndex{}
	_, err = sysDB.Query("user_email_index").Filter("Email=", email).First(ctx, index)
	if err != nil {
		return nil, ErrUnauthorized
	}

	// Now get the user from their personal database
	userDB, err := s.UserDB(index.UserID)
	if err != nil {
		return nil, err
	}

	repo := s.Repository(userDB)
	return repo.Authenticate(ctx, email, password)
}

// UserEmailIndex is stored in the system database for email lookups
type UserEmailIndex struct {
	Email  string `json:"email"`
	UserID string `json:"userId"`
}

// Kind implements db.Entity
func (i *UserEmailIndex) Kind() string {
	return "user_email_index"
}

// New creates a new User with default values
func New() *User {
	return &User{
		Enabled:       true,
		Metadata:      make(map[string]interface{}),
		History:       make([]Event, 0),
		Organizations: make([]string, 0),
		OAuthTokens:   make([]OAuthToken, 0),
		KYC: KYC{
			Status: KYCStatusInitiated,
			KYCData: KYCData{
				Documents:       make([]string, 0),
				WalletAddresses: make([]string, 0),
			},
		},
	}
}

// NewWithEmail creates a new User with the given email
func NewWithEmail(email string) *User {
	user := New()
	user.Email = email
	return user
}

// NewFromHanzoID creates a new User linked to a hanzo.id account
func NewFromHanzoID(hanzoID string, email string) *User {
	user := NewWithEmail(email)
	user.HanzoID = hanzoID
	user.HanzoIDVerified = true
	return user
}

// Clone creates a deep copy of the user
func (u *User) Clone() *User {
	clone := *u

	// Deep copy slices
	if u.Organizations != nil {
		clone.Organizations = make([]string, len(u.Organizations))
		copy(clone.Organizations, u.Organizations)
	}

	if u.OAuthTokens != nil {
		clone.OAuthTokens = make([]OAuthToken, len(u.OAuthTokens))
		copy(clone.OAuthTokens, u.OAuthTokens)
	}

	if u.History != nil {
		clone.History = make([]Event, len(u.History))
		copy(clone.History, u.History)
	}

	if u.Metadata != nil {
		clone.Metadata = make(map[string]interface{})
		for k, v := range u.Metadata {
			clone.Metadata[k] = v
		}
	}

	if u.KYC.Documents != nil {
		clone.KYC.Documents = make([]string, len(u.KYC.Documents))
		copy(clone.KYC.Documents, u.KYC.Documents)
	}

	if u.KYC.WalletAddresses != nil {
		clone.KYC.WalletAddresses = make([]string, len(u.KYC.WalletAddresses))
		copy(clone.KYC.WalletAddresses, u.KYC.WalletAddresses)
	}

	return &clone
}

// Merge updates the user with non-zero values from another user
func (u *User) Merge(other *User) {
	if other.Username != "" {
		u.Username = other.Username
	}
	if other.Email != "" {
		u.Email = other.Email
	}
	if other.FirstName != "" {
		u.FirstName = other.FirstName
	}
	if other.LastName != "" {
		u.LastName = other.LastName
	}
	if other.Company != "" {
		u.Company = other.Company
	}
	if other.Phone != "" {
		u.Phone = other.Phone
	}

	// Merge addresses if any field is set
	if other.BillingAddress.Line1 != "" || other.BillingAddress.City != "" {
		u.BillingAddress = other.BillingAddress
	}
	if other.ShippingAddress.Line1 != "" || other.ShippingAddress.City != "" {
		u.ShippingAddress = other.ShippingAddress
	}

	// Merge metadata
	for k, v := range other.Metadata {
		u.SetMetadata(k, v)
	}
}

// Query is a helper to create queries against the user kind
func Query(database db.DB) db.Query {
	return database.Query(Kind)
}
