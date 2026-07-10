// Package product provides model initialization and query helpers for the v2 Product model.
package product

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// kindV2 is the entity kind for products in the database
const kindV2 = "product"

// KindV2 returns the entity kind
func KindV2() string {
	return kindV2
}

// NewV2 creates a new product with the given database and organization ID
func NewV2(database db.DB, orgID string) *ProductV2 {
	return NewProductV2(database, orgID)
}

// GetV2 retrieves a product by ID
func GetV2(ctx context.Context, database db.DB, id string) (*ProductV2, error) {
	p := NewProductV2(database, "")
	if err := p.GetByID(ctx, id); err != nil {
		return nil, err
	}
	return p, nil
}

// GetBySlugV2 retrieves a product by slug
func GetBySlugV2(ctx context.Context, database db.DB, slug string) (*ProductV2, error) {
	p := NewProductV2(database, "")
	if err := p.GetBySlug(ctx, slug); err != nil {
		return nil, err
	}
	return p, nil
}

// GetBySKUV2 retrieves a product by SKU
func GetBySKUV2(ctx context.Context, database db.DB, sku string) (*ProductV2, error) {
	p := NewProductV2(database, "")
	if err := p.GetBySKU(ctx, sku); err != nil {
		return nil, err
	}
	return p, nil
}

// QueryV2 creates a new product query
func QueryV2(database db.DB) *ProductQueryV2 {
	return QueryProductsV2(database)
}

// ListV2 retrieves all products for an organization
func ListV2(ctx context.Context, database db.DB, orgID string, limit, offset int) ([]*ProductV2, error) {
	query := QueryV2(database).
		Filter("orgId=", orgID).
		NotDeleted().
		Order("-updatedAt").
		Limit(limit).
		Offset(offset)

	return query.GetAll(ctx)
}

// ListAvailableV2 retrieves all available products
func ListAvailableV2(ctx context.Context, database db.DB, orgID string, limit, offset int) ([]*ProductV2, error) {
	query := QueryV2(database).
		Filter("orgId=", orgID).
		Available().
		NotDeleted().
		Visible().
		Order("-updatedAt").
		Limit(limit).
		Offset(offset)

	return query.GetAll(ctx)
}

// CountV2 returns the total number of products
func CountV2(ctx context.Context, database db.DB, orgID string) (int, error) {
	return QueryV2(database).
		Filter("orgId=", orgID).
		NotDeleted().
		Count(ctx)
}

// SearchV2Options configures product search
type SearchV2Options struct {
	OrgID       string
	Query       string   // Text search query
	Collections []string // Filter by collection IDs
	Tags        []string // Filter by tags
	Categories  []string // Filter by categories
	MinPrice    currency.Cents
	MaxPrice    currency.Cents
	Available   *bool
	Hidden      *bool
	Limit       int
	Offset      int
	SortBy      string // Field to sort by
	SortDesc    bool   // Sort descending
}

// SearchV2 performs a filtered search for products
// Note: Full-text search requires additional indexing setup
func SearchV2(ctx context.Context, database db.DB, opts SearchV2Options) ([]*ProductV2, error) {
	query := QueryV2(database)

	if opts.OrgID != "" {
		query = query.Filter("OrgID=", opts.OrgID)
	}

	if opts.Available != nil && *opts.Available {
		query = query.Available()
	}

	if opts.Hidden != nil && !*opts.Hidden {
		query = query.Visible()
	}

	query = query.NotDeleted()

	if opts.SortBy != "" {
		if opts.SortDesc {
			query = query.OrderDesc(opts.SortBy)
		} else {
			query = query.Order(opts.SortBy)
		}
	} else {
		query = query.Order("-UpdatedAt")
	}

	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}

	products, err := query.GetAll(ctx)
	if err != nil {
		return nil, err
	}

	// Post-filter for array fields (collections, tags, categories, price)
	// This is less efficient but necessary until we add array query support
	var filtered []*ProductV2
	for _, p := range products {
		// Filter by collections
		if len(opts.Collections) > 0 && !hasAnyString(p.CollectionIDs, opts.Collections) {
			continue
		}

		// Filter by tags
		if len(opts.Tags) > 0 && !hasAnyString(p.Tags, opts.Tags) {
			continue
		}

		// Filter by categories
		if len(opts.Categories) > 0 && !hasAnyString(p.Categories, opts.Categories) {
			continue
		}

		// Filter by price range
		minPrice := p.MinPrice()
		if opts.MinPrice > 0 && minPrice < opts.MinPrice {
			continue
		}
		if opts.MaxPrice > 0 && minPrice > opts.MaxPrice {
			continue
		}

		filtered = append(filtered, p)
	}

	return filtered, nil
}

// hasAnyString checks if any element in source is in targets
func hasAnyString(source, targets []string) bool {
	targetSet := make(map[string]bool)
	for _, t := range targets {
		targetSet[t] = true
	}
	for _, s := range source {
		if targetSet[s] {
			return true
		}
	}
	return false
}

// GetMultiV2 retrieves multiple products by ID
func GetMultiV2(ctx context.Context, database db.DB, ids []string) ([]*ProductV2, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	keys := make([]db.Key, len(ids))
	for i, id := range ids {
		keys[i] = database.NewKey(kindV2, id, 0, nil)
	}

	var products []*ProductV2
	if err := database.GetMulti(ctx, keys, &products); err != nil {
		return nil, err
	}

	// Bind database to products
	for i, p := range products {
		if p != nil {
			p.db = database
			p.key = keys[i]
			p.isNew = false
		}
	}

	return products, nil
}

// DeleteMultiV2 soft-deletes multiple products
func DeleteMultiV2(ctx context.Context, database db.DB, ids []string) error {
	products, err := GetMultiV2(ctx, database, ids)
	if err != nil {
		return err
	}

	return database.RunInTransaction(ctx, func(tx db.Transaction) error {
		for _, p := range products {
			if p != nil {
				p.Deleted = true
				p.UpdatedAt = time.Now()
				if _, err := tx.Put(p.Key(), p); err != nil {
					return err
				}
			}
		}
		return nil
	}, nil)
}

// MigrateFromV1 converts a v1 Product to v2 ProductV2
// This is used during the migration process
func MigrateFromV1(v1 *Product, database db.DB, orgID string) *ProductV2 {
	v2 := NewProductV2(database, orgID)

	// Copy basic fields
	v2.ID = v1.Id_
	v2.CreatedAt = v1.CreatedAt
	v2.UpdatedAt = v1.UpdatedAt
	v2.Deleted = v1.Deleted
	v2.Namespace = v1.Namespace()

	// Copy identification fields
	v2.Ref = v1.Ref
	v2.Slug = v1.Slug
	v2.SKU = v1.SKU
	v2.UPC = v1.UPC

	// Copy content fields
	v2.Name = v1.Name
	v2.Headline = v1.Headline
	v2.Excerpt = v1.Excerpt
	v2.Description = v1.Description

	// Copy media
	v2.Header = v1.Header
	v2.Image = v1.Image
	v2.Media = v1.Media

	// Copy availability
	v2.Available = v1.Available
	v2.Hidden = v1.Hidden
	v2.Availability = v1.Availability
	v2.Preorder = v1.Preorder
	v2.AddLabel = v1.AddLabel

	// Copy cached values
	v2.ProductCachedValues = v1.ProductCachedValues

	// Convert variants
	v2.Variants = make([]*VariantV2, len(v1.Variants))
	for i, oldV := range v1.Variants {
		v2.Variants[i] = &VariantV2{
			ID:        oldV.Id_,
			ProductId: v2.ID,
			SKU:       oldV.SKU,
			UPC:       oldV.UPC,
			Name:      oldV.Name,
			Price:     oldV.Price,
			ListPrice: oldV.ListPrice,
			Available: oldV.Available,
			Sold:      oldV.Sold,
			Header:    oldV.Header,
			Image:     oldV.Image,
			Media:     oldV.Media,
		}

		// Convert variant options
		v2.Variants[i].Options = make([]VariantOption, len(oldV.Options))
		for j, opt := range oldV.Options {
			v2.Variants[i].Options[j] = VariantOption{
				Name:  opt.Name,
				Value: opt.Value,
			}
		}
	}

	// Convert options
	v2.Options = make([]*OptionV2, len(v1.Options))
	for i, opt := range v1.Options {
		v2.Options[i] = &OptionV2{
			Name:   opt.Name,
			Values: opt.Values,
		}
	}

	// Copy reservation
	v2.Reservation = ReservationV2{
		IsReservable:    v1.Reservation.IsReservable,
		IsBeingReserved: v1.Reservation.IsBeingReserved,
		ReservedBy:      v1.Reservation.ReservedBy,
		OrderId:         v1.Reservation.OrderId,
		ReservedAt:      v1.Reservation.ReservedAt,
	}

	// Copy metadata
	v2.Metadata = v1.Metadata

	v2.isNew = false
	return v2
}

// ProductStats holds aggregate statistics for products
type ProductStats struct {
	TotalProducts     int            `json:"totalProducts"`
	AvailableProducts int            `json:"availableProducts"`
	HiddenProducts    int            `json:"hiddenProducts"`
	DeletedProducts   int            `json:"deletedProducts"`
	TotalVariants     int            `json:"totalVariants"`
	TotalInventory    int            `json:"totalInventory"`
	TotalSold         int            `json:"totalSold"`
	PriceRange        PriceRange     `json:"priceRange"`
	CollectionCounts  map[string]int `json:"collectionCounts,omitempty"`
	TagCounts         map[string]int `json:"tagCounts,omitempty"`
}

// PriceRange represents a price range
type PriceRange struct {
	Min currency.Cents `json:"min"`
	Max currency.Cents `json:"max"`
}

// GetStatsV2 calculates aggregate statistics for products
func GetStatsV2(ctx context.Context, database db.DB, orgID string) (*ProductStats, error) {
	products, err := QueryV2(database).
		Filter("OrgID=", orgID).
		GetAll(ctx)
	if err != nil {
		return nil, err
	}

	stats := &ProductStats{
		CollectionCounts: make(map[string]int),
		TagCounts:        make(map[string]int),
		PriceRange:       PriceRange{Min: -1, Max: -1},
	}

	for _, p := range products {
		stats.TotalProducts++

		if p.Deleted {
			stats.DeletedProducts++
			continue
		}

		if p.Available {
			stats.AvailableProducts++
		}
		if p.Hidden {
			stats.HiddenProducts++
		}

		stats.TotalVariants += len(p.Variants)
		stats.TotalInventory += p.TotalInventory()
		stats.TotalSold += p.TotalSold()

		minPrice := p.MinPrice()
		maxPrice := p.MaxPrice()

		if stats.PriceRange.Min < 0 || minPrice < stats.PriceRange.Min {
			stats.PriceRange.Min = minPrice
		}
		if maxPrice > stats.PriceRange.Max {
			stats.PriceRange.Max = maxPrice
		}

		for _, c := range p.CollectionIDs {
			stats.CollectionCounts[c]++
		}
		for _, t := range p.Tags {
			stats.TagCounts[t]++
		}
	}

	// Fix min price if no products
	if stats.PriceRange.Min < 0 {
		stats.PriceRange.Min = 0
	}

	return stats, nil
}

// EnsureTableV2 creates the product table if it doesn't exist
// This is called automatically by SQLiteDB but can be used for explicit setup
func EnsureTableV2(ctx context.Context, database db.DB) error {
	// The table is created automatically by the generic _entities table
	// This function is a placeholder for any product-specific indexes
	return nil
}

// ExportV2 exports all products as JSON for backup/migration
func ExportV2(ctx context.Context, database db.DB, orgID string) ([]byte, error) {
	products, err := QueryV2(database).
		Filter("OrgID=", orgID).
		GetAll(ctx)
	if err != nil {
		return nil, err
	}

	return json.Marshal(products)
}

// ImportV2 imports products from JSON backup
func ImportV2(ctx context.Context, database db.DB, data []byte) error {
	var products []*ProductV2
	if err := json.Unmarshal(data, &products); err != nil {
		return fmt.Errorf("product: failed to unmarshal import data: %w", err)
	}

	return BulkCreate(ctx, database, products)
}
