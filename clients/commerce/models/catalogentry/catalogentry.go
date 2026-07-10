// Package catalogentry is the CMS source-of-truth for the platform product
// catalog — the single list docs.<brand>, the console sidebar, and pricing all
// derive from. commerce owns the DATA (source + seed + edits); the shape is the
// @hanzo/products contract (that package owns the schema + the iconKey→component
// and brandColor→css code-maps). Pricing is native: an entry references a
// pricing plan by key (PricingId), or carries a fixed PriceCents.
//
// Conformance (GET /v1/commerce/catalog → the @hanzo/products CatalogEntry):
//   - iconKey  is a @hanzogui/lucide-icons-2 export NAME ("Brain") — never a component.
//   - brandColor is a swatch KEY ("violet") — never hex. @hanzo/products maps key→css.
//   - category is EXACTLY one of the 13 canonical labels (others are dropped by scope).
//   - route is "/<slug>", apiPath is /v1-prefixed, docsUrl is /docs/services/<slug>.
//   - pricingId is a pricing plans/<key>.json key, or null.
//   - brands is a category-derived convenience — NOT a hand-authored filter; the
//     server scopes by CATEGORY (categoriesForBrand), matching @hanzo/products
//     catalogForBrand.
package catalogentry

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[CatalogEntry]("catalog-entry") }

// Status enumerates a product's honest enablement state (mirrors the console
// registry): a working in-console module, a live external surface, or a
// primitive that ships with no console surface yet. No fabricated states.
const (
	StatusEnabled  = "enabled"  // in-console module that works
	StatusExternal = "external" // live external brand surface
	StatusSoon     = "soon"     // primitive shipped, no console surface yet
)

// CatalogEntry is one product in the platform catalog. Slug (== id) is the
// stable, globally-unique key the entry is addressed by.
type CatalogEntry struct {
	mixin.Model[CatalogEntry]

	Slug        string `json:"slug"`                             // stable id / path segment, e.g. "gateway"
	Name        string `json:"name"`                             // "Gateway"
	Category    string `json:"category"`                         // one of the 13 canonical labels
	Description string `json:"description" datastore:",noindex"` // one-line (additive; not in the core contract)
	Gcp         string `json:"gcp,omitempty"`                    // GCP product it stands in for

	// Presentation KEYS (CMS-editable; @hanzo/products resolves them).
	IconKey    string `json:"iconKey"`    // lucide export name, e.g. "Network"
	BrandColor string `json:"brandColor"` // swatch key, e.g. "blue"

	Route   string `json:"route"`   // "/<slug>"
	DocsUrl string `json:"docsUrl"` // https://docs.hanzo.ai/docs/services/<slug>
	ApiPath string `json:"apiPath"` // /v1-prefixed

	// PricingId references a pricing plan by key (plans/<key>.json); empty ⇒
	// projected as JSON null. PriceCents is an optional native fixed-price
	// override (additive to the contract).
	PricingId  string         `json:"pricingId"`
	PriceCents currency.Cents `json:"priceCents,omitempty"`
	Currency   currency.Type  `json:"currency,omitempty"`

	Status string `json:"status" orm:"default:enabled"` // enabled|external|soon
	Repo   string `json:"repo,omitempty"`               // source repo, e.g. "hanzoai/ai"
	Admin  bool   `json:"admin,omitempty"`              // admin-gated surface

	// Brands is a category-derived convenience preserved from the seed (the
	// server filters by category, not by this list). Stored as a noindex blob.
	Brands  []string `json:"brands,omitempty" datastore:"-"`
	Brands_ string   `json:"-" datastore:",noindex"`

	Order     int  `json:"order"`                        // display rank within a category
	Published bool `json:"published" orm:"default:true"` // gates from the public projection

	// ProductId optionally links this catalog surface to a real commerce
	// product for checkout/subscription. Empty = presentation/pricing only.
	ProductId string `json:"productId,omitempty"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (e *CatalogEntry) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(e, ps); err != nil {
		return err
	}
	if len(e.Brands_) > 0 {
		if err = json.DecodeBytes([]byte(e.Brands_), &e.Brands); err != nil {
			return err
		}
	}
	if len(e.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(e.Metadata_), &e.Metadata)
	}
	return err
}

func (e *CatalogEntry) Save() ([]datastore.Property, error) {
	e.Brands_ = string(json.EncodeBytes(&e.Brands))
	e.Metadata_ = string(json.EncodeBytes(&e.Metadata))
	return datastore.SaveStruct(e)
}

func New(db *datastore.Datastore) *CatalogEntry {
	e := new(CatalogEntry)
	e.Init(db)
	return e
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("catalog-entry")
}
