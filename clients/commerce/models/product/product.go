package product

import (
	"reflect"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/productcachedvalues"
	"github.com/hanzoai/cloud/clients/commerce/models/types/refs"
	"github.com/hanzoai/cloud/clients/commerce/models/variant"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Product]("product") }

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

type Option struct {
	// Ex. Size
	Name string `json:"name"`
	// Ex. [S, M, L]
	Values []string `json:"values"`
}

type Reservation struct {
	// Is the product reservable?
	IsReservable bool `json:"isReservable"`

	// Set to true if being reserved
	IsBeingReserved bool `json:"isBeingReserved"`

	// Usually initials of reserver
	ReservedBy string `json:"reservedBy"`

	// OrderID of Reservation
	OrderId string `json:"orderId"`

	// When was the product reserved
	ReservedAt time.Time `json:"ReservedAt"`
}

// Prune down since Product Listing has a lot of this info now
type Product struct {
	mixin.Model[Product]
	productcachedvalues.ProductCachedValues

	Ref refs.EcommerceRef `json:"ref,omitempty"`

	// Unique human readable id
	Slug string `json:"slug"`
	SKU  string `json:"sku,omitempty"`
	UPC  string `json:"upc,omitempty"`

	// Product Name
	Name string `json:"name"`

	// Product headline
	Headline string `json:"headline" datastore:",noindex"`

	// Product Excerpt
	Excerpt string `json:"excerpt" datastore:",noindex"`

	// Product Description
	Description string `json:"description" datastore:",noindex"`

	// Product Media
	Header Media   `json:"header"`
	Image  Media   `json:"image"`
	Media  []Media `json:"media"`

	// Is the product available
	Available bool `json:"available"`

	// Is product hidden from users
	Hidden bool `json:"hidden"`

	// Range in which product is available. If active, it takes precedent over
	// Available bool.
	Availability Availability `json:"availability"`

	// Is this product for preorder
	Preorder bool `json:"preorder"`

	// Pre-order now or Add to cart
	AddLabel string `json:"addLabel"`

	// List of variants
	Variants  []*variant.Variant `json:"variants" datastore:"-"`
	Variants_ string             `json:"-" datastore:",noindex"`

	// Reference to options used
	Options  []*Option `json:"options" datastore:"-"`
	Options_ string    `json:"-" datastore:",noindex"`

	Reservation Reservation `json:"reservation"`

	// Arbitrary key/value pairs associated with this order
	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (p *Product) Validator() *val.Validator {
	return val.New().
		Check("Slug").Exists().
		Check("SKU").Exists().
		Check("Name").Exists()
	// 	if p.Name == "" {
	// 		errs = append(errs, binding.Error{
	// 			FieldNames:     []string{"Title"},
	// 			Classification: "InputError",
	// 			Message:        "Product does not have a title.",
	// 		})
	// 	}

	// 	if len(p.Media) > 0 {
	// 		for _, media := range p.Media {
	// 			errs = media.Validate(req, errs)
	// 		}
	// 	}

	// 	if len(p.Variants) == 0 {
	// 		errs = append(errs, binding.Error{
	// 			FieldNames:     []string{"Variants"},
	// 			Classification: "InputError",
	// 			Message:        "No Variants on Product",
	// 		})
	// 	} else {
	// 		for _, v := range p.Variants {
	// 			errs = v.Validate(req, errs)
	// 		}
	// 	}
	// 	return errs
}

func (p *Product) Load(ps []datastore.Property) (err error) {
	// Ensure we're initialized
	p.Defaults()

	// Load supported properties
	if err = datastore.LoadStruct(p, ps); err != nil {
		return err
	}

	if len(p.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(p.Metadata_), &p.Metadata)
	}

	// Deserialize from datastore
	if len(p.Variants_) > 0 {
		err = json.DecodeBytes([]byte(p.Variants_), &p.Variants)
	}

	if len(p.Options_) > 0 {
		err = json.DecodeBytes([]byte(p.Options_), &p.Options)
	}

	return err
}

func (p *Product) Save() ([]datastore.Property, error) {
	// Serialize unsupported properties
	p.Metadata_ = string(json.EncodeBytes(&p.Metadata))
	p.Variants_ = string(json.EncodeBytes(&p.Variants))
	p.Options_ = string(json.Encode(&p.Options))

	// Save properties
	return datastore.SaveStruct(p)
}

func (p Product) DisplayName() string {
	return DisplayTitle(p.Name)
}

func (p Product) DisplayImage() Media {
	for _, media := range p.Media {
		if media.Type == MediaImage {
			return media
		}
	}
	return Media{}
}

func (p Product) DisplayPrice() string {
	return DisplayPrice(p.Currency, p.MinPrice())
}

func (p Product) MinPrice() currency.Cents {
	min := p.Variants[0].Price

	for _, v := range p.Variants {
		if v.Price < min {
			min = v.Price
		}
	}

	return min
}

// TODO: Don't do this.
func (p Product) VariantOptions(name string) (options []string) {
	set := make(map[string]bool)

	for _, v := range p.Variants {
		r := reflect.ValueOf(v)
		f := reflect.Indirect(r).FieldByName(name)
		v := f.String()
		if v != "" {
			set[v] = true
		}
	}

	for key := range set {
		options = append(options, key)
	}

	return options
}

func (p *Product) Defaults() {
	p.Variants = make([]*variant.Variant, 0)
	p.Options = make([]*Option, 0)
	p.Taxable = true
}

func New(db *datastore.Datastore) *Product {
	p := new(Product)
	p.Init(db)
	p.Defaults()
	return p
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("product")
}
