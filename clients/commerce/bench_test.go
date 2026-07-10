package commerce

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	ujson "github.com/hanzoai/cloud/clients/commerce/util/json"
)

// --------------------------------------------------------------------------
// Test fixture: a Plan-like struct that mirrors models/plan.Plan without
// pulling in the full model dependency graph (datastore, mixin, orm, etc.).
// Field names and json tags match the real Plan struct.
// --------------------------------------------------------------------------

type benchPlan struct {
	ID_             string            `json:"id,omitempty"`
	Slug            string            `json:"slug"`
	SKU             string            `json:"sku"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Price           int64             `json:"price"`
	Currency        string            `json:"currency"`
	Interval        string            `json:"interval"`
	IntervalCount   int               `json:"intervalCount"`
	TrialPeriodDays int               `json:"trialPeriodDays"`
	Metadata        map[string]any    `json:"metadata,omitempty"`
	CreatedAt       string            `json:"createdAt,omitempty"`
	UpdatedAt       string            `json:"updatedAt,omitempty"`
	Deleted         bool              `json:"deleted,omitempty"`
	Ref             benchEcommerceRef `json:"ref,omitempty"`
}

type benchEcommerceRef struct {
	Type   string          `json:"type,omitempty"`
	Stripe benchStripeRef  `json:"stripe,omitempty"`
	Affirm benchAffirmRef  `json:"affirm,omitempty"`
}

type benchStripeRef struct {
	ID string `json:"id,omitempty"`
}

type benchAffirmRef struct {
	ID string `json:"id,omitempty"`
}

// samplePlan returns a realistic Plan struct for benchmarking.
func samplePlan() benchPlan {
	return benchPlan{
		ID_:             "abc123def456",
		Slug:            "pro-monthly",
		SKU:             "plan_pro_monthly_v2",
		Name:            "Pro Plan",
		Description:     "Professional tier with 100GB storage, priority support, and advanced analytics dashboard access.",
		Price:           4900,
		Currency:        "usd",
		Interval:        "month",
		IntervalCount:   1,
		TrialPeriodDays: 14,
		Metadata: map[string]any{
			"tier":            "pro",
			"features":        []string{"analytics", "priority-support", "100gb-storage"},
			"stripe_price_id": "price_1234567890",
			"visible":         true,
		},
		CreatedAt: "2026-01-15T10:30:00Z",
		UpdatedAt: "2026-03-01T08:00:00Z",
		Deleted:   false,
		Ref: benchEcommerceRef{
			Type:   "stripe",
			Stripe: benchStripeRef{ID: "prod_ABC123"},
		},
	}
}

// ==========================================================================
// 1. SQLite Put + Get round-trip (JSONB serialize/deserialize)
// ==========================================================================

func BenchmarkSQLiteRoundTrip(b *testing.B) {
	dir := b.TempDir()
	sdb, err := db.NewSQLiteDB(&db.SQLiteDBConfig{
		Path: filepath.Join(dir, "bench.db"),
		Config: db.SQLiteConfig{
			MaxOpenConns: 4,
			MaxIdleConns: 2,
			BusyTimeout:  5000,
			JournalMode:  "WAL",
			Synchronous:  "NORMAL",
			CacheSize:    -8000,
		},
		TenantID:   "bench-tenant",
		TenantType: "org",
	})
	if err != nil {
		b.Fatalf("NewSQLiteDB: %v", err)
	}
	defer sdb.Close()

	ctx := context.Background()
	plan := samplePlan()

	// Seed one entity so Get has something to find.
	key := sdb.NewKey("plan", "bench-plan-0", 0, nil)
	if _, err := sdb.Put(ctx, key, &plan); err != nil {
		b.Fatalf("seed Put: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := sdb.NewKey("plan", fmt.Sprintf("bench-plan-%d", i+1), 0, nil)
		if _, err := sdb.Put(ctx, k, &plan); err != nil {
			b.Fatalf("Put: %v", err)
		}
		var dst benchPlan
		if err := sdb.Get(ctx, k, &dst); err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}

// ==========================================================================
// 2. PostgresDB Put + Get round-trip (requires SQL_URL)
// ==========================================================================

func BenchmarkPostgresRoundTrip(b *testing.B) {
	sqlURL := os.Getenv("SQL_URL")
	if sqlURL == "" {
		b.Skip("SQL_URL not set")
	}

	pdb, err := db.NewPostgresDB(&db.PostgresDBConfig{
		DSN:          sqlURL,
		MaxOpenConns: 10,
		MaxIdleConns: 5,
		TenantID:     "bench-tenant",
		TenantType:   "org",
	})
	if err != nil {
		b.Fatalf("NewPostgresDB: %v", err)
	}
	defer pdb.Close()

	ctx := context.Background()
	plan := samplePlan()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := pdb.NewKey("plan", fmt.Sprintf("pg-bench-%d", i), 0, nil)
		if _, err := pdb.Put(ctx, k, &plan); err != nil {
			b.Fatalf("Put: %v", err)
		}
		var dst benchPlan
		if err := pdb.Get(ctx, k, &dst); err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}

// ==========================================================================
// 3. SQLite Query().Filter().GetAll() overhead
// ==========================================================================

func BenchmarkSQLiteQueryFilterGetAll(b *testing.B) {
	dir := b.TempDir()
	sdb, err := db.NewSQLiteDB(&db.SQLiteDBConfig{
		Path: filepath.Join(dir, "query.db"),
		Config: db.SQLiteConfig{
			MaxOpenConns: 4,
			MaxIdleConns: 2,
			BusyTimeout:  5000,
			JournalMode:  "WAL",
			Synchronous:  "NORMAL",
			CacheSize:    -8000,
		},
		TenantID:   "bench-tenant",
		TenantType: "org",
	})
	if err != nil {
		b.Fatalf("NewSQLiteDB: %v", err)
	}
	defer sdb.Close()

	ctx := context.Background()

	// Seed 100 plans
	for i := 0; i < 100; i++ {
		p := samplePlan()
		p.Slug = fmt.Sprintf("plan-%d", i)
		p.Currency = "usd"
		k := sdb.NewKey("plan", fmt.Sprintf("qplan-%d", i), 0, nil)
		if _, err := sdb.Put(ctx, k, &p); err != nil {
			b.Fatalf("seed: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var results []benchPlan
		_, err := sdb.Query("plan").
			Filter("Currency=", "usd").
			Limit(50).
			GetAll(ctx, &results)
		if err != nil {
			b.Fatalf("GetAll: %v", err)
		}
	}
}

// ==========================================================================
// 4. Cache middleware overhead — CachePublic vs CachePrivate per-request cost
// ==========================================================================

func BenchmarkCachePublic(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)

	handler := middleware.CachePublic(300)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/plans", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w = httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req
		handler(c)
	}
}

func BenchmarkCachePrivate(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)

	handler := middleware.CachePrivate()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/billing/subscription", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w = httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req
		handler(c)
	}
}

func BenchmarkCachePublicMutation(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)

	handler := middleware.CachePublic(300)
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/subscribe", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req
		handler(c)
	}
}

// ==========================================================================
// 5. JSON serialization: encoding/json vs util/json (EncodeBytes/DecodeBytes)
// ==========================================================================

func BenchmarkStdlibJSONMarshal(b *testing.B) {
	plan := samplePlan()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(&plan)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStdlibJSONUnmarshal(b *testing.B) {
	plan := samplePlan()
	data, _ := json.Marshal(&plan)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var dst benchPlan
		if err := json.Unmarshal(data, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUtilJSONEncodeBytes(b *testing.B) {
	plan := samplePlan()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ujson.EncodeBytes(&plan)
	}
}

func BenchmarkUtilJSONDecodeBytes(b *testing.B) {
	plan := samplePlan()
	data := ujson.EncodeBytes(&plan)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var dst benchPlan
		if err := ujson.DecodeBytes(data, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStdlibJSONRoundTrip(b *testing.B) {
	plan := samplePlan()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, err := json.Marshal(&plan)
		if err != nil {
			b.Fatal(err)
		}
		var dst benchPlan
		if err := json.Unmarshal(data, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUtilJSONRoundTrip(b *testing.B) {
	plan := samplePlan()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data := ujson.EncodeBytes(&plan)
		var dst benchPlan
		if err := ujson.DecodeBytes(data, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

// ==========================================================================
// 6. Key encoding throughput — NewKey().Encode()
// ==========================================================================

func BenchmarkKeyEncodeStringID(b *testing.B) {
	dir := b.TempDir()
	sdb, err := db.NewSQLiteDB(&db.SQLiteDBConfig{
		Path: filepath.Join(dir, "key.db"),
		Config: db.SQLiteConfig{
			MaxOpenConns: 1,
			MaxIdleConns: 1,
			JournalMode:  "WAL",
		},
		TenantID: "t1",
	})
	if err != nil {
		b.Fatalf("NewSQLiteDB: %v", err)
	}
	defer sdb.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := sdb.NewKey("plan", fmt.Sprintf("plan-%d", i), 0, nil)
		_ = k.Encode()
	}
}

func BenchmarkKeyEncodeIntID(b *testing.B) {
	dir := b.TempDir()
	sdb, err := db.NewSQLiteDB(&db.SQLiteDBConfig{
		Path: filepath.Join(dir, "key.db"),
		Config: db.SQLiteConfig{
			MaxOpenConns: 1,
			MaxIdleConns: 1,
			JournalMode:  "WAL",
		},
		TenantID: "t1",
	})
	if err != nil {
		b.Fatalf("NewSQLiteDB: %v", err)
	}
	defer sdb.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := sdb.NewKey("plan", "", int64(i+1), nil)
		_ = k.Encode()
	}
}

func BenchmarkKeyEncodeWithParent(b *testing.B) {
	dir := b.TempDir()
	sdb, err := db.NewSQLiteDB(&db.SQLiteDBConfig{
		Path: filepath.Join(dir, "key.db"),
		Config: db.SQLiteConfig{
			MaxOpenConns: 1,
			MaxIdleConns: 1,
			JournalMode:  "WAL",
		},
		TenantID: "t1",
	})
	if err != nil {
		b.Fatalf("NewSQLiteDB: %v", err)
	}
	defer sdb.Close()

	parent := sdb.NewKey("org", "org-hanzo", 0, nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := sdb.NewKey("plan", fmt.Sprintf("plan-%d", i), 0, parent)
		_ = k.Encode()
	}
}

// ==========================================================================
// 7. DB serialize/deserialize (marshalForDB / unmarshalForDB via Put/Get)
// ==========================================================================

func BenchmarkSQLitePutOnly(b *testing.B) {
	dir := b.TempDir()
	sdb, err := db.NewSQLiteDB(&db.SQLiteDBConfig{
		Path: filepath.Join(dir, "put.db"),
		Config: db.SQLiteConfig{
			MaxOpenConns: 4,
			MaxIdleConns: 2,
			BusyTimeout:  5000,
			JournalMode:  "WAL",
			Synchronous:  "NORMAL",
			CacheSize:    -8000,
		},
		TenantID: "bench",
	})
	if err != nil {
		b.Fatalf("NewSQLiteDB: %v", err)
	}
	defer sdb.Close()

	ctx := context.Background()
	plan := samplePlan()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		k := sdb.NewKey("plan", fmt.Sprintf("p-%d", i), 0, nil)
		if _, err := sdb.Put(ctx, k, &plan); err != nil {
			b.Fatalf("Put: %v", err)
		}
	}
}

func BenchmarkSQLiteGetOnly(b *testing.B) {
	dir := b.TempDir()
	sdb, err := db.NewSQLiteDB(&db.SQLiteDBConfig{
		Path: filepath.Join(dir, "get.db"),
		Config: db.SQLiteConfig{
			MaxOpenConns: 4,
			MaxIdleConns: 2,
			BusyTimeout:  5000,
			JournalMode:  "WAL",
			Synchronous:  "NORMAL",
			CacheSize:    -8000,
		},
		TenantID: "bench",
	})
	if err != nil {
		b.Fatalf("NewSQLiteDB: %v", err)
	}
	defer sdb.Close()

	ctx := context.Background()
	plan := samplePlan()

	// Seed one entity
	key := sdb.NewKey("plan", "get-target", 0, nil)
	if _, err := sdb.Put(ctx, key, &plan); err != nil {
		b.Fatalf("seed: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var dst benchPlan
		if err := sdb.Get(ctx, key, &dst); err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}
