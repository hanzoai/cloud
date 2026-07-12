package organization

import (
	"context"
	"github.com/hanzoai/orm"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ryanuber/go-glob"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/mintauth"
	"github.com/hanzoai/cloud/clients/commerce/models/app"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/oauthtoken"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/analytics"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/pricing"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/cloud/clients/commerce/types/socialmedia"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/val"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var kind = "organization"

func init() { orm.Register[Organization]("organization") }

type Organization struct {
	mixin.Model[Organization]
	mixin.AccessTokens
	wallet.WalletHolder

	Name       string   `json:"name"`
	FullName   string   `json:"fullName"`
	Owners     []string `json:"owners,omitempty" datastore:",noindex"`
	Admins     []string `json:"admins,omitempty" datastore:",noindex"`
	Moderators []string `json:"moderators,omitempty" datastore:",noindex"`
	Enabled    bool     `json:"enabled"`

	BillingEmail     string                  `json:"billingEmail,omitempty"`
	Phone            string                  `json:"phone,omitempty"`
	Address          Address                 `json:"address,omitempty"`
	SocialMedia      socialmedia.SocialMedia `json:"socialMedia,omitEmpty"`
	Websites         []website.Website       `json:"websites,omitEmpty"`
	WalletPassphrase string                  `json:"-"`

	Timezone string `json:"timezone"`

	Country string `json:"country"`
	TaxId   string `json:"taxId"`

	// Used in generating email templates
	LogoUrl string `json:"logoUrl"`

	// Where is the user's dashboard?
	DashboardUrl string `json:"dashboardUrl"`

	// Fee structure for this organization
	Fees pricing.Fees `json:"fees" datastore:",noindex"`

	// Partner fees (private, should be up to partner to disclose)
	Partners []pricing.Partner `json:"-" datastore:",noindex"`

	// Email settings
	Email email.Settings `json:"email" datastore:",noindex"`

	// Default Store
	DefaultStore string `json:"defaultStore"`

	// Default App
	DefaultApp string `json:"defaultApp"`

	// Plan settings
	Plan struct {
		PlanId    string
		StartDate time.Time
	} `json:"-"`

	// Affiliate configuration
	Affiliate integration.Affiliate `json:"-" datastore:",noindex"`

	// Signup options
	SignUpOptions struct {
		// Controls the enabled status of account after creation
		AccountsEnabledByDefault bool `json:"accountsEnabledByDefault"`

		// Allow direct affiliate sign up
		AllowAffiliateSignup bool `json:"allowAffiliateSignup"`

		// Turns off required backend checks
		NoNameRequired     bool `json:"noNameRequired"`
		NoPasswordRequired bool `json:"noPasswordRequired"`

		// Requires password set on create confirmation
		TwoStageEnabled bool `json:"twoStageEnabled"`
		ImmediateLogin  bool `json:"immediateLogin"`

		UsernameRequired bool `json:"usernameRequired"`
	} `json:"signUpOptions" datastore:",noindex"`

	// Whether we use live or test tokens, mostly applicable to stripe
	Live bool `json:"live"`

	// TODO: Remain to PaymentWhitelist for clarity
	// List of comma deliminated email globs that result in charges of 50 cents
	EmailWhitelist string `json:"emailWhitelist" datastore:",noindex"`

	// integration
	Integrations  integration.Integrations `json:"-" datastore:"-"`
	Integrations_ string                   `json:"-" datastore:",noindex"`

	// integration (deprecated)

	// Analytics config
	Analytics analytics.Analytics `json:"analytics" datastore:",noindex"`

	// Bitcoi settings
	Bitcoin integration.Bitcoin `json:"-"`

	// Ethereum settings
	Ethereum integration.Ethereum `json:"-"`

	// Mailchimp settings
	Mailchimp integration.Mailchimp `json:"-"`

	// Mandrill settings
	Mandrill integration.Mandrill `json:"-"`

	// Mercury bank connection
	Mercury integration.Mercury `json:"-"`

	// Netlify settings
	Netlify integration.Netlify `json:"-"`

	// Paypal connection
	Paypal integration.Paypal `json:"-"`

	// Plaid connection
	Plaid integration.Plaid `json:"-"`

	// ReAmaze settings
	Reamaze integration.Reamaze `json:"-"`

	Recaptcha integration.Recaptcha `json:"-" datastore:",noindex"`

	// Salesforce settings
	Salesforce integration.Salesforce `json:"-"`

	// Shipwire settings
	Shipwire integration.Shipwire `json:"-"`

	// Square connection
	Square integration.Square `json:"-"`

	// Stripe connection
	Stripe integration.Stripe `json:"-"`

	// Wire transfer settings
	Wire integration.WireTransfer `json:"-"`

	// AuthorizeNet connection
	AuthorizeNet integration.AuthorizeNet `json:"-"`

	// Adyen connection
	Adyen integration.Adyen `json:"-"`

	// Braintree connection
	Braintree integration.Braintree `json:"-"`

	// Recurly connection
	Recurly integration.Recurly `json:"-"`

	// LemonSqueezy connection
	LemonSqueezy integration.LemonSqueezy `json:"-"`

	SecurityToken integration.SecurityToken `json:"-"`

	Currency currency.Type `json:"currency"`
}

func (o *Organization) Load(ps []datastore.Property) (err error) {
	// Ensure we're initialized
	o.Defaults()

	// Load supported properties
	if err = datastore.LoadStruct(o, ps); err != nil {
		return err
	}

	if len(o.Integrations_) > 0 {
		err = json.DecodeBytes([]byte(o.Integrations_), &o.Integrations)
	}

	for i, in := range o.Integrations {
		err = integration.Decode(&in, &in)
		o.Integrations[i] = in
		if err != nil {
			return err
		}
	}

	return err
}

func (o *Organization) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	o.Integrations_ = string(json.EncodeBytes(o.Integrations))

	// Save properties
	return datastore.SaveStruct(o)
}

func (o Organization) GetStripeAccessToken(userId string) (string, error) {
	if o.Stripe.Live.UserId == userId {
		return o.Stripe.Live.AccessToken, nil
	}
	if o.Stripe.Test.UserId == userId {
		return o.Stripe.Test.AccessToken, nil
	}
	return "", StripeAccessTokenNotFound{userId, o.Stripe.Live.UserId, o.Stripe.Test.UserId}
}

func (o *Organization) Validator() *val.Validator {
	return val.New().Check("FullName").Exists()
}

// Old JWT / AccessToken AUTH
func (o *Organization) AddDefaultTokens() {
	o.RemoveToken("live-secret-key")
	o.RemoveToken("live-published-key")
	o.RemoveToken("test-secret-key")
	o.RemoveToken("test-published-key")
	o.AddToken("live-secret-key", permission.Admin|permission.Live)
	o.AddToken("live-published-key", permission.Published|permission.Live|permission.ReadCoupon|permission.ReadProduct|permission.WriteReferrer)
	o.AddToken("test-secret-key", permission.Admin|permission.Test)
	o.AddToken("test-published-key", permission.Published|permission.Test|permission.ReadCoupon|permission.ReadProduct|permission.WriteReferrer)
}

// New JWT / OAUTH
func (o *Organization) ResetReferenceToken(usr *user.User, claims oauthtoken.Claims) (*oauthtoken.Token, error) {
	if usr.Key().Namespace() != "" {
		return nil, UserNotTopLevel
	}

	if _, _, err := o.RevokeReferenceToken(usr); err != nil {
		return nil, err
	}

	tok := oauthtoken.New(o.Datastore())

	log.Info("Creating New Reference Token for user '%s' from org '%s'.", usr.Id(), o.Name, o.Context())

	claims.OrganizationName = o.Name
	claims.UserId = usr.Id()
	claims.Type = oauthtoken.Reference
	claims.JTI = tok.Id()
	claims.IssuedAt = time.Now().Unix()

	tok.Claims = claims
	tok.AccessPeriod = 24 * 30

	if _, err := tok.Encode(o.SecretKey); err != nil {
		log.Info("Failed to create New Reference Token for user '%s' from org '%s': %s", usr.Id(), o.Name, err, o.Context())
		return nil, err
	}

	tok.MustCreate()

	return tok, nil
}

func (o *Organization) GetReferenceToken(usr *user.User) (*oauthtoken.Token, bool, error) {
	if usr.Key().Namespace() != "" {
		return nil, false, UserNotTopLevel
	}

	log.Info("Getting Reference Token for user '%s' from org '%s'.", usr.Id(), o.Name, o.Context())

	tok := oauthtoken.New(o.Datastore())

	if ok, err := tok.Query().Filter("Claims.OrganizationName=", o.Name).Filter("Claims.Type=", oauthtoken.Reference).Filter("Revoked=", false).Filter("Claims.UserId=", usr.Id()).Get(); !ok {
		log.Info("Failed to get Reference Token for user '%s' from org '%s': %s", usr.Id(), o.Name, err, o.Context())
		return nil, false, err
	}

	return tok, true, nil
}

func (o *Organization) RevokeReferenceToken(usr *user.User) (*oauthtoken.Token, bool, error) {
	if usr.Key().Namespace() != "" {
		return nil, false, UserNotTopLevel
	}

	log.Info("Revoking Reference Token for user '%s' from org '%s'.", usr.Id(), o.Name, o.Context())

	if tok, ok, err := o.GetReferenceToken(usr); !ok {
		log.Info("Failed to revoke Reference Token for user '%s' from org '%s': %s", usr.Id(), o.Name, err, o.Context())
		return nil, false, err
	} else {
		tok.Revoke()
		return tok, true, nil
	}
}

func userId(userOrId interface{}) string {
	userid := ""
	switch v := userOrId.(type) {
	case *user.User:
		userid = v.Id()
	case string:
		userid = v
	}
	return userid
}

func (o Organization) IsAdmin(userOrId interface{}) bool {
	userid := userId(userOrId)

	for i := range o.Admins {
		if o.Admins[i] == userid {
			return true
		}
	}
	return false
}

func (o Organization) IsOwner(userOrId interface{}) bool {
	userid := userId(userOrId)

	for i := range o.Owners {
		if o.Owners[i] == userid {
			return true
		}
	}
	return false
}

// Add admin to organization
func (o *Organization) AddAdmin(userOrId string) {
	userid := userId(userOrId)

	if !o.IsAdmin(userid) {
		o.Admins = append(o.Admins, userid)
	}
}

// Add admin to organization
func (o *Organization) AddOwner(userOrId string) {
	userid := userId(userOrId)

	if !o.IsOwner(userid) {
		o.Owners = append(o.Owners, userid)
	}
}

// Namespace returns the datastore namespace for this organization.
//
// SECURITY: every org — without exception — is strictly scoped to its own
// name. There is NO org-name escape hatch. The legacy
// `Name=="platform" -> "" (global/cross-org namespace)` bypass was REMOVED:
// it keyed cross-org datastore access on an org-NAME string, fully decoupled
// from real platform-admin identity, so anyone who could land in (or forge)
// an org named "platform" read/wrote the global namespace where every org's
// records live (Red — privilege escalation via the empty namespace).
// Cross-org / superadmin datastore access is now gated EXCLUSIVELY on
// auth.IAMClaims.SuperAdmin() (owner=="admin" or the isSuperAdmin claim)
// at the handler/middleware layer — never inferred from the org name here.
func (o Organization) Namespace() string {
	return o.Name
}

// Namespaced returns a context scoped to this organization's namespace.
// When called with a gin.Context, we always detach from the HTTP request
// lifecycle and use context.Background() for the database context. This
// prevents "context canceled" errors when the browser disconnects or the
// upstream proxy timeout fires before the ClickHouse query completes.
func (o Organization) Namespaced(ctx context.Context) context.Context {
	if c, ok := ctx.(*gin.Context); ok {
		// HTTP boundary: mark every inbound-request datastore context mint-gated
		// (mintauth.WithGate) so the ledger sink refuses an unauthorized spendable
		// mint by ANY inbound principal — the single structural gate point. A
		// proven mint authority (PlatformOnly / settled payment / server-fixed
		// grant) satisfies it by adding mintauth.WithAuthorized, which rides in
		// either the stored "context" (route middleware) or the write's own ctx.
		base := context.Background() // DB ops must survive HTTP disconnect
		if reqCtx := c.Value("context"); reqCtx != nil {
			if ctxVal, ok := reqCtx.(context.Context); ok {
				base = ctxVal
			}
		}
		return nscontext.WithNamespace(mintauth.WithGate(base), o.Namespace())
	}

	// Non-*gin.Context callers are internal (crons, migrations, tests): NOT gated,
	// so internal ledger writes are unaffected by the mint invariant.
	return nscontext.WithNamespace(ctx, o.Namespace())
}

func (o Organization) SquareConfig(sandbox bool) integration.SquareConnection {
	if sandbox {
		return o.Square.Sandbox
	}
	return o.Square.Production
}

func (o Organization) StripeToken() string {
	if o.Live {
		return o.Stripe.Live.AccessToken
	}

	return o.Stripe.Test.AccessToken
}

func (o Organization) AuthorizeNetToken(sandbox bool) integration.AuthorizeNetConnection {
	if sandbox {
		return o.AuthorizeNet.Sandbox
	}

	return o.AuthorizeNet.Live
}

func (o Organization) IsTestEmail(email string) bool {
	if email == "" || o.EmailWhitelist == "" {
		return false
	}

	globs := strings.Split(strings.Replace(o.EmailWhitelist, " ", "", -1), ",")

	for _, g := range globs {
		if glob.Glob(g, email) {
			return true
		}
	}

	return false
}

func (o Organization) Pricing() (*pricing.Fees, []pricing.Partner) {
	// Ensure our id is set on fees used
	fees := o.Fees
	fees.Id = o.Id()
	return &fees, o.Partners
}

// Return DefaultStore
func (o Organization) GetDefaultStore() (*store.Store, error) {
	db := datastore.New(o.Namespaced(o.Context()))
	s := store.New(db)
	if err := s.GetById(o.DefaultStore); err != nil {
		return nil, err
	}

	return s, nil
}

// Return DefaultApp
func (o Organization) GetDefaultApp() (*app.App, error) {
	db := datastore.New(o.Namespaced(o.Context()))
	a := app.New(db)
	if err := a.GetById(o.DefaultApp); err != nil {
		return nil, err
	}

	return a, nil
}

func (o *Organization) Defaults() {
	o.Admins = make([]string, 0)
	o.Moderators = make([]string, 0)

	o.Enabled = true

	o.Fees.Card.Flat = 50
	o.Fees.Card.Percent = 0.05
	o.Fees.Bitcoin.Flat = 0
	o.Fees.Bitcoin.Percent = 0.05
	o.Fees.Ethereum.Flat = 0
	o.Fees.Ethereum.Percent = 0.05
	o.Fees.Affiliate.Flat = 30
	o.Fees.Affiliate.Percent = 0.30

	o.Partners = make([]pricing.Partner, 0)
}

func (o *Organization) Init(db *datastore.Datastore) {
	o.Model.Init(db)
	o.AccessTokens.Init(o)
}

func New(db *datastore.Datastore) *Organization {
	o := new(Organization)
	o.Init(db)
	o.Defaults()
	return o
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
