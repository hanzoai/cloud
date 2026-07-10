package collection

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

func init() { orm.Register[Collection]("collection") }

// A collection of Products/Variants to be listed on a store
type Collection struct {
	mixin.Model[Collection]

	// Unique human readable identifier
	Slug string `json:"slug"`

	// Name of Collection
	Name string `json:"name"`

	// Description of collection
	Description string `datastore:",noindex" json:"description"`

	// Image/Video/Other Media to show in a gallery
	Media []Media `json:"media" orm:"default:[]"`

	// Is the collection available
	Available bool `json:"available"`

	// Range in which collection is available. If active, it takes precedent
	// over Available bool.
	Availability struct {
		Active    bool      `json:"active'"`
		StartDate time.Time `json:"startDate"`
		EndDate   time.Time `json:"endDate"`
	} `json:"availability"`

	// Show this on store?
	Published bool `json:"published"`

	// Is this a preorder?
	Preorder bool `json:"preorder"`

	// Is this in stock?
	OutOfStock bool `json:"outOfStock"`

	// Lists of products or specific product variants that are part of this collection
	ProductIds []string `json:"productIds" orm:"default:[]"`
	VariantIds []string `json:"variantIds" orm:"default:[]"`

	History []Event `json:"-" orm:"default:[]"`
}

func (c *Collection) Validator() *val.Validator {
	return val.New().
		Check("Slug").Exists().
		Check("Name").Exists()
}

func (c Collection) GetDescriptionParagraphs() []string {
	return SplitParagraph(c.Description)
}

func (c Collection) DisplayTitle() string {
	return DisplayTitle(c.Name)
}

func New(db *datastore.Datastore) *Collection {
	c := new(Collection)
	c.Init(db)
	return c
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("collection")
}
