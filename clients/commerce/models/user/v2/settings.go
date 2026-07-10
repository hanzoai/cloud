package user

import (
	"context"
	"errors"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/db"
)

// Settings errors
var (
	ErrSettingsNotFound = errors.New("settings: not found")
)

// Settings represents user preferences and configuration
type Settings struct {
	UserID string `json:"userId"`

	// Notification preferences
	Notifications NotificationSettings `json:"notifications"`

	// Privacy settings
	Privacy PrivacySettings `json:"privacy"`

	// Commerce preferences
	Commerce CommerceSettings `json:"commerce"`

	// Display preferences
	Display DisplaySettings `json:"display"`

	// Security settings
	Security SecuritySettings `json:"security"`

	// API/Developer settings
	Developer DeveloperSettings `json:"developer,omitempty"`

	// Timestamps
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// NotificationSettings controls notification delivery
type NotificationSettings struct {
	// Email notifications
	EmailEnabled      bool `json:"emailEnabled"`
	OrderUpdates      bool `json:"orderUpdates"`
	ShippingUpdates   bool `json:"shippingUpdates"`
	PromotionalEmails bool `json:"promotionalEmails"`
	Newsletter        bool `json:"newsletter"`
	SecurityAlerts    bool `json:"securityAlerts"`

	// Push notifications
	PushEnabled      bool `json:"pushEnabled"`
	PushOrderUpdates bool `json:"pushOrderUpdates"`
	PushPromos       bool `json:"pushPromos"`

	// SMS notifications
	SMSEnabled        bool `json:"smsEnabled"`
	SMSOrderUpdates   bool `json:"smsOrderUpdates"`
	SMSSecurityAlerts bool `json:"smsSecurityAlerts"`

	// Digest preferences
	DigestFrequency string `json:"digestFrequency,omitempty"` // "daily", "weekly", "never"
}

// PrivacySettings controls data privacy options
type PrivacySettings struct {
	// Profile visibility
	ProfileVisible   bool `json:"profileVisible"`
	ShowOrderHistory bool `json:"showOrderHistory"`
	ShowWishlist     bool `json:"showWishlist"`
	ShowReviews      bool `json:"showReviews"`

	// Data sharing
	ShareAnalytics       bool `json:"shareAnalytics"`
	AllowPersonalization bool `json:"allowPersonalization"`
	AllowThirdParty      bool `json:"allowThirdParty"`

	// Activity tracking
	TrackActivity       bool `json:"trackActivity"`
	SaveBrowsingHistory bool `json:"saveBrowsingHistory"`
	SaveSearchHistory   bool `json:"saveSearchHistory"`
}

// CommerceSettings controls shopping preferences
type CommerceSettings struct {
	// Default addresses
	DefaultShippingAddressID string `json:"defaultShippingAddressId,omitempty"`
	DefaultBillingAddressID  string `json:"defaultBillingAddressId,omitempty"`

	// Payment preferences
	DefaultPaymentMethodID string `json:"defaultPaymentMethodId,omitempty"`
	SavePaymentMethods     bool   `json:"savePaymentMethods"`
	AutoApplyRewards       bool   `json:"autoApplyRewards"`

	// Currency and locale
	PreferredCurrency string `json:"preferredCurrency,omitempty"`
	PreferredLanguage string `json:"preferredLanguage,omitempty"`

	// Shopping preferences
	ShowPricesWithTax bool `json:"showPricesWithTax"`
	EnableOneClickBuy bool `json:"enableOneClickBuy"`
	SaveCartOnLogout  bool `json:"saveCartOnLogout"`

	// Subscription preferences
	AutoRenewSubscriptions bool `json:"autoRenewSubscriptions"`
}

// DisplaySettings controls UI preferences
type DisplaySettings struct {
	Theme         string `json:"theme,omitempty"`       // "light", "dark", "system"
	ColorScheme   string `json:"colorScheme,omitempty"` // Custom color scheme
	CompactMode   bool   `json:"compactMode"`
	HighContrast  bool   `json:"highContrast"`
	ReducedMotion bool   `json:"reducedMotion"`
	FontSize      string `json:"fontSize,omitempty"` // "small", "medium", "large"

	// Dashboard preferences
	DashboardLayout string   `json:"dashboardLayout,omitempty"`
	VisibleWidgets  []string `json:"visibleWidgets,omitempty"`
	DefaultView     string   `json:"defaultView,omitempty"` // "grid", "list"
}

// SecuritySettings controls account security
type SecuritySettings struct {
	// Two-factor authentication
	TwoFactorEnabled bool   `json:"twoFactorEnabled"`
	TwoFactorMethod  string `json:"twoFactorMethod,omitempty"` // "totp", "sms", "email"

	// Session management
	SessionTimeout    int  `json:"sessionTimeout,omitempty"` // Minutes
	RememberMe        bool `json:"rememberMe"`
	SingleSessionOnly bool `json:"singleSessionOnly"`

	// Login security
	RequirePasswordChange bool      `json:"requirePasswordChange"`
	LastPasswordChange    time.Time `json:"lastPasswordChange,omitempty"`
	LoginNotifications    bool      `json:"loginNotifications"`

	// Trusted devices
	TrustedDevices []TrustedDevice `json:"trustedDevices,omitempty"`

	// Recovery options
	RecoveryEmail string `json:"recoveryEmail,omitempty"`
	RecoveryPhone string `json:"recoveryPhone,omitempty"`
}

// TrustedDevice represents a device trusted for login
type TrustedDevice struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	DeviceType string    `json:"deviceType"` // "desktop", "mobile", "tablet"
	Browser    string    `json:"browser,omitempty"`
	OS         string    `json:"os,omitempty"`
	LastUsed   time.Time `json:"lastUsed"`
	AddedAt    time.Time `json:"addedAt"`
	IPAddress  string    `json:"ipAddress,omitempty"`
}

// DeveloperSettings for API access
type DeveloperSettings struct {
	APIEnabled    bool     `json:"apiEnabled"`
	WebhookURL    string   `json:"webhookUrl,omitempty"`
	WebhookSecret string   `json:"-"`
	APIKeys       []APIKey `json:"apiKeys,omitempty"`
	RateLimit     int      `json:"rateLimit,omitempty"` // Requests per minute
}

// APIKey represents a user API key
type APIKey struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	KeyPrefix   string    `json:"keyPrefix"` // First 8 chars for identification
	KeyHash     []byte    `json:"-"`
	Permissions []string  `json:"permissions,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	ExpiresAt   time.Time `json:"expiresAt,omitempty"`
	LastUsedAt  time.Time `json:"lastUsedAt,omitempty"`
}

// Kind implements db.Entity
func (s *Settings) Kind() string {
	return "settings"
}

// SettingsRepository provides data access for Settings entities
type SettingsRepository struct {
	db db.DB
}

// NewSettingsRepository creates a new Settings repository
func NewSettingsRepository(database db.DB) *SettingsRepository {
	return &SettingsRepository{db: database}
}

// DefaultSettings returns default settings for a new user
func DefaultSettings(userID string) *Settings {
	now := time.Now()
	return &Settings{
		UserID: userID,
		Notifications: NotificationSettings{
			EmailEnabled:      true,
			OrderUpdates:      true,
			ShippingUpdates:   true,
			PromotionalEmails: false,
			Newsletter:        false,
			SecurityAlerts:    true,
			PushEnabled:       false,
			SMSEnabled:        false,
			DigestFrequency:   "weekly",
		},
		Privacy: PrivacySettings{
			ProfileVisible:       false,
			ShowOrderHistory:     false,
			ShowWishlist:         false,
			ShowReviews:          true,
			ShareAnalytics:       false,
			AllowPersonalization: true,
			AllowThirdParty:      false,
			TrackActivity:        true,
			SaveBrowsingHistory:  true,
			SaveSearchHistory:    true,
		},
		Commerce: CommerceSettings{
			SavePaymentMethods:     true,
			AutoApplyRewards:       true,
			ShowPricesWithTax:      true,
			EnableOneClickBuy:      false,
			SaveCartOnLogout:       true,
			AutoRenewSubscriptions: true,
		},
		Display: DisplaySettings{
			Theme:         "system",
			CompactMode:   false,
			HighContrast:  false,
			ReducedMotion: false,
			FontSize:      "medium",
			DefaultView:   "grid",
		},
		Security: SecuritySettings{
			TwoFactorEnabled:   false,
			SessionTimeout:     60,
			RememberMe:         true,
			SingleSessionOnly:  false,
			LoginNotifications: true,
			TrustedDevices:     make([]TrustedDevice, 0),
		},
		Developer: DeveloperSettings{
			APIEnabled: false,
			APIKeys:    make([]APIKey, 0),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// Get retrieves settings by user ID
func (r *SettingsRepository) Get(ctx context.Context, userID string) (*Settings, error) {
	key := r.db.NewKey("settings", userID, 0, nil)
	settings := &Settings{}

	if err := r.db.Get(ctx, key, settings); err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return nil, ErrSettingsNotFound
		}
		return nil, err
	}

	return settings, nil
}

// GetOrCreate retrieves settings or creates defaults
func (r *SettingsRepository) GetOrCreate(ctx context.Context, userID string) (*Settings, error) {
	settings, err := r.Get(ctx, userID)
	if err == nil {
		return settings, nil
	}

	if !errors.Is(err, ErrSettingsNotFound) {
		return nil, err
	}

	settings = DefaultSettings(userID)
	if err := r.Create(ctx, settings); err != nil {
		return nil, err
	}

	return settings, nil
}

// Create creates new settings
func (r *SettingsRepository) Create(ctx context.Context, settings *Settings) error {
	now := time.Now()
	if settings.CreatedAt.IsZero() {
		settings.CreatedAt = now
	}
	settings.UpdatedAt = now

	key := r.db.NewKey("settings", settings.UserID, 0, nil)
	_, err := r.db.Put(ctx, key, settings)
	return err
}

// Update updates existing settings
func (r *SettingsRepository) Update(ctx context.Context, settings *Settings) error {
	settings.UpdatedAt = time.Now()

	key := r.db.NewKey("settings", settings.UserID, 0, nil)
	_, err := r.db.Put(ctx, key, settings)
	return err
}

// Delete deletes settings
func (r *SettingsRepository) Delete(ctx context.Context, userID string) error {
	key := r.db.NewKey("settings", userID, 0, nil)
	return r.db.Delete(ctx, key)
}

// AddTrustedDevice adds a trusted device
func (r *SettingsRepository) AddTrustedDevice(ctx context.Context, userID string, device TrustedDevice) error {
	settings, err := r.GetOrCreate(ctx, userID)
	if err != nil {
		return err
	}

	device.AddedAt = time.Now()
	device.LastUsed = time.Now()
	settings.Security.TrustedDevices = append(settings.Security.TrustedDevices, device)

	return r.Update(ctx, settings)
}

// RemoveTrustedDevice removes a trusted device
func (r *SettingsRepository) RemoveTrustedDevice(ctx context.Context, userID string, deviceID string) error {
	settings, err := r.Get(ctx, userID)
	if err != nil {
		return err
	}

	devices := make([]TrustedDevice, 0, len(settings.Security.TrustedDevices))
	for _, d := range settings.Security.TrustedDevices {
		if d.ID != deviceID {
			devices = append(devices, d)
		}
	}
	settings.Security.TrustedDevices = devices

	return r.Update(ctx, settings)
}

// AddAPIKey adds an API key
func (r *SettingsRepository) AddAPIKey(ctx context.Context, userID string, key APIKey) error {
	settings, err := r.GetOrCreate(ctx, userID)
	if err != nil {
		return err
	}

	key.CreatedAt = time.Now()
	settings.Developer.APIKeys = append(settings.Developer.APIKeys, key)

	return r.Update(ctx, settings)
}

// RemoveAPIKey removes an API key
func (r *SettingsRepository) RemoveAPIKey(ctx context.Context, userID string, keyID string) error {
	settings, err := r.Get(ctx, userID)
	if err != nil {
		return err
	}

	keys := make([]APIKey, 0, len(settings.Developer.APIKeys))
	for _, k := range settings.Developer.APIKeys {
		if k.ID != keyID {
			keys = append(keys, k)
		}
	}
	settings.Developer.APIKeys = keys

	return r.Update(ctx, settings)
}

// SettingsService provides high-level settings operations
type SettingsService struct {
	service *Service
}

// NewSettingsService creates a new settings service
func NewSettingsService(service *Service) *SettingsService {
	return &SettingsService{service: service}
}

// Get retrieves user settings
func (s *SettingsService) Get(ctx context.Context, userID string) (*Settings, error) {
	database, err := s.service.UserDB(userID)
	if err != nil {
		return nil, err
	}

	repo := NewSettingsRepository(database)
	return repo.GetOrCreate(ctx, userID)
}

// Update updates user settings
func (s *SettingsService) Update(ctx context.Context, settings *Settings) error {
	database, err := s.service.UserDB(settings.UserID)
	if err != nil {
		return err
	}

	repo := NewSettingsRepository(database)
	return repo.Update(ctx, settings)
}

// UpdateNotifications updates only notification settings
func (s *SettingsService) UpdateNotifications(ctx context.Context, userID string, notifications NotificationSettings) error {
	settings, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}

	settings.Notifications = notifications
	return s.Update(ctx, settings)
}

// UpdatePrivacy updates only privacy settings
func (s *SettingsService) UpdatePrivacy(ctx context.Context, userID string, privacy PrivacySettings) error {
	settings, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}

	settings.Privacy = privacy
	return s.Update(ctx, settings)
}

// UpdateSecurity updates only security settings
func (s *SettingsService) UpdateSecurity(ctx context.Context, userID string, security SecuritySettings) error {
	settings, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}

	// Preserve trusted devices when updating security settings
	security.TrustedDevices = settings.Security.TrustedDevices
	settings.Security = security
	return s.Update(ctx, settings)
}
