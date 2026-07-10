// Package product provides the Product model for e-commerce product management.
// This v2 implementation uses the new db.DB interface with SQLite backends
// and supports vector embeddings for AI-powered recommendations.
package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/productcachedvalues"
	"github.com/hanzoai/cloud/clients/commerce/models/types/refs"
	"github.com/hanzoai/cloud/clients/commerce/util/val"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// Common errors for Product operations
var (
	ErrProductNotFound    = errors.New("product: not found")
	ErrProductInvalidID   = errors.New("product: invalid id")
	ErrProductInvalidSlug = errors.New("product: invalid slug")
	ErrProductDuplicate   = errors.New("product: duplicate slug or sku")
	ErrProductValidation  = errors.New("product: validation failed")
	ErrEmbeddingFailed    = errors.New("product: failed to store embedding")
)

// OptionV2 represents a product option (e.g., Size, Color)
type OptionV2 struct {
	// Name is the option name (e.g., "Size", "Color")
	Name string `json:"name"`
	// Values are the available option values (e.g., ["S", "M", "L"])
	Values []string `json:"values"`
}

// ReservationV2 represents product reservation state
type ReservationV2 struct {
	// IsReservable indicates if the product can be reserved
	IsReservable bool `json:"isReservable"`
	// IsBeingReserved indicates if currently reserved
	IsBeingReserved bool `json:"isBeingReserved"`
	// ReservedBy is usually the initials or ID of the reserver
	ReservedBy string `json:"reservedBy,omitempty"`
	// OrderId is the order ID associated with the reservation
	OrderId string `json:"orderId,omitempty"`
	// ReservedAt is when the product was reserved
	ReservedAt time.Time `json:"reservedAt,omitempty"`
}

// WeightV2 represents product weight for shipping calculations
type WeightV2 struct {
	Value float64  `json:"value"`
	Unit  MassUnit `json:"unit"`
}

// VariantV2 represents a product variant with its own SKU and pricing
type VariantV2 struct {
	// ID is the unique variant identifier
	ID string `json:"id"`
	// ProductId links back to the parent product
	ProductId string `json:"productId"`
	// SKU is the stock keeping unit
	SKU string `json:"sku"`
	// UPC is the universal product code
	UPC string `json:"upc,omitempty"`
	// Name is the variant name
	Name string `json:"name"`
	// Price in cents
	Price currency.Cents `json:"price"`
	// ListPrice is the original/list price
	ListPrice currency.Cents `json:"listPrice,omitempty"`
	// Available indicates if this variant is available for purchase
	Available bool `json:"available"`
	// Inventory count
	Inventory int `json:"inventory"`
	// Sold count
	Sold int `json:"sold"`
	// Options are the selected option values for this variant
	Options []VariantOption `json:"options"`
	// Media for this variant
	Header Media   `json:"header,omitempty"`
	Image  Media   `json:"image,omitempty"`
	Media  []Media `json:"media,omitempty"`
	// Weight for shipping calculations
	Weight WeightV2 `json:"weight,omitempty"`
}

// VariantOption represents a selected option value for a variant
type VariantOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ProductV2 is the modernized product model using the new db.DB interface.
// It stores products in organization SQLite databases with JSON fields
// for flexible metadata and supports vector embeddings for AI recommendations.
type ProductV2 struct {
	productcachedvalues.ProductCachedValues

	// Core identification
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Deleted   bool      `json:"deleted,omitempty"`

	// Organization context
	OrgID     string `json:"orgId"`
	Namespace string `json:"namespace,omitempty"`

	// External references
	Ref refs.EcommerceRef `json:"ref,omitempty"`

	// Human-readable identifiers
	Slug string `json:"slug"`
	SKU  string `json:"sku,omitempty"`
	UPC  string `json:"upc,omitempty"`

	// Product content
	Name        string `json:"name"`
	Headline    string `json:"headline,omitempty"`
	Excerpt     string `json:"excerpt,omitempty"`
	Description string `json:"description,omitempty"`

	// Media assets
	Header Media   `json:"header,omitempty"`
	Image  Media   `json:"image,omitempty"`
	Media  []Media `json:"media,omitempty"`

	// Availability
	Available    bool         `json:"available"`
	Hidden       bool         `json:"hidden"`
	Availability Availability `json:"availability,omitempty"`
	Preorder     bool         `json:"preorder"`
	AddLabel     string       `json:"addLabel,omitempty"`

	// Variants and options
	Variants []*VariantV2 `json:"variants"`
	Options  []*OptionV2  `json:"options"`

	// Reservation state
	Reservation ReservationV2 `json:"reservation,omitempty"`

	// Flexible metadata (stored as JSON)
	Metadata Map `json:"metadata,omitempty"`

	// AI/ML fields
	EmbeddingVersion string    `json:"embeddingVersion,omitempty"`
	EmbeddingUpdated time.Time `json:"embeddingUpdated,omitempty"`

	// Collections and categories
	CollectionIDs []string `json:"collectionIds,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Categories    []string `json:"categories,omitempty"`

	// SEO fields
	SEOTitle       string `json:"seoTitle,omitempty"`
	SEODescription string `json:"seoDescription,omitempty"`
	SEOKeywords    string `json:"seoKeywords,omitempty"`

	// Internal state (not persisted)
	db    db.DB  `json:"-"`
	key   db.Key `json:"-"`
	isNew bool   `json:"-"`
}

// Kind returns the entity kind for database operations
func (p *ProductV2) Kind() string {
	return "product"
}

// SyncToDatastore returns true if this product should be synced to analytics
func (p *ProductV2) SyncToDatastore() bool {
	return true
}

// NewProductV2 creates a new product instance bound to the given database
func NewProductV2(database db.DB, orgID string) *ProductV2 {
	p := &ProductV2{
		db:       database,
		OrgID:    orgID,
		Variants: make([]*VariantV2, 0),
		Options:  make([]*OptionV2, 0),
		Metadata: make(Map),
		isNew:    true,
	}
	// Set default cached values
	p.Taxable = true
	return p
}

// Validator returns a validator for product fields
func (p *ProductV2) Validator() *val.Validator {
	return val.New().
		Check("Slug").Exists().
		Check("SKU").Exists().
		Check("Name").Exists()
}

// Validate checks all required fields and returns an error if invalid
func (p *ProductV2) Validate() error {
	v := p.Validator()
	errs := v.Exec(p)
	if len(errs) > 0 {
		return fmt.Errorf("%w: %v", ErrProductValidation, errs[0])
	}
	return nil
}

// Key returns the database key for this product
func (p *ProductV2) Key() db.Key {
	if p.key == nil {
		if p.ID != "" {
			p.key = p.db.NewKey(p.Kind(), p.ID, 0, nil)
		} else {
			p.key = p.db.NewIncompleteKey(p.Kind(), nil)
		}
	}
	return p.key
}

// SetKey sets the key and ID for this product
func (p *ProductV2) SetKey(key db.Key) {
	p.key = key
	p.ID = key.Encode()
}

// Get retrieves a product by key
func (p *ProductV2) Get(ctx context.Context, key db.Key) error {
	if err := p.db.Get(ctx, key, p); err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return ErrProductNotFound
		}
		return err
	}
	p.key = key
	p.isNew = false
	return nil
}

// GetByID retrieves a product by its string ID
func (p *ProductV2) GetByID(ctx context.Context, id string) error {
	if id == "" {
		return ErrProductInvalidID
	}
	key := p.db.NewKey(p.Kind(), id, 0, nil)
	return p.Get(ctx, key)
}

// GetBySlug retrieves a product by its slug
func (p *ProductV2) GetBySlug(ctx context.Context, slug string) error {
	if slug == "" {
		return ErrProductInvalidSlug
	}

	// Use lowercase 'slug' to match JSON field name
	key, err := p.db.Query(p.Kind()).
		Filter("slug=", slug).
		First(ctx, p)

	if err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return ErrProductNotFound
		}
		return err
	}

	p.key = key
	p.isNew = false
	return nil
}

// GetBySKU retrieves a product by its SKU
func (p *ProductV2) GetBySKU(ctx context.Context, sku string) error {
	// Use lowercase 'sku' to match JSON field name
	key, err := p.db.Query(p.Kind()).
		Filter("sku=", sku).
		First(ctx, p)

	if err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return ErrProductNotFound
		}
		return err
	}

	p.key = key
	p.isNew = false
	return nil
}

// Put saves the product to the database
func (p *ProductV2) Put(ctx context.Context) error {
	now := time.Now()

	if p.isNew || p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now

	// Get the key and set ID before marshaling so it's included in JSON
	k := p.Key()
	p.ID = k.Encode()

	key, err := p.db.Put(ctx, k, p)
	if err != nil {
		return err
	}

	p.key = key
	p.isNew = false
	return nil
}

// Create creates a new product (validates first)
func (p *ProductV2) Create(ctx context.Context) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return p.Put(ctx)
}

// Update saves changes to an existing product
func (p *ProductV2) Update(ctx context.Context) error {
	if p.isNew {
		return errors.New("product: cannot update unsaved product")
	}
	return p.Put(ctx)
}

// Delete soft-deletes the product
func (p *ProductV2) Delete(ctx context.Context) error {
	p.Deleted = true
	return p.Put(ctx)
}

// HardDelete permanently removes the product
func (p *ProductV2) HardDelete(ctx context.Context) error {
	return p.db.Delete(ctx, p.Key())
}

// PutEmbedding stores a vector embedding for AI similarity search
func (p *ProductV2) PutEmbedding(ctx context.Context, vector []float32, version string) error {
	if len(vector) == 0 {
		return ErrEmbeddingFailed
	}

	metadata := map[string]interface{}{
		"productId":   p.ID,
		"slug":        p.Slug,
		"sku":         p.SKU,
		"name":        p.Name,
		"orgId":       p.OrgID,
		"available":   p.Available,
		"price":       p.MinPrice(),
		"collections": p.CollectionIDs,
		"tags":        p.Tags,
	}

	if err := p.db.PutVector(ctx, p.Kind(), p.ID, vector, metadata); err != nil {
		return fmt.Errorf("%w: %v", ErrEmbeddingFailed, err)
	}

	p.EmbeddingVersion = version
	p.EmbeddingUpdated = time.Now()
	return p.Put(ctx)
}

// DisplayName returns a formatted display name
func (p *ProductV2) DisplayName() string {
	return DisplayTitle(p.Name)
}

// DisplayImage returns the first image media
func (p *ProductV2) DisplayImage() Media {
	for _, media := range p.Media {
		if media.Type == MediaImage {
			return media
		}
	}
	if p.Image.Type != "" {
		return p.Image
	}
	return Media{}
}

// DisplayPrice returns a formatted price string
func (p *ProductV2) DisplayPrice() string {
	return DisplayPrice(p.Currency, p.MinPrice())
}

// MinPrice returns the lowest variant price
func (p *ProductV2) MinPrice() currency.Cents {
	if len(p.Variants) == 0 {
		return 0
	}

	min := p.Variants[0].Price
	for _, v := range p.Variants {
		if v.Price < min {
			min = v.Price
		}
	}
	return min
}

// MaxPrice returns the highest variant price
func (p *ProductV2) MaxPrice() currency.Cents {
	if len(p.Variants) == 0 {
		return 0
	}

	max := p.Variants[0].Price
	for _, v := range p.Variants {
		if v.Price > max {
			max = v.Price
		}
	}
	return max
}

// PriceRange returns min and max prices
func (p *ProductV2) PriceRange() (min, max currency.Cents) {
	return p.MinPrice(), p.MaxPrice()
}

// TotalInventory returns the sum of all variant inventories
func (p *ProductV2) TotalInventory() int {
	total := 0
	for _, v := range p.Variants {
		total += v.Inventory
	}
	return total
}

// TotalSold returns the sum of all variant sales
func (p *ProductV2) TotalSold() int {
	total := 0
	for _, v := range p.Variants {
		total += v.Sold
	}
	return total
}

// VariantByID finds a variant by its ID
func (p *ProductV2) VariantByID(id string) *VariantV2 {
	for _, v := range p.Variants {
		if v.ID == id {
			return v
		}
	}
	return nil
}

// VariantBySKU finds a variant by its SKU
func (p *ProductV2) VariantBySKU(sku string) *VariantV2 {
	for _, v := range p.Variants {
		if v.SKU == sku {
			return v
		}
	}
	return nil
}

// VariantOptions returns unique values for a given option name across all variants
func (p *ProductV2) VariantOptions(optionName string) []string {
	seen := make(map[string]bool)
	var values []string

	for _, variant := range p.Variants {
		for _, opt := range variant.Options {
			if opt.Name == optionName && !seen[opt.Value] {
				seen[opt.Value] = true
				values = append(values, opt.Value)
			}
		}
	}
	return values
}

// AvailableVariants returns only variants that are available for purchase
func (p *ProductV2) AvailableVariants() []*VariantV2 {
	var available []*VariantV2
	for _, v := range p.Variants {
		if v.Available && v.Inventory > 0 {
			available = append(available, v)
		}
	}
	return available
}

// AddVariant adds a new variant to the product
func (p *ProductV2) AddVariant(v *VariantV2) {
	v.ProductId = p.ID
	p.Variants = append(p.Variants, v)
}

// RemoveVariant removes a variant by ID
func (p *ProductV2) RemoveVariant(id string) bool {
	for i, v := range p.Variants {
		if v.ID == id {
			p.Variants = append(p.Variants[:i], p.Variants[i+1:]...)
			return true
		}
	}
	return false
}

// Clone creates a deep copy of the product
func (p *ProductV2) Clone() *ProductV2 {
	data, _ := json.Marshal(p)
	clone := &ProductV2{}
	json.Unmarshal(data, clone)
	clone.db = p.db
	clone.key = nil
	clone.ID = ""
	clone.isNew = true
	return clone
}

// ToJSON returns the JSON representation
func (p *ProductV2) ToJSON() []byte {
	data, _ := json.Marshal(p)
	return data
}

// FromJSON populates the product from JSON
func (p *ProductV2) FromJSON(data []byte) error {
	return json.Unmarshal(data, p)
}

// ProductQueryV2 provides query operations for products
type ProductQueryV2 struct {
	db    db.DB
	query db.Query
}

// QueryProductsV2 creates a new product query
func QueryProductsV2(database db.DB) *ProductQueryV2 {
	return &ProductQueryV2{
		db:    database,
		query: database.Query("product"),
	}
}

// Filter adds a filter condition
func (q *ProductQueryV2) Filter(field string, value interface{}) *ProductQueryV2 {
	q.query = q.query.Filter(field, value)
	return q
}

// Available filters to only available products
func (q *ProductQueryV2) Available() *ProductQueryV2 {
	// SQLite JSON stores booleans as 1/0
	return q.Filter("available=", 1)
}

// NotDeleted filters out deleted products
func (q *ProductQueryV2) NotDeleted() *ProductQueryV2 {
	// SQLite JSON stores booleans as 1/0
	return q.Filter("deleted=", 0)
}

// Visible filters to only visible (not hidden) products
func (q *ProductQueryV2) Visible() *ProductQueryV2 {
	// SQLite JSON stores booleans as 1/0
	return q.Filter("hidden=", 0)
}

// InCollection filters by collection ID
func (q *ProductQueryV2) InCollection(collectionID string) *ProductQueryV2 {
	// Note: This requires array contains support in the query layer
	// For now, we'll filter in-memory after GetAll
	return q
}

// WithTag filters by tag
func (q *ProductQueryV2) WithTag(tag string) *ProductQueryV2 {
	// Similar to InCollection, requires array contains
	return q
}

// Order sets the sort order
func (q *ProductQueryV2) Order(field string) *ProductQueryV2 {
	q.query = q.query.Order(field)
	return q
}

// OrderDesc sets descending sort order
func (q *ProductQueryV2) OrderDesc(field string) *ProductQueryV2 {
	q.query = q.query.OrderDesc(field)
	return q
}

// Limit sets the maximum results
func (q *ProductQueryV2) Limit(n int) *ProductQueryV2 {
	q.query = q.query.Limit(n)
	return q
}

// Offset sets the starting offset
func (q *ProductQueryV2) Offset(n int) *ProductQueryV2 {
	q.query = q.query.Offset(n)
	return q
}

// GetAll retrieves all matching products
func (q *ProductQueryV2) GetAll(ctx context.Context) ([]*ProductV2, error) {
	var products []*ProductV2
	keys, err := q.query.GetAll(ctx, &products)
	if err != nil {
		return nil, err
	}

	// Bind database and keys to products
	for i, p := range products {
		p.db = q.db
		p.key = keys[i]
		p.isNew = false
	}

	return products, nil
}

// First retrieves the first matching product
func (q *ProductQueryV2) First(ctx context.Context) (*ProductV2, error) {
	p := &ProductV2{db: q.db}
	key, err := q.query.First(ctx, p)
	if err != nil {
		if errors.Is(err, db.ErrNoSuchEntity) {
			return nil, ErrProductNotFound
		}
		return nil, err
	}
	p.key = key
	p.isNew = false
	return p, nil
}

// Count returns the number of matching products
func (q *ProductQueryV2) Count(ctx context.Context) (int, error) {
	return q.query.Count(ctx)
}

// Keys returns only the keys of matching products
func (q *ProductQueryV2) Keys(ctx context.Context) ([]db.Key, error) {
	return q.query.Keys(ctx)
}

// SimilarProducts finds products similar to the given product using vector search
func SimilarProducts(ctx context.Context, database db.DB, productID string, embedding []float32, limit int) ([]*ProductV2, error) {
	results, err := database.VectorSearch(ctx, &db.VectorSearchOptions{
		Kind:     "product",
		Vector:   embedding,
		Limit:    limit + 1, // +1 to exclude self
		MinScore: 0.7,       // Minimum similarity threshold
	})
	if err != nil {
		return nil, err
	}

	var products []*ProductV2
	for _, r := range results {
		if r.ID == productID {
			continue // Skip the source product
		}

		p := NewProductV2(database, "")
		if err := p.GetByID(ctx, r.ID); err == nil {
			products = append(products, p)
		}

		if len(products) >= limit {
			break
		}
	}

	return products, nil
}

// BulkCreate creates multiple products in a single transaction
func BulkCreate(ctx context.Context, database db.DB, products []*ProductV2) error {
	return database.RunInTransaction(ctx, func(tx db.Transaction) error {
		for _, p := range products {
			if err := p.Validate(); err != nil {
				return err
			}

			now := time.Now()
			p.CreatedAt = now
			p.UpdatedAt = now

			key, err := tx.Put(p.Key(), p)
			if err != nil {
				return err
			}
			p.key = key
			p.ID = key.Encode()
			p.isNew = false
		}
		return nil
	}, nil)
}

// ProductIndex generates search index data for a product
func (p *ProductV2) ProductIndex() map[string]interface{} {
	return map[string]interface{}{
		"id":           p.ID,
		"orgId":        p.OrgID,
		"slug":         p.Slug,
		"sku":          p.SKU,
		"name":         p.Name,
		"headline":     p.Headline,
		"description":  p.Description,
		"available":    p.Available,
		"hidden":       p.Hidden,
		"preorder":     p.Preorder,
		"minPrice":     p.MinPrice(),
		"maxPrice":     p.MaxPrice(),
		"collections":  p.CollectionIDs,
		"tags":         p.Tags,
		"categories":   p.Categories,
		"variantCount": len(p.Variants),
		"createdAt":    p.CreatedAt,
		"updatedAt":    p.UpdatedAt,
	}
}

// EmbeddingText generates text for creating AI embeddings
func (p *ProductV2) EmbeddingText() string {
	// Combine relevant text fields for embedding generation
	parts := []string{p.Name}

	if p.Headline != "" {
		parts = append(parts, p.Headline)
	}
	if p.Excerpt != "" {
		parts = append(parts, p.Excerpt)
	}
	if p.Description != "" {
		parts = append(parts, p.Description)
	}

	for _, tag := range p.Tags {
		parts = append(parts, tag)
	}
	for _, cat := range p.Categories {
		parts = append(parts, cat)
	}

	// Join with spaces
	result := ""
	for _, part := range parts {
		if part != "" {
			if result != "" {
				result += " "
			}
			result += part
		}
	}
	return result
}

// VariantOptionsDeprecated returns unique option values by field name using reflection
// Deprecated: Use VariantOptions instead
func (p *ProductV2) VariantOptionsDeprecated(name string) (options []string) {
	set := make(map[string]bool)

	for _, v := range p.Variants {
		r := reflect.ValueOf(v)
		f := reflect.Indirect(r).FieldByName(name)
		val := f.String()
		if val != "" {
			set[val] = true
		}
	}

	for key := range set {
		options = append(options, key)
	}
	return options
}
