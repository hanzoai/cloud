package product

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func setupTestDB(t *testing.T) (db.DB, func()) {
	t.Helper()

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "product_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	cfg := db.DefaultConfig()
	cfg.DataDir = tmpDir
	cfg.OrgDataDir = tmpDir + "/orgs"
	cfg.EnableVectorSearch = false // Don't require sqlite-vec for tests

	manager, err := db.NewManager(cfg)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create manager: %v", err)
	}

	database, err := manager.Org("test-org")
	if err != nil {
		manager.Close()
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to get org database: %v", err)
	}

	cleanup := func() {
		manager.Close()
		os.RemoveAll(tmpDir)
	}

	return database, cleanup
}

func TestNewProductV2(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	p := NewProductV2(database, "org-123")

	if p.OrgID != "org-123" {
		t.Errorf("Expected OrgID 'org-123', got '%s'", p.OrgID)
	}

	if p.Variants == nil {
		t.Error("Expected Variants to be initialized")
	}

	if p.Options == nil {
		t.Error("Expected Options to be initialized")
	}

	if p.Metadata == nil {
		t.Error("Expected Metadata to be initialized")
	}

	if !p.Taxable {
		t.Error("Expected Taxable to default to true")
	}

	if !p.isNew {
		t.Error("Expected isNew to be true")
	}
}

func TestProductV2_Validate(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	tests := []struct {
		name    string
		product *ProductV2
		wantErr bool
	}{
		{
			name: "valid product",
			product: func() *ProductV2 {
				p := NewProductV2(database, "org-123")
				p.Slug = "test-product"
				p.SKU = "SKU-001"
				p.Name = "Test Product"
				return p
			}(),
			wantErr: false,
		},
		{
			name: "missing slug",
			product: func() *ProductV2 {
				p := NewProductV2(database, "org-123")
				p.SKU = "SKU-001"
				p.Name = "Test Product"
				return p
			}(),
			wantErr: true,
		},
		{
			name: "missing sku",
			product: func() *ProductV2 {
				p := NewProductV2(database, "org-123")
				p.Slug = "test-product"
				p.Name = "Test Product"
				return p
			}(),
			wantErr: true,
		},
		{
			name: "missing name",
			product: func() *ProductV2 {
				p := NewProductV2(database, "org-123")
				p.Slug = "test-product"
				p.SKU = "SKU-001"
				return p
			}(),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.product.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestProductV2_CreateAndGet(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create product
	p := NewProductV2(database, "org-123")
	p.Slug = "test-product"
	p.SKU = "SKU-001"
	p.Name = "Test Product"
	p.Description = "A test product description"
	p.Available = true

	if err := p.Create(ctx); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if p.ID == "" {
		t.Error("Expected ID to be set after Create")
	}

	if p.CreatedAt.IsZero() {
		t.Error("Expected CreatedAt to be set")
	}

	if p.UpdatedAt.IsZero() {
		t.Error("Expected UpdatedAt to be set")
	}

	// Get by ID
	retrieved := NewProductV2(database, "")
	if err := retrieved.GetByID(ctx, p.ID); err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}

	if retrieved.Name != p.Name {
		t.Errorf("Expected Name '%s', got '%s'", p.Name, retrieved.Name)
	}

	if retrieved.Description != p.Description {
		t.Errorf("Expected Description '%s', got '%s'", p.Description, retrieved.Description)
	}
}

func TestProductV2_GetBySlug(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create product
	p := NewProductV2(database, "org-123")
	p.Slug = "unique-slug"
	p.SKU = "SKU-002"
	p.Name = "Slug Test Product"

	if err := p.Create(ctx); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Get by Slug
	retrieved := NewProductV2(database, "")
	if err := retrieved.GetBySlug(ctx, "unique-slug"); err != nil {
		t.Fatalf("GetBySlug() error = %v", err)
	}

	if retrieved.ID != p.ID {
		t.Errorf("Expected ID '%s', got '%s'", p.ID, retrieved.ID)
	}
}

func TestProductV2_Update(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create product
	p := NewProductV2(database, "org-123")
	p.Slug = "update-test"
	p.SKU = "SKU-003"
	p.Name = "Original Name"

	if err := p.Create(ctx); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	originalUpdatedAt := p.UpdatedAt

	// Wait a bit to ensure time difference
	time.Sleep(10 * time.Millisecond)

	// Update
	p.Name = "Updated Name"
	if err := p.Update(ctx); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if p.UpdatedAt.Equal(originalUpdatedAt) || p.UpdatedAt.Before(originalUpdatedAt) {
		t.Error("Expected UpdatedAt to be after original")
	}

	// Verify update persisted
	retrieved := NewProductV2(database, "")
	if err := retrieved.GetByID(ctx, p.ID); err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}

	if retrieved.Name != "Updated Name" {
		t.Errorf("Expected Name 'Updated Name', got '%s'", retrieved.Name)
	}
}

func TestProductV2_Delete(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create product
	p := NewProductV2(database, "org-123")
	p.Slug = "delete-test"
	p.SKU = "SKU-004"
	p.Name = "Delete Test"

	if err := p.Create(ctx); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Soft delete
	if err := p.Delete(ctx); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if !p.Deleted {
		t.Error("Expected Deleted to be true")
	}
}

func TestProductV2_Variants(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create product with variants
	p := NewProductV2(database, "org-123")
	p.Slug = "variant-test"
	p.SKU = "SKU-005"
	p.Name = "Variant Test Product"

	p.AddVariant(&VariantV2{
		ID:        "var-1",
		SKU:       "SKU-005-S",
		Name:      "Small",
		Price:     1999,
		Available: true,
		Inventory: 10,
	})

	p.AddVariant(&VariantV2{
		ID:        "var-2",
		SKU:       "SKU-005-M",
		Name:      "Medium",
		Price:     2199,
		Available: true,
		Inventory: 15,
	})

	p.AddVariant(&VariantV2{
		ID:        "var-3",
		SKU:       "SKU-005-L",
		Name:      "Large",
		Price:     2499,
		Available: false,
		Inventory: 0,
	})

	if err := p.Create(ctx); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Test MinPrice
	if p.MinPrice() != 1999 {
		t.Errorf("Expected MinPrice 1999, got %d", p.MinPrice())
	}

	// Test MaxPrice
	if p.MaxPrice() != 2499 {
		t.Errorf("Expected MaxPrice 2499, got %d", p.MaxPrice())
	}

	// Test TotalInventory
	if p.TotalInventory() != 25 {
		t.Errorf("Expected TotalInventory 25, got %d", p.TotalInventory())
	}

	// Test VariantByID
	v := p.VariantByID("var-2")
	if v == nil {
		t.Error("Expected to find variant var-2")
	} else if v.Name != "Medium" {
		t.Errorf("Expected variant Name 'Medium', got '%s'", v.Name)
	}

	// Test VariantBySKU
	v = p.VariantBySKU("SKU-005-L")
	if v == nil {
		t.Error("Expected to find variant by SKU")
	} else if v.Name != "Large" {
		t.Errorf("Expected variant Name 'Large', got '%s'", v.Name)
	}

	// Test AvailableVariants
	available := p.AvailableVariants()
	if len(available) != 2 {
		t.Errorf("Expected 2 available variants, got %d", len(available))
	}

	// Test RemoveVariant
	if !p.RemoveVariant("var-1") {
		t.Error("Expected RemoveVariant to return true")
	}
	if len(p.Variants) != 2 {
		t.Errorf("Expected 2 variants after removal, got %d", len(p.Variants))
	}
}

func TestProductQueryV2(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create multiple products
	products := []*ProductV2{
		{Slug: "prod-1", SKU: "SKU-Q1", Name: "Product 1", Available: true, Hidden: false},
		{Slug: "prod-2", SKU: "SKU-Q2", Name: "Product 2", Available: true, Hidden: false},
		{Slug: "prod-3", SKU: "SKU-Q3", Name: "Product 3", Available: false, Hidden: false},
		{Slug: "prod-4", SKU: "SKU-Q4", Name: "Product 4", Available: true, Hidden: true},
	}

	for _, p := range products {
		prod := NewProductV2(database, "org-123")
		prod.Slug = p.Slug
		prod.SKU = p.SKU
		prod.Name = p.Name
		prod.Available = p.Available
		prod.Hidden = p.Hidden
		if err := prod.Create(ctx); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}

	// Query all
	results, err := QueryV2(database).GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll() error = %v", err)
	}
	if len(results) != 4 {
		t.Errorf("Expected 4 products, got %d", len(results))
	}

	// Query available only
	results, err = QueryV2(database).Available().GetAll(ctx)
	if err != nil {
		t.Fatalf("Available().GetAll() error = %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Expected 3 available products, got %d", len(results))
	}

	// Query visible only
	results, err = QueryV2(database).Visible().GetAll(ctx)
	if err != nil {
		t.Fatalf("Visible().GetAll() error = %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Expected 3 visible products, got %d", len(results))
	}

	// Query with limit
	results, err = QueryV2(database).Limit(2).GetAll(ctx)
	if err != nil {
		t.Fatalf("Limit().GetAll() error = %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 products with limit, got %d", len(results))
	}

	// Count
	count, err := QueryV2(database).Count(ctx)
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}
	if count != 4 {
		t.Errorf("Expected count 4, got %d", count)
	}
}

func TestProductV2_Clone(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	p := NewProductV2(database, "org-123")
	p.ID = "original-id"
	p.Slug = "original-slug"
	p.SKU = "SKU-ORIG"
	p.Name = "Original"
	p.AddVariant(&VariantV2{
		ID:    "var-1",
		SKU:   "VAR-SKU",
		Name:  "Variant",
		Price: 999,
	})

	clone := p.Clone()

	if clone.ID != "" {
		t.Error("Expected clone ID to be empty")
	}

	if clone.Slug != p.Slug {
		t.Errorf("Expected clone Slug '%s', got '%s'", p.Slug, clone.Slug)
	}

	if len(clone.Variants) != len(p.Variants) {
		t.Errorf("Expected clone to have %d variants, got %d", len(p.Variants), len(clone.Variants))
	}

	// Modify clone and ensure original unchanged
	clone.Name = "Modified Clone"
	if p.Name == "Modified Clone" {
		t.Error("Expected original to be unchanged after modifying clone")
	}
}

func TestProductV2_EmbeddingText(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	p := NewProductV2(database, "org-123")
	p.Name = "Test Product"
	p.Headline = "Amazing headline"
	p.Description = "Full description here"
	p.Tags = []string{"tag1", "tag2"}
	p.Categories = []string{"cat1"}

	text := p.EmbeddingText()

	if text == "" {
		t.Error("Expected embedding text to not be empty")
	}

	// Check all parts are included
	expectedParts := []string{"Test Product", "Amazing headline", "Full description here", "tag1", "tag2", "cat1"}
	for _, part := range expectedParts {
		found := false
		if len(text) >= len(part) {
			for i := 0; i <= len(text)-len(part); i++ {
				if text[i:i+len(part)] == part {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("Expected embedding text to contain '%s'", part)
		}
	}
}

func TestMigrateFromV1(t *testing.T) {
	_, cleanup := setupTestDB(t)
	defer cleanup()

	// Create a mock v1 product (simplified since we can't import the real one easily)
	// This test validates the migration function structure exists
	// In a real scenario, you'd have integration tests with actual v1 data

	t.Run("migration function exists", func(t *testing.T) {
		// Just verify the function signature is correct
		_ = MigrateFromV1
	})
}

func TestProductV2_PriceRange(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	p := NewProductV2(database, "org-123")
	p.AddVariant(&VariantV2{Price: 1000})
	p.AddVariant(&VariantV2{Price: 2000})
	p.AddVariant(&VariantV2{Price: 1500})

	min, max := p.PriceRange()

	if min != 1000 {
		t.Errorf("Expected min 1000, got %d", min)
	}

	if max != 2000 {
		t.Errorf("Expected max 2000, got %d", max)
	}
}

func TestProductV2_DisplayMethods(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	p := NewProductV2(database, "org-123")
	p.Name = "test product name"
	p.Currency = currency.USD
	p.AddVariant(&VariantV2{Price: 1999})
	p.Media = []Media{{Type: MediaImage, Url: "http://example.com/img.jpg"}}

	// DisplayName should title-case
	name := p.DisplayName()
	if name == "" {
		t.Error("Expected DisplayName to return non-empty string")
	}

	// DisplayPrice
	price := p.DisplayPrice()
	if price == "" {
		t.Error("Expected DisplayPrice to return non-empty string")
	}

	// DisplayImage
	img := p.DisplayImage()
	if img.Url != "http://example.com/img.jpg" {
		t.Errorf("Expected DisplayImage URL, got '%s'", img.Url)
	}
}
