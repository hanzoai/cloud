package user

import (
	"strings"

	"time"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/demo/tokentransaction"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/affiliate"
	"github.com/hanzoai/cloud/clients/commerce/models/fee"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/commission"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[User]("user") }

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

type KYCData struct {
	Flagged      bool      `json:"flagged,omitempty"`
	Frozen       bool      `json:"frozen,omitempty"`
	DateApproved time.Time `json:"dateApproved,omitempty"`

	WalletAddresses []string `json:"walletAddresses,omitempty"`
	Address         Address  `json:"address,omitempty"`
	Documents       []string `json:"documents,omitempty" datastore:"-"`
	Documents_      []byte   `json:"-" datastore:",noindex"`

	TaxId     string    `json:"taxId,omitempty"`
	Phone     string    `json:"phone,omitempty"`
	Birthdate time.Time `json:"birthdate,omitempty"`
	Gender    string    `json:"gender,omitempty"`

	EthereumAddress string `json:"ethereumAddress,omitempty"`
	EOSPublicKey    string `json:"eosPublicKey,omitempty"`
}

type User struct {
	mixin.Model[User]
	mixin.Salesforce
	wallet.WalletHolder

	// Crowdstart Id, found in default namespace
	Cid string `json:"-"`

	Username         string   `json:"username,omitempty"`
	FirstName        string   `json:"firstName"`
	LastName         string   `json:"lastName"`
	Company          string   `json:"company,omitempty"`
	Phone            string   `json:"phone,omitempty"`
	BillingAddress   Address  `json:"billingAddress,omitempty"`
	ShippingAddress  Address  `json:"shippingAddress,omitempty"`
	Email            string   `json:"email"`
	PaypalEmail      string   `json:"paypalEmail,omitempty"`
	PasswordHash     []byte   `schema:"-" datastore:",noindex" json:"-"`
	Organizations    []string `json:"-" datastore:",noindex"`
	StoreId          string   `json:"storeId,omitempty"`
	WalletPassphrase string   `json:"-"`

	Facebook struct {
		AccessToken string `facebook:"-"`
		UserId      string `facebook:"id"`
		FirstName   string `facebook:"first_name"`
		LastName    string `facebook:"last_name"`
		MiddleName  string `facebook:"middle_name"`
		Name        string `facebook:"name" datastore:"-"`
		NameFormat  string `facebook:"name_format"` // For Chinese, Japanese, and Korean names. Possibly used in the future.
		Email       string `facebook:"email" datastore:"-"`
		Verified    bool   `facebook:"verified" datastore:"-"`
	} `json:"-"`

	KYC struct {
		KYCData

		Status KYCStatus `json:"status,omitempty"`
		Hash   string    `json:"hash"`
	} `json:"kyc,omitempty"`

	// Account to use for new orders when customer creates new orders
	Accounts accounts.Account `json:"-" datastore:",noindex"`

	Enabled     bool `json:"enabled"` //whether or not the user can login yet
	PreApproved bool `json:"preApproved"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`

	Referrals      []referral.Referral           `json:"referrals,omitempty" datastore:"-"`
	Referrers      []referrer.Referrer           `json:"referrers,omitempty" datastore:"-"`
	Orders         []order.Order                 `json:"orders,omitempty" datastore:"-"`
	PendingFees    []fee.Fee                     `json:"pendingFees,omitempty" datastore:"-"`
	PaymentMethods []paymentmethod.PaymentMethod `json:"paymentMethods,omitempty" datastore:"-"`
	Affiliate      affiliate.Affiliate           `json:"affiliate,omitempty" datastore:"-"`

	Transactions map[currency.Type]*util.TransactionData `json:"transactions" datastore:"-"`

	TokenTransactions []*tokentransaction.Transaction `json:"tokenTransactions" datastore:"-"`

	ReferrerId string `json:"referrerId,omitempty"`

	// Series of events that have occured relevant to this order
	History []Event `json:"-,omitempty" datastore:",noindex"`

	IsOwner bool `json:"owner,omitempty" datastore:"-"`

	IsAffiliate bool `json:"isAffiliate,omitempty"`

	AffiliateId string `json:"affiliateId,omitempty"`

	FormId string `json:"formId,omitempty"`

	// For Halcyon
	Commission commission.Commission `json:"commission"`

	Test bool `json:"test"`
}

// Total implements referrer.Referrent. Users have no monetary total by themselves.
func (u *User) Total() currency.Cents {
	return currency.Cents(0)
}

func (u *User) Load(ps []datastore.Property) (err error) {
	// Load supported properties
	if err = datastore.LoadStruct(u, ps); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(u.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(u.Metadata_), &u.Metadata)
	}

	if len(u.KYC.Documents_) > 0 {
		err = json.DecodeBytes([]byte(u.KYC.Documents_), &u.KYC.Documents)
	}

	return
}

func (u *User) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	u.Metadata_ = string(json.EncodeBytes(&u.Metadata))

	u.KYC.Documents_ = json.EncodeBytes(&u.KYC.Documents)

	// sanitize email
	u.Email = strings.ToLower(strings.TrimSpace(u.Email))

	// Save properties
	return datastore.SaveStruct(u)
}

func (u User) Name() string {
	return u.FirstName + " " + u.LastName
}

func (u User) HasPassword() bool {
	return len(u.PasswordHash) != 0
}

func (u User) ComparePassword(pass string) bool {
	return password.HashAndCompare(u.PasswordHash, pass)
}

func (u User) Buyer() Buyer {
	return Buyer{
		Email:     u.Email,
		UserId:    u.Id(),
		FirstName: u.FirstName,
		LastName:  u.LastName,
		Company:   u.Company,
		Phone:     u.Phone,

		ShippingAddress: u.ShippingAddress,
		BillingAddress:  u.BillingAddress,
	}
}

func (u *User) Validator() *val.Validator {
	return val.New().Check("FirstName").Exists().
		Check("LastName").Exists().
		Check("Email").Exists()
}

// Populates current entity from datastore by Email.
func (u *User) GetByEmail(email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	log.Debug("Searching for user '%v'", email)

	ok, err := u.Query().Filter("Email=", email).Get()

	if err != nil {
		log.Warn("Unable to find user by email: '%v'", err)
		return err
	}

	// Return error if no user found.
	if !ok {
		return UserNotFound
	}

	return nil
}

// Populates current entity from datastore by Email.
func (u *User) GetByUsername(un string) error {
	un = strings.ToLower(strings.TrimSpace(un))
	log.Debug("Searching for user '%v'", un)

	ok, err := u.Query().Filter("Username=", un).Get()

	if err != nil {
		log.Warn("Unable to find user by username: '%v'", err)
		return err
	}

	// Return error if no user found.
	if !ok {
		return UserNotFound
	}

	return nil
}

func (u *User) LoadReferrals() error {
	db := u.Datastore()
	u.Referrers = make([]referrer.Referrer, 0)
	if _, err := referrer.Query(db).Filter("UserId=", u.Id()).GetAll(&u.Referrers); err != nil {
		return err
	}

	u.Referrals = make([]referral.Referral, 0)
	if _, err := referral.Query(db).Filter("Referrer.UserId=", u.Id()).GetAll(&u.Referrals); err != nil {
		return err
	}

	log.Warn("Referrals %v", u.Referrals)

	return nil
}

func (u *User) LoadPaymentMethods() error {
	db := u.Datastore()
	u.PaymentMethods = make([]paymentmethod.PaymentMethod, 0)
	if _, err := paymentmethod.Query(db).Filter("UserId=", u.Id()).GetAll(&u.PaymentMethods); err != nil {
		return err
	}

	log.Warn("PaymentMethods %v", u.PaymentMethods)

	return nil
}

func (u *User) LoadOrders() error {
	db := u.Datastore()
	u.Orders = make([]order.Order, 0)
	if _, err := order.Query(db).Filter("UserId=", u.Id()).GetAll(&u.Orders); err != nil {
		return err
	}

	for i, o := range u.Orders {
		if err := o.LoadWallet(db); err != nil {
			return err
		}

		u.Orders[i].Wallet = o.Wallet
	}

	return nil
}

func (u *User) LoadAffiliateAndPendingFees() error {
	if u.AffiliateId == "" {
		return nil
	}

	db := u.Datastore()
	aff := affiliate.New(db)

	if err := aff.GetById(u.AffiliateId); err != nil {
		return err
	}

	u.Affiliate = *aff

	u.PendingFees = make([]fee.Fee, 0)
	if _, err := fee.Query(db).Filter("AffiliateId=", u.AffiliateId).Filter("Status=", fee.Payable).GetAll(&u.PendingFees); err != nil {
		return err
	}

	return nil
}

func (u *User) LoadTokenTransactions() error {
	db := u.Datastore()
	u.TokenTransactions = make([]*tokentransaction.Transaction, 0)
	if _, err := tokentransaction.Query(db).Filter("SendingUserId=", u.Id()).GetAll(&u.TokenTransactions); err != nil {
		return err
	}

	tt := make([]*tokentransaction.Transaction, 0)
	if _, err := tokentransaction.Query(db).Filter("ReceivingUserId=", u.Id()).GetAll(&tt); err != nil {
		return err
	}

	u.TokenTransactions = append(u.TokenTransactions, tt...)

	return nil
}

func (u *User) CalculateBalances(test bool) error {
	res, err := util.GetTransactions(u.Context(), u.Id(), "user", test)

	u.Transactions = res.Data

	return err
}

func (u *User) SetPassword(newPassword string) error {
	hash, err := password.Hash(newPassword)
	if err != nil {
		return err
	}

	u.PasswordHash = hash
	return nil
}

// Check if user is part of an organization
func (u *User) InOrganization(orgId string) bool {
	for i := range u.Organizations {
		if u.Organizations[i] == orgId {
			return true
		}
	}
	return false
}

// Save organization to organization slice.
func (u *User) AddOrganization(orgId string) {
	if !u.InOrganization(orgId) {
		u.Organizations = append(u.Organizations, orgId)
	}
}

func New(db *datastore.Datastore) *User {
	u := new(User)
	u.Init(db)
	u.Metadata = make(map[string]interface{})
	u.History = make([]Event, 0)
	u.Organizations = make([]string, 0)
	u.KYC.Documents = make([]string, 0)
	u.KYC.Status = KYCStatusInitiated
	return u
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("user")
}
