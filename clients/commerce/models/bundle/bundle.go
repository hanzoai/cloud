package bundle

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

func init() { orm.Register[Bundle]("bundle") }

// A bundle of Products/Variants to be listed on a store
type Bundle struct {
	mixin.Model[Bundle]

	// Unique human readable identifier
	Slug string `json:"slug"`

	// Name of bundle
	Name string `json:"name"`

	// Description of bundle
	Description string `datastore:",noindex" json:"description"`

	// Image/Video/Other Media to show in a gallery
	Media []Media `json:"media"`

	// Is the bundle available
	Available bool `json:"available"`

	// Is product hidden from users
	Hidden bool `json:"hidden"`

	// Range in which bundle is available. If active, it takes precedent
	// over Available bool.
	Availability Availability `json:"availability"`

	// Show this on store?
	Published bool `json:"published"`

	// Is this a preorder?
	Preorder bool `json:"preorder"`

	// Lists of products or specific product variants that are part of this
	// bundle
	ProductIds []string `json:"productIds"`
	VariantIds []string `json:"variantIds"`
}

func (c *Bundle) Validator() *val.Validator {
	return val.New().
		Check("Slug").Exists().
		Check("Name").Exists()
}

func (c Bundle) GetDescriptionParagraphs() []string {
	return SplitParagraph(c.Description)
}

func (c Bundle) DisplayTitle() string {
	return DisplayTitle(c.Name)
}

// New creates a new Bundle wired to the given datastore.
func New(db *datastore.Datastore) *Bundle {
	b := new(Bundle)
	b.Init(db)
	return b
}

// Query returns a datastore query for bundles.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("bundle")
}
