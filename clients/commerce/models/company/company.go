// Package company is the B2B company domain: a customer organization that
// buys through the store. Employees (models/employee) belong to a company and
// buy on its behalf, gated by per-employee spending limits and (optionally)
// spend-approval workflows (models/approval).
package company

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Company]("company") }

// Company is a B2B customer organization scoped to the issuing store's
// namespace. Its CurrencyCode governs the currency employee spending limits and
// quote totals are denominated in.
type Company struct {
	mixin.Model[Company]

	Name  string `json:"name"`
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`

	AddressLine1 string `json:"addressLine1,omitempty"`
	AddressLine2 string `json:"addressLine2,omitempty"`
	City         string `json:"city,omitempty"`
	State        string `json:"state,omitempty"`
	Zip          string `json:"zip,omitempty"`
	Country      string `json:"country,omitempty"`

	CurrencyCode currency.Type `json:"currencyCode" orm:"default:usd"`

	// SpendingLimitResetFrequency governs how often committed spend resets
	// against employee limits: never/daily/weekly/monthly/yearly.
	SpendingLimitResetFrequency string `json:"spendingLimitResetFrequency" orm:"default:never"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (c *Company) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(c, ps); err != nil {
		return err
	}
	if len(c.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(c.Metadata_), &c.Metadata)
	}
	return err
}

func (c *Company) Save() ([]datastore.Property, error) {
	c.Metadata_ = string(json.EncodeBytes(&c.Metadata))
	return datastore.SaveStruct(c)
}

// Address joins the company's address lines into a single comma-separated
// string, skipping any empty segments.
func (c *Company) Address() string {
	parts := make([]string, 0, 6)
	for _, p := range []string{c.AddressLine1, c.AddressLine2, c.City, c.State, c.Zip, c.Country} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

func New(db *datastore.Datastore) *Company {
	c := new(Company)
	c.Init(db)
	return c
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("company")
}
