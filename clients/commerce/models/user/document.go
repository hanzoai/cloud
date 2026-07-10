package user

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/country"
	"github.com/hanzoai/cloud/clients/commerce/util/searchpartial"
)

type Document struct {
	mixin.DocumentSaveLoad `datastore:"-" json:"-"`

	// Special Kind Facet
	Kind string `search:",facet"`

	Id_               string
	Email             string
	EmailPartials     string
	Username          string
	UsernamePartials  string
	FirstName         string
	FirstNamePartials string
	LastName          string
	LastNamePartials  string
	Phone             string

	BillingAddressName        string
	BillingAddressLine1       string
	BillingAddressLine2       string
	BillingAddressCity        string
	BillingAddressStateCode   string
	BillingAddressState       string
	BillingAddressCountryCode string
	BillingAddressCountry     string
	BillingAddressPostalCode  string

	ShippingAddressName        string
	ShippingAddressLine1       string
	ShippingAddressLine2       string
	ShippingAddressCity        string
	ShippingAddressStateCode   string
	ShippingAddressState       string
	ShippingAddressCountryCode string
	ShippingAddressCountry     string
	ShippingAddressPostalCode  string

	CreatedAt float64
	UpdatedAt float64

	StripeBalanceTransactionId string
	StripeCardId               string
	StripeChargeId             string
	StripeCustomerId           string
	StripeLastFour             string

	KYCStatus  string
	KYCFlagged string
	KYCFrozen  string

	KYCWalletAddresses string

	KYCAddressLine1       string
	KYCAddressLine2       string
	KYCAddressCity        string
	KYCAddressStateCode   string
	KYCAddressState       string
	KYCAddressCountryCode string
	KYCAddressCountry     string
	KYCAddressPostalCode  string

	KYCTaxId     string
	KYCPhone     string
	KYCGender    string
	KYCBirthdate string

	Test string
}

func (d Document) Id() string {
	return string(d.Id_)
}

func (d *Document) Init() {
	d.SetDocument(d)
}

func (u User) Document() mixin.Document {
	emailUser := strings.Split(u.Email, "@")[0]

	doc := &Document{}
	doc.Init()
	doc.Kind = "user"
	doc.Id_ = u.Id()
	doc.Email = u.Email
	doc.EmailPartials = searchpartial.Partials(emailUser) + " " + emailUser
	doc.Username = u.Username
	doc.UsernamePartials = searchpartial.Partials(u.Username)
	doc.FirstName = u.FirstName
	doc.FirstNamePartials = searchpartial.Partials(u.FirstName)
	doc.LastName = u.LastName
	doc.LastNamePartials = searchpartial.Partials(u.LastName)
	doc.Phone = u.Phone

	doc.BillingAddressName = u.BillingAddress.Name
	doc.BillingAddressLine1 = u.BillingAddress.Line1
	doc.BillingAddressLine2 = u.BillingAddress.Line2
	doc.BillingAddressCity = u.BillingAddress.City
	doc.BillingAddressStateCode = u.BillingAddress.State
	doc.BillingAddressCountryCode = u.BillingAddress.Country
	if u.BillingAddress.Country != "" {
		if c, err := country.FindByISO3166_2(u.BillingAddress.Country); err == nil {
			doc.BillingAddressCountry = c.Name.Common

			if u.BillingAddress.State != "" {
				if sd, err := c.FindSubDivision(u.BillingAddress.State); err == nil {
					doc.BillingAddressState = sd.Name
				} else {
					log.Error("BillingAddress State Code '%s' caused an error: %s ", u.BillingAddress.State, err, u.Context())
				}
			}
		} else {
			log.Error("BillingAddress Country Code '%s' caused an error: %s", u.BillingAddress.Country, err, u.Context())
		}
	}
	doc.BillingAddressPostalCode = u.BillingAddress.PostalCode

	doc.ShippingAddressName = u.ShippingAddress.Name
	doc.ShippingAddressLine1 = u.ShippingAddress.Line1
	doc.ShippingAddressLine2 = u.ShippingAddress.Line2
	doc.ShippingAddressCity = u.ShippingAddress.City
	doc.ShippingAddressStateCode = u.ShippingAddress.State
	doc.ShippingAddressCountryCode = u.ShippingAddress.Country
	if u.ShippingAddress.Country != "" {
		if c, err := country.FindByISO3166_2(u.ShippingAddress.Country); err == nil {
			doc.ShippingAddressCountry = c.Name.Common

			if u.ShippingAddress.State != "" {
				if sd, err := c.FindSubDivision(u.ShippingAddress.State); err == nil {
					doc.ShippingAddressState = sd.Name
				} else {
					log.Error("ShippingAddress State Code '%s' caused an error: %s ", u.ShippingAddress.State, err, u.Context())
				}
			}
		} else {
			log.Error("ShippingAddress Country Code '%s' caused an error: %s", u.ShippingAddress.Country, err, u.Context())
		}
	}
	doc.ShippingAddressPostalCode = u.ShippingAddress.PostalCode

	doc.CreatedAt = float64(u.CreatedAt.Unix())
	doc.UpdatedAt = float64(u.UpdatedAt.Unix())

	doc.StripeBalanceTransactionId = u.Accounts.Stripe.BalanceTransactionId
	doc.StripeCardId = u.Accounts.Stripe.CardId
	doc.StripeChargeId = u.Accounts.Stripe.ChargeId
	doc.StripeCustomerId = u.Accounts.Stripe.CustomerId
	doc.StripeLastFour = u.Accounts.Stripe.LastFour

	doc.KYCStatus = string(u.KYC.Status)

	if u.KYC.Flagged {
		doc.KYCFlagged = "true"
	} else {
		doc.KYCFlagged = "false"
	}

	if u.KYC.Frozen {
		doc.KYCFrozen = "true"
	} else {
		doc.KYCFrozen = "false"
	}

	doc.KYCWalletAddresses = strings.Join(u.KYC.WalletAddresses, " ")

	doc.KYCAddressLine1 = u.KYC.Address.Line1
	doc.KYCAddressLine2 = u.KYC.Address.Line2
	doc.KYCAddressCity = u.KYC.Address.City
	doc.KYCAddressStateCode = u.KYC.Address.State
	doc.KYCAddressCountryCode = u.KYC.Address.Country
	if u.KYC.Address.Country != "" {
		if c, err := country.FindByISO3166_2(u.KYC.Address.Country); err == nil {
			doc.KYCAddressCountry = c.Name.Common

			if u.KYC.Address.State != "" {
				if sd, err := c.FindSubDivision(u.KYC.Address.State); err == nil {
					doc.KYCAddressState = sd.Name
				} else {
					log.Error("KYCAddress State Code '%s' caused an error: %s ", u.KYC.Address.State, err, u.Context())
				}
			}
		} else {
			log.Error("KYCAddress Country Code '%s' caused an error: %s", u.KYC.Address.Country, err, u.Context())
		}
	}
	doc.KYCAddressPostalCode = u.KYC.Address.PostalCode

	doc.KYCTaxId = u.KYC.TaxId
	doc.KYCPhone = u.KYC.Phone
	doc.KYCGender = u.KYC.Gender
	doc.KYCBirthdate = u.KYC.Birthdate.Format("2006-01-02")

	if u.Test {
		doc.Test = "true"
	} else {
		doc.Test = "false"
	}

	return doc
}
