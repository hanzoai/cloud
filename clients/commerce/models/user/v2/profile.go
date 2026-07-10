package user

import (
	"context"
	"errors"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/db"
)

// Profile errors
var (
	ErrProfileNotFound     = errors.New("profile: not found")
	ErrProfileInvalidField = errors.New("profile: invalid field")
	ErrAvatarTooLarge      = errors.New("profile: avatar too large")
)

// Profile represents extended user profile information
type Profile struct {
	UserID string `json:"userId"`

	// Display info
	DisplayName string `json:"displayName,omitempty"`
	Bio         string `json:"bio,omitempty"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	CoverURL    string `json:"coverUrl,omitempty"`

	// Contact preferences
	PreferredLanguage string `json:"preferredLanguage,omitempty"`
	Timezone          string `json:"timezone,omitempty"`
	Currency          string `json:"currency,omitempty"`

	// Social links
	Website  string `json:"website,omitempty"`
	Twitter  string `json:"twitter,omitempty"`
	GitHub   string `json:"github,omitempty"`
	LinkedIn string `json:"linkedin,omitempty"`
	Discord  string `json:"discord,omitempty"`
	Telegram string `json:"telegram,omitempty"`

	// Professional info
	JobTitle   string   `json:"jobTitle,omitempty"`
	Department string   `json:"department,omitempty"`
	Skills     []string `json:"skills,omitempty"`
	Interests  []string `json:"interests,omitempty"`

	// Verification badges
	Badges []Badge `json:"badges,omitempty"`

	// Activity tracking
	LastActiveAt time.Time `json:"lastActiveAt,omitempty"`
	JoinedAt     time.Time `json:"joinedAt,omitempty"`

	// Stats (cached)
	OrderCount    int `json:"orderCount,omitempty"`
	ReferralCount int `json:"referralCount,omitempty"`
	ReviewCount   int `json:"reviewCount,omitempty"`
	TotalSpent    int `json:"totalSpent,omitempty"` // In cents
	LoyaltyPoints int `json:"loyaltyPoints,omitempty"`

	// Privacy settings
	ProfilePublic     bool `json:"profilePublic,omitempty"`
	ShowEmail         bool `json:"showEmail,omitempty"`
	ShowWalletAddress bool `json:"showWalletAddress,omitempty"`
	ShowActivity      bool `json:"showActivity,omitempty"`

	// Timestamps
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Badge represents an achievement or verification badge
type Badge struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	IconURL     string    `json:"iconUrl,omitempty"`
	AwardedAt   time.Time `json:"awardedAt"`
	ExpiresAt   time.Time `json:"expiresAt,omitempty"`
}

// Kind implements db.Entity
func (p *Profile) Kind() string {
	return "profile"
}

// ProfileRepository provides data access for Profile entities
type ProfileRepository struct {
	db db.DB
}

// NewProfileRepository creates a new Profile repository
func NewProfileRepository(database db.DB) *ProfileRepository {
	return &ProfileRepository{db: database}
}

// Get retrieves a profile by user ID
func (r *ProfileRepository) Get(ctx context.Context, userID string) (*Profile, error) {
	key := r.db.NewKey("profile", userID, 0, nil)
	profile := &Profile{}

	if err := r.db.Get(ctx, key, profile); err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return nil, ErrProfileNotFound
		}
		return nil, err
	}

	return profile, nil
}

// GetOrCreate retrieves a profile or creates a default one
func (r *ProfileRepository) GetOrCreate(ctx context.Context, userID string) (*Profile, error) {
	profile, err := r.Get(ctx, userID)
	if err == nil {
		return profile, nil
	}

	if !errors.Is(err, ErrProfileNotFound) {
		return nil, err
	}

	// Create default profile
	profile = &Profile{
		UserID:        userID,
		ProfilePublic: false,
		ShowEmail:     false,
		JoinedAt:      time.Now(),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		Badges:        make([]Badge, 0),
		Skills:        make([]string, 0),
		Interests:     make([]string, 0),
	}

	if err := r.Create(ctx, profile); err != nil {
		return nil, err
	}

	return profile, nil
}

// Create creates a new profile
func (r *ProfileRepository) Create(ctx context.Context, profile *Profile) error {
	if profile.UserID == "" {
		return ErrProfileInvalidField
	}

	now := time.Now()
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = now
	}
	profile.UpdatedAt = now

	if profile.Badges == nil {
		profile.Badges = make([]Badge, 0)
	}
	if profile.Skills == nil {
		profile.Skills = make([]string, 0)
	}
	if profile.Interests == nil {
		profile.Interests = make([]string, 0)
	}

	key := r.db.NewKey("profile", profile.UserID, 0, nil)
	_, err := r.db.Put(ctx, key, profile)
	return err
}

// Update updates an existing profile
func (r *ProfileRepository) Update(ctx context.Context, profile *Profile) error {
	profile.UpdatedAt = time.Now()

	key := r.db.NewKey("profile", profile.UserID, 0, nil)
	_, err := r.db.Put(ctx, key, profile)
	return err
}

// Delete deletes a profile
func (r *ProfileRepository) Delete(ctx context.Context, userID string) error {
	key := r.db.NewKey("profile", userID, 0, nil)
	return r.db.Delete(ctx, key)
}

// UpdateActivity updates the last active timestamp
func (r *ProfileRepository) UpdateActivity(ctx context.Context, userID string) error {
	profile, err := r.GetOrCreate(ctx, userID)
	if err != nil {
		return err
	}

	profile.LastActiveAt = time.Now()
	return r.Update(ctx, profile)
}

// AddBadge adds a badge to the profile
func (r *ProfileRepository) AddBadge(ctx context.Context, userID string, badge Badge) error {
	profile, err := r.GetOrCreate(ctx, userID)
	if err != nil {
		return err
	}

	badge.AwardedAt = time.Now()
	profile.Badges = append(profile.Badges, badge)

	return r.Update(ctx, profile)
}

// RemoveBadge removes a badge from the profile
func (r *ProfileRepository) RemoveBadge(ctx context.Context, userID string, badgeID string) error {
	profile, err := r.Get(ctx, userID)
	if err != nil {
		return err
	}

	badges := make([]Badge, 0, len(profile.Badges))
	for _, b := range profile.Badges {
		if b.ID != badgeID {
			badges = append(badges, b)
		}
	}
	profile.Badges = badges

	return r.Update(ctx, profile)
}

// UpdateStats updates cached statistics
func (r *ProfileRepository) UpdateStats(ctx context.Context, userID string, stats *ProfileStats) error {
	profile, err := r.GetOrCreate(ctx, userID)
	if err != nil {
		return err
	}

	if stats.OrderCount >= 0 {
		profile.OrderCount = stats.OrderCount
	}
	if stats.ReferralCount >= 0 {
		profile.ReferralCount = stats.ReferralCount
	}
	if stats.ReviewCount >= 0 {
		profile.ReviewCount = stats.ReviewCount
	}
	if stats.TotalSpent >= 0 {
		profile.TotalSpent = stats.TotalSpent
	}
	if stats.LoyaltyPoints >= 0 {
		profile.LoyaltyPoints = stats.LoyaltyPoints
	}

	return r.Update(ctx, profile)
}

// ProfileStats holds stats to update
type ProfileStats struct {
	OrderCount    int
	ReferralCount int
	ReviewCount   int
	TotalSpent    int
	LoyaltyPoints int
}

// PublicProfile returns a sanitized profile for public viewing
type PublicProfile struct {
	UserID      string    `json:"userId"`
	DisplayName string    `json:"displayName,omitempty"`
	Bio         string    `json:"bio,omitempty"`
	AvatarURL   string    `json:"avatarUrl,omitempty"`
	Website     string    `json:"website,omitempty"`
	Twitter     string    `json:"twitter,omitempty"`
	GitHub      string    `json:"github,omitempty"`
	Badges      []Badge   `json:"badges,omitempty"`
	JoinedAt    time.Time `json:"joinedAt,omitempty"`
}

// ToPublic converts a profile to its public representation
func (p *Profile) ToPublic() *PublicProfile {
	if !p.ProfilePublic {
		return &PublicProfile{
			UserID: p.UserID,
		}
	}

	return &PublicProfile{
		UserID:      p.UserID,
		DisplayName: p.DisplayName,
		Bio:         p.Bio,
		AvatarURL:   p.AvatarURL,
		Website:     p.Website,
		Twitter:     p.Twitter,
		GitHub:      p.GitHub,
		Badges:      p.Badges,
		JoinedAt:    p.JoinedAt,
	}
}

// ProfileService provides high-level profile operations
type ProfileService struct {
	service *Service
}

// NewProfileService creates a new profile service
func NewProfileService(service *Service) *ProfileService {
	return &ProfileService{service: service}
}

// Get retrieves a user's profile
func (s *ProfileService) Get(ctx context.Context, userID string) (*Profile, error) {
	database, err := s.service.UserDB(userID)
	if err != nil {
		return nil, err
	}

	repo := NewProfileRepository(database)
	return repo.GetOrCreate(ctx, userID)
}

// Update updates a user's profile
func (s *ProfileService) Update(ctx context.Context, profile *Profile) error {
	database, err := s.service.UserDB(profile.UserID)
	if err != nil {
		return err
	}

	repo := NewProfileRepository(database)
	return repo.Update(ctx, profile)
}

// GetPublic retrieves a public profile
func (s *ProfileService) GetPublic(ctx context.Context, userID string) (*PublicProfile, error) {
	profile, err := s.Get(ctx, userID)
	if err != nil {
		return nil, err
	}

	return profile.ToPublic(), nil
}
