package integration

import (
	"encoding/json"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/analytics"
)

// StripeConnectToken holds legacy Stripe Connect OAuth credentials.
// Retained for backward compatibility with stored data.
type StripeConnectToken struct {
	AccessToken    string `json:"accessToken,omitempty"`
	PublishableKey string `json:"publishableKey,omitempty"`
	RefreshToken   string `json:"refreshToken,omitempty"`
	UserId         string `json:"userId,omitempty"`
	Livemode       bool   `json:"livemode,omitempty"`
	Scope          string `json:"scope,omitempty"`
	TokenType      string `json:"tokenType,omitempty"`
}

type Type string

const (
	// Analytics
	AnalyticsCustomType              Type = "analytics-custom"
	AnalyticsFacebookPixelType       Type = "analytics-facebook-pixel"
	AnalyticsFacebookConversionsType Type = "analytics-facebook-conversions"
	AnalyticsGoogleAdwordsType       Type = "analytics-google-adwords"
	AnalyticsGoogleAnalyticsType     Type = "analytics-google-analytics"
	AnalyticsHeapType                Type = "analytics-heap"
	AnalyticsSentryType              Type = "analytics-sentry"

	// Others
	AuthorizeNetType  Type = "authorizeNet"
	BitcoinType       Type = "bitcoin"
	EthereumType      Type = "ethereum"
	MailchimpType     Type = "mailchimp"
	MandrillType      Type = "mandrill"
	MercuryType       Type = "mercury"
	NetlifyType       Type = "netlify"
	PaypalType        Type = "paypal"
	PlaidType         Type = "plaid"
	ReamazeType       Type = "reamaze"
	RecaptchaType     Type = "recaptcha"
	SalesforceType    Type = "salesforce"
	SecurityTokenType Type = "securityToken"
	WireTransferType  Type = "wireTransfer"
	SendGridType      Type = "sendgrid"
	ShipwireType      Type = "shipwire"
	SMTPRelayType     Type = "smtprelay"
	StripeType        Type = "stripe"
	WoopraType        Type = "woopra"
)

// Analytics

// Generic fields
type AnalyticsIntegration struct {
	// Common to all integrations
	Event string `json:"event,omitempty"`
	Id    string `json:"id,omitempty"`

	// Sampling percentage
	Sampling float64 `json:"sampling,omitempty"`
}

// Integration specific properties

// Override value for a given event
type Value analytics.Value

type Values analytics.Values

type AnalyticsCustom struct {
	AnalyticsIntegration

	Code string `json:"code,omitempty"`
}

type AnalyticsFacebookPixel struct {
	AnalyticsIntegration

	Values Values `json:"values,omitempty"`
}

type AnalyticsFacebookConversions struct {
	AnalyticsIntegration

	Value    string `json:"value,omitempty"`
	Currency string `json:"currency,omitempty"`
}

type AnalyticsGoogleAdwords struct {
	AnalyticsIntegration
}

type AnalyticsGoogleAnalytics struct {
	AnalyticsIntegration
}

type AnalyticsHeap struct {
	AnalyticsIntegration
}

type AnalyticsSentry struct {
	AnalyticsIntegration
}

// Others

// Mailchimp settings
type Mailchimp struct {
	ListId      string `json:"listId,omitempty"`
	APIKey      string `json:"apiKey,omitempty"`
	CheckoutUrl string `json:"checkoutUrl,omitempty"`
}

// Mandrill settings
type Mandrill struct {
	APIKey string `json:"apiKey,omitempty"`
}

// Mercury bank connection
type Mercury struct {
	APIToken      string `json:"apiToken,omitempty"`
	WebhookSecret string `json:"webhookSecret,omitempty"`
	AccountID     string `json:"accountId,omitempty"`
}

// SendGrid settings
type SendGrid struct {
	APIKey string `json:"apiKey,omitempty"`
}

// Netlify settings
type Netlify struct {
	AccessToken string    `json:"accessToken,omitempty"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
	Email       string    `json:"email,omitempty"`
	Id          string    `json:"id,omitempty"`
	Uid         string    `json:"uId,omitempty"`
}

// Paypal connection
type Paypal struct {
	Live struct {
		Email             string `json:"email,omitempty"`
		SecurityUserId    string `json:"securityUserId,omitempty"`
		SecurityPassword  string `json:"securityPassword,omitempty" datastore:",noindex"`
		SecuritySignature string `json:"SecuritySignature,omitempty" datastore:",noindex"`
		ApplicationId     string `json:"applicationId,omitempty"`
	} `json:"live,omitempty"`
	Test struct {
		Email             string `json:"email,omitempty"`
		SecurityUserId    string `json:"securityUserId,omitempty"`
		SecurityPassword  string `json:"securityPassword,omitempty" datastore:",noindex"`
		SecuritySignature string `json:"SecuritySignature,omitempty" datastore:",noindex"`
		ApplicationId     string `json:"applicationId,omitempty"`
	} `json:"test,omitempty"`

	ConfirmUrl string `json:"confirmUrl,omitempty" datastore:",noindex"`
	CancelUrl  string `json:"cancelUrl,omitempty" datastore:",noindex"`
}

// Plaid keys
type Plaid struct {
	ClientId  string `json:"clientId,omitempty"`
	Secret    string `json:"secret,omitempty" datastore:",noindex"`
	PublicKey string `json:"pubKey,omitempty"`
}

// Affiliate configuration
type Affiliate struct {
	SuccessUrl string `json:"successUrl,omitempty"`
	ErrorUrl   string `json:"errorUrl,omitempty"`
}

type Reamaze struct {
	Secret string `json:"secret,omitempty"`
}

type Recaptcha struct {
	Enabled   bool   `json:"enabled,omitempty"`
	SecretKey string `json:"secretKey,omitempty"`
}

// Salesforce settings
type Salesforce struct {
	AccessToken        string `json:"accessToken,omitempty"`
	DefaultPriceBookId string `json:"defaultPriceBookId,omitempty"`
	// personalized login url
	Id           string `json:"id,omitempty"`
	InstanceUrl  string `json:"instanceUrl,omitempty"`
	IssuedAt     string `json:"issuedAt,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
	Signature    string `json:"signature,omitempty" datastore:",noindex"`
}

type Shipwire struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// SMTP settings
type SMTPRelay struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	Host     string   `json:"host"`
	Port     string   `json:"port"`
	MailFrom string   `json:"mailFrom"`
	MailTo   []string `json:"mailTo"`
	Msg      string   `json:"msg"`
}

// Stripe holds legacy Stripe integration credentials.
// Retained for backward compatibility with stored organization data.
type Stripe struct {
	// For convenience duplicated
	AccessToken    string `json:"accessToken,omitempty"`
	PublishableKey string `json:"publishableKey,omitempty"`
	RefreshToken   string `json:"refreshToken,omitempty"`
	UserId         string `json:"userId,omitempty"`

	// Save entire live and test tokens
	Live StripeConnectToken `json:"live,omitempty" datastore:",noindex"`
	Test StripeConnectToken `json:"test,omitempty" datastore:",noindex"`
}

// Square connection
type SquareConnection struct {
	ApplicationId string `json:"applicationId,omitempty"`
	AccessToken   string `json:"accessToken,omitempty"`
	LocationId    string `json:"locationId,omitempty"`
}

type Square struct {
	WebhookSignatureKey string `json:"webhookSignatureKey,omitempty"`
	// WebhookURL is the notification URL registered for this org's Square
	// webhook subscription. Square signs deliveries over (WebhookURL +
	// rawBody); when empty the deployment falls back to SQUARE_WEBHOOK_URL
	// and then the canonical commerce ingress URL.
	WebhookURL string           `json:"webhookUrl,omitempty"`
	Sandbox    SquareConnection `json:"sandbox"`
	Production SquareConnection `json:"production"`
}

// WireTransfer holds bank wire transfer details for an organization
type WireTransfer struct {
	BankName      string `json:"bankName,omitempty"`
	AccountHolder string `json:"accountHolder,omitempty"`
	RoutingNumber string `json:"routingNumber,omitempty"`
	AccountNumber string `json:"accountNumber,omitempty"`
	SWIFT         string `json:"swift,omitempty"`
	IBAN          string `json:"iban,omitempty"`
	BankAddress   string `json:"bankAddress,omitempty"`
	Reference     string `json:"reference,omitempty"`
	Instructions  string `json:"instructions,omitempty"`
}

// Adyen connection
type Adyen struct {
	APIKey          string `json:"apiKey,omitempty" datastore:",noindex"`
	MerchantAccount string `json:"merchantAccount,omitempty"`
	HMACKey         string `json:"hmacKey,omitempty" datastore:",noindex"`
	Environment     string `json:"environment,omitempty"` // "test" or "live"
	LiveURLPrefix   string `json:"liveUrlPrefix,omitempty"`
}

// Braintree connection
type Braintree struct {
	MerchantID  string `json:"merchantId,omitempty"`
	PublicKey   string `json:"publicKey,omitempty"`
	PrivateKey  string `json:"privateKey,omitempty" datastore:",noindex"`
	Environment string `json:"environment,omitempty"` // "sandbox" or "production"
}

// Recurly connection
type Recurly struct {
	APIKey    string `json:"apiKey,omitempty" datastore:",noindex"`
	Subdomain string `json:"subdomain,omitempty"`
}

// LemonSqueezy connection
type LemonSqueezy struct {
	APIKey           string `json:"apiKey,omitempty" datastore:",noindex"`
	StoreID          string `json:"storeId,omitempty"`
	WebhookSecret    string `json:"webhookSecret,omitempty" datastore:",noindex"`
	DefaultVariantID string `json:"defaultVariantId,omitempty"`
}

// Authorize.net connection
type AuthorizeNetConnection struct {
	LoginId        string `json:"loginId,omitempty"`
	TransactionKey string `json:"transactionKey,omitempty"`
	Key            string `json:"key,omitempty"`
}

type AuthorizeNet struct {
	// For convenience duplicated
	Sandbox AuthorizeNetConnection `json:"sandbox"`
	Live    AuthorizeNetConnection `json:"live"`
}

// Bitcoin
type Bitcoin struct {
	Address     string `json:"address,omitempty"`
	TestAddress string `json:"testAddress,omitempty"`
}

// Ethereum
type Ethereum struct {
	Address     string `json:"address,omitempty"`
	TestAddress string `json:"testAddress,omitempty"`
}

// Security Tokens
type EthereumSecurityToken struct {
	TokenAddress    string `json:"tokenAddress,omitempty"`
	RegistryAddress string `json:"registryAddress,omitempty"`
	PrivateKey      string `json:"-"`
}

type EOSSecurityToken struct {
	TokenAccount    string `json:"tokenAccount,omitempty"`
	RegistryAccount string `json:"registryAccount,omitempty"`
	PrivateKey      string `json:"-"`
}

type SecurityToken struct {
	Ethereum EthereumSecurityToken `json:"ethereum,omitempty"`
	EOS      EOSSecurityToken      `json:"eos,omitempty"`
}

type Woopra struct {
	Domain string `json:"domain,omitempty"`
}

type Integration struct {
	Type      Type            `json:"type,omitempty"`
	Enabled   bool            `json:"enabled,omitempty"`
	Show      bool            `json:"show,omitempty"`
	Id        string          `json:"id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	CreatedAt time.Time       `json:"createdAt,omitempty"`
	UpdatedAt time.Time       `json:"updatedAt,omitempty"`

	// Analytics
	AnalyticsCustom              AnalyticsCustom              `json:"-"`
	AnalyticsFacebookPixel       AnalyticsFacebookPixel       `json:"-"`
	AnalyticsFacebookConversions AnalyticsFacebookConversions `json:"-"`
	AnalyticsGoogleAdwords       AnalyticsGoogleAdwords       `json:"-"`
	AnalyticsGoogleAnalytics     AnalyticsGoogleAnalytics     `json:"-"`
	AnalyticsHeap                AnalyticsHeap                `json:"-"`
	AnalyticsSentry              AnalyticsSentry              `json:"-"`

	// Others
	AuthorizeNet  AuthorizeNet  `json:"-"`
	Bitcoin       Bitcoin       `json:"-"`
	Ethereum      Ethereum      `json:"-"`
	Mailchimp     Mailchimp     `json:"-"`
	Mandrill      Mandrill      `json:"-"`
	Netlify       Netlify       `json:"-"`
	Paypal        Paypal        `json:"-"`
	Plaid         Plaid         `json:"-"`
	Reamaze       Reamaze       `json:"-"`
	Recaptcha     Recaptcha     `json:"-"`
	Salesforce    Salesforce    `json:"-"`
	Shipwire      Shipwire      `json:"-"`
	SendGrid      SendGrid      `json:"-"`
	SMTPRelay     SMTPRelay     `json:"-"`
	Stripe        Stripe        `json:"-"`
	SecurityToken SecurityToken `json:"-"`
	Woopra        Woopra        `json:"-"`
}
