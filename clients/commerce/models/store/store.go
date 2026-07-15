package store

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/shippingrates"
	"github.com/hanzoai/cloud/clients/commerce/models/taxrates"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/shipping"
	"github.com/hanzoai/cloud/clients/commerce/models/types/weight"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/reflect"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// Everything is a pointer, which allows fields to be nil. This way when we
// serialize to/from JSON we know what has and has not been set.
type Listing struct {
	// Not customizable
	ProductId string        `json:"productId,omitempty"`
	Slug      string        `json:"slug,omitempty"`
	VariantId string        `json:"variantId,omitempty"`
	SKU       string        `json:"sku,omitempty"`
	Currency  currency.Type `json:"currency,omitempty"`

	// Everything else May be overriden

	Name *string `json:"name"`

	Headline    *string `json:"headline,omitempty"`
	Excerpt     *string `json:"excerpt,omitempty"`
	Description *string `json:"description,omitempty"`

	// Product Media
	HeaderImage *Media   `json:"headerImage,omitempty"`
	Media       *[]Media `json:"media,omitempty"`

	Sold *int `json:"sold"`

	Price     *currency.Cents `json:"price,omitempty"`
	ListPrice *currency.Cents `json:"listPrice,omitempty"`
	Shipping  *currency.Cents `json:"shipping,omitempty"`
	Taxable   *bool           `json:"taxable,omitempty"`

	WeightUnit *weight.Unit `json:"weightUnit,omitempty"`

	Available    *bool         `json:"available,omitempty"`
	Availability *Availability `json:"availability,omitempty"`

	Hidden *bool `json:"hidden,omitempty"`
}

var ListingFields = reflect.FieldNames(Listing{})

type Listings map[string]Listing
type ShippingRateTable map[string]shipping.Rates

func init() { orm.Register[Store]("store") }

type Store struct {
	mixin.Model[Store]

	// Full name of store
	Name string `json:"name"`

	// Unique human readable id for url <slug>.hanzo.aie
	Slug string `json:"slug"`

	// Where this is hosted if not on hanzo.ai
	Domain string `json:"domain"`
	Prefix string `json:"prefix"`

	// Currency for store
	Currency currency.Type `json:"currency"`

	// Taxation information

	Address  Address   `json:"address,omitempty"`
	TaxNexus []Address `json:"taxNexus,omitempty"`

	// Shipping Rate Table, country name to shipping rate
	// ShippingRateTable  ShippingRateTable `json:"shippingRates" datastore:"-"`
	// ShippingRateTable_ string            `json:"-" datastore:",noindex"`

	// Overrides per item
	Listings  Listings `json:"listings" datastore:"-" orm:"default:{}"`
	Listings_ string   `json:"-" datastore:",noindex"`

	Salesforce struct {
		PriceBookId string `json:"PriceBookId"`
	} `json:"-"`

	Email           string `json:"email,omitempty"`
	Phone           string `json:"phone,omitempty"`
	Timezone        string `json:"timezone,omitempty"`
	ReferralBaseUrl string `json:"referralBaseUrl,omitempty"`

	Mailchimp struct {
		ListId string `json:"listId"`
		APIKey string `json:"apiKey"`
	} `json:"mailchimp,omitempty"`
}

// Defaults sets runtime defaults that cannot be expressed as orm tags.
func (s *Store) Defaults() {
	if s.Currency == "" {
		s.Currency = currency.USD
	}
}

func (s *Store) Load(ps []datastore.Property) (err error) {
	// Apply defaults only when loading actual properties (appengine path).
	// When ps is nil (SQLite callPostLoad hook), the struct was already
	// populated by unmarshalForDB — calling Defaults() here would overwrite
	// a stored empty Currency with "usd", masking the org-level currency.
	if ps != nil {
		s.Defaults()
	}

	// Load supported properties
	if err = datastore.LoadStruct(s, ps); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(s.Listings_) > 0 {
		err = json.DecodeBytes([]byte(s.Listings_), &s.Listings)
	}

	// if len(s.ShippingRateTable_) > 0 {
	// 	err = json.DecodeBytes([]byte(s.ShippingRateTable_), &s.ShippingRateTable)
	// }

	return err
}

func (s *Store) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	s.Listings_ = string(json.EncodeBytes(&s.Listings))
	// s.ShippingRateTable_ = string(json.EncodeBytes(&s.ShippingRateTable))

	// Save properties
	return datastore.SaveStruct(s)
}

func (s *Store) Validator() *val.Validator {
	return val.New()
}

// Add a new listing to the listings map
func (s *Store) AddListing(id string, listing Listing) {
	listing.Currency = s.Currency
	s.Listings[id] = listing
}

// Update product/variant using listing for said item
func (s *Store) UpdateFromListing(entity mixin.Entity) {
	// Check if we have a listing for this product/variant
	listing, ok := s.Listings[entity.Id()]
	if !ok {
		log.Warn("No listing found that matches given %s", entity.Kind())
		return
	}

	log.Info("Listing Found %s", entity.Id(), s.Context())

	ev := reflect.Indirect(reflect.ValueOf(entity))
	lv := reflect.ValueOf(listing)

	// Loop over listing fields and set any that this listing has that are non-nil
	for _, name := range ListingFields {
		field := ev.FieldByName(name)
		val := reflect.Indirect(lv.FieldByName(name))
		if val.IsValid() && !reflect.IsZero(val) && field.IsValid() {
			field.Set(val)
			log.Info("Name %v, Field %v", name, field, s.Context())
		}
	}

	// Ensure currency is set to currency of store
	field := ev.FieldByName("Currency")
	field.Set(reflect.ValueOf(s.Currency))
}

// Return TaxRates
func (s Store) GetTaxRates() (*taxrates.TaxRates, error) {
	tr := taxrates.New(s.Datastore())
	if ok, err := tr.Query().Filter("StoreId=", s.Id()).Get(); !ok {
		return nil, err
	}

	return tr, nil
}

// Return ShippingRates
func (s Store) GetShippingRates() (*shippingrates.ShippingRates, error) {
	sr := shippingrates.New(s.Datastore())
	if ok, err := sr.Query().Filter("StoreId=", s.Id()).Get(); !ok {
		return nil, err
	}

	return sr, nil
}

func New(db *datastore.Datastore) *Store {
	s := new(Store)
	s.Init(db)
	s.Defaults()
	return s
}

// DefaultSlug is the stable slug of the store every org gets on first use. It is
// safe to reuse across tenants because each org's merchant rows live in its OWN
// namespaced store (datastore.NewNamespaced), so the slug never collides.
const DefaultSlug = "default"

// EnsureDefault returns the org's default store, creating it on first use. This is
// the ONE canonical way an org gets its commerce store: idempotent (keyed by the
// stable DefaultSlug), org-scoped (db MUST be a per-org datastore.NewNamespaced),
// and carrying NO payment credentials — binding Square/Stripe stays a separate
// business step. It replaces the long-dead commented-out provisioning in
// util/provision: a store is lazily provisioned at the first authenticated
// GET /v1/store/current instead of eagerly at org creation.
func EnsureDefault(db *datastore.Datastore) (*Store, error) {
	s := New(db)
	s.Name = "Default Store"
	s.Slug = DefaultSlug
	if err := s.GetOrCreate("Slug=", DefaultSlug); err != nil {
		return nil, err
	}
	return s, nil
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("store")
}
