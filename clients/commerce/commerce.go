// Package commerce provides the main application framework for Hanzo Commerce.
//
// Commerce is a multi-tenant e-commerce platform that runs as a standalone
// binary with embedded SQLite for per-user/org data and optional analytics
// via ClickHouse.
//
// Architecture:
//
//	┌─────────────────────────────────────────────────────────────┐
//	│                     Commerce App                            │
//	├─────────────────────────────────────────────────────────────┤
//	│  HTTP Server (Gin)  │  Hooks System  │  Background Tasks    │
//	├─────────────────────────────────────────────────────────────┤
//	│  User SQLite        │  Org SQLite    │  Analytics (CH)      │
//	│  + sqlite-vec       │  + sqlite-vec  │  (parallel queries)  │
//	└─────────────────────────────────────────────────────────────┘
package commerce

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"

	"github.com/hanzoai/cloud/clients/commerce/admin"
	billingPkg "github.com/hanzoai/cloud/clients/commerce/api/billing"
	catalogapi "github.com/hanzoai/cloud/clients/commerce/api/catalog"
	"github.com/hanzoai/cloud/clients/commerce/auth"
	billingUI "github.com/hanzoai/cloud/clients/commerce/billing"
	"github.com/hanzoai/cloud/clients/commerce/checkout"
	commerceDatastore "github.com/hanzoai/cloud/clients/commerce/datastore"
	commerceQuery "github.com/hanzoai/cloud/clients/commerce/datastore/query"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/events"
	"github.com/hanzoai/cloud/clients/commerce/billing/husdledger"
	"github.com/hanzoai/cloud/clients/commerce/hooks"
	"github.com/hanzoai/cloud/clients/commerce/infra"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/middleware/iammiddleware"
	"github.com/hanzoai/cloud/clients/commerce/models/catalogentry"
	orgModel "github.com/hanzoai/cloud/clients/commerce/models/organization"
	planModel "github.com/hanzoai/cloud/clients/commerce/models/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/sbomrecord"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/providers/stripe"
	"github.com/hanzoai/cloud/clients/commerce/seed"
	commercestore "github.com/hanzoai/cloud/clients/commerce/store"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/kms"
	"github.com/hanzoai/cloud/clients/commerce/treasury"
	"github.com/hanzoai/cloud/clients/commerce/types"
	"github.com/hanzoai/cloud/clients/commerce/util/husd"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// Version, GitCommit, and BuildTime are set via -ldflags at build time.
// Version's default is the source-of-record release; CI overrides it with
// the immutable image tag (-X github.com/hanzoai/cloud/clients/commerce.Version=<tag>) so
// the running binary's /healthz version always equals its deployed tag.
var (
	Version   = "1.46.39"
	GitCommit = "dev"
	BuildTime = "unknown"
)

// Config holds application configuration
type Config struct {
	// DataDir is the base directory for all data
	DataDir string

	// Dev enables development mode
	Dev bool

	// RequireIdentity makes the identity boundary (auth.Gin) reject any
	// request that arrives without X-Org-Id/X-User-Id. Sourced from
	// COMMERCED_REQUIRE_IDENTITY. It is OFF by default and MUST stay off
	// wherever the cloud-api -> commerce per-org billing path runs: that
	// path authenticates with a Bearer service token + X-Org-Id and
	// carries NO X-Org-Id, so a require-identity gate would 401 the money
	// path. The anti-spoofing boundary is EdgeAuth (always mounted), not
	// this gate.
	RequireIdentity bool

	// Secret for encryption and sessions
	Secret string

	// HTTP server address
	HTTPAddr string

	// HTTPS server address (optional)
	HTTPSAddr string

	// TLS certificate paths
	TLSCert string
	TLSKey  string

	// CORS allowed origins
	AllowedOrigins []string

	// Database configuration
	Database db.Config

	// Analytics collector endpoint (optional)
	AnalyticsEndpoint string

	// Analytics DSN (optional, for direct ClickHouse queries)
	DatastoreDSN string

	// Infrastructure configuration
	Infra infra.Config

	// Query timeout
	QueryTimeout time.Duration

	// KMS configuration for secret management
	KMS kms.Config

	// IAM configuration for hanzo.id JWT validation
	IAM struct {
		Enabled           bool     `json:"enabled"`
		Issuer            string   `json:"issuer"`
		ClientID          string   `json:"clientId"`
		ClientSecret      string   `json:"clientSecret"`
		AcceptedAudiences []string `json:"acceptedAudiences"`
		AcceptedIssuers   []string `json:"acceptedIssuers"`
		JwksURI           string   `json:"jwksUri"`
	} `json:"iam"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	cfg := &Config{
		DataDir:           getEnv("COMMERCE_DIR", "./commerce_data"),
		Dev:               getEnv("COMMERCE_DEV", "false") == "true",
		RequireIdentity:   getEnv("COMMERCED_REQUIRE_IDENTITY", "false") == "true",
		Secret:            getEnv("COMMERCE_SECRET", "change-me-in-production"),
		HTTPAddr:          getEnv("COMMERCE_HTTP", "127.0.0.1:8090"),
		AllowedOrigins:    []string{"*"},
		AnalyticsEndpoint: getEnv("ANALYTICS_ENDPOINT", ""),
		DatastoreDSN:      getEnv("DATASTORE_URL", ""),
		Infra:             *infraConfigFromEnv(),
		QueryTimeout:      30 * time.Second,
	}

	cfg.KMS.Enabled = getEnv("KMS_ENABLED", "false") == "true"
	cfg.KMS.URL = getEnv("KMS_URL", "")
	cfg.KMS.ClientID = getEnv("KMS_CLIENT_ID", "")
	cfg.KMS.ClientSecret = getEnv("KMS_CLIENT_SECRET", "")
	cfg.KMS.ProjectID = getEnv("KMS_PROJECT_ID", "")
	cfg.KMS.Environment = getEnv("KMS_ENVIRONMENT", "prod")

	cfg.IAM.Enabled = getEnv("IAM_ENABLED", "true") == "true"
	cfg.IAM.Issuer = getEnv("IAM_ISSUER", "https://hanzo.id")
	cfg.IAM.ClientID = getEnv("IAM_CLIENT_ID", "")
	cfg.IAM.ClientSecret = getEnv("IAM_CLIENT_SECRET", "")
	if accepted := getEnv("IAM_ACCEPTED_AUDIENCES", ""); accepted != "" {
		cfg.IAM.AcceptedAudiences = strings.Split(accepted, ",")
	}
	if issuers := getEnv("IAM_ACCEPTED_ISSUERS", ""); issuers != "" {
		cfg.IAM.AcceptedIssuers = strings.Split(issuers, ",")
	}
	cfg.IAM.JwksURI = getEnv("IAM_JWKS_URI", "")

	return cfg
}

// infraConfigFromEnv loads infrastructure config from environment.
//
// Env vars (generic, no implementation leakage):
//
//	KV_PREFIX     = optional key namespace (cache is hanzo/base, embedded)
//	S3_URL        = s3://key:secret@host:9000/bucket
//	S3_ENDPOINT   = host:9000  (with S3_ACCESS_KEY, S3_SECRET_KEY, S3_BUCKET)
//	DATASTORE_URL = clickhouse://host:9000/db
//	SQL_URL       = postgresql://user:pass@host:5432/db
//	VECTOR_URL    = qdrant://host:6333
//	SEARCH_URL    = http://host:7700
//	PUBSUB_URL    = nats://host:4222
//	TASKS_URL     = temporal://host:7233/namespace
func infraConfigFromEnv() *infra.Config {
	cfg := infra.DefaultConfig()

	// KV (hanzo/base, per-org/user SQLite). The cache is always available —
	// base is an embedded store, not an external service. DataDir/DataDSN are
	// resolved against the commerce store at bootstrap (commerce.go wires the
	// shared store), so env wiring here only sets an optional key namespace.
	cfg.KV.Enabled = true
	cfg.KV.KeyPrefix = getEnv("KV_PREFIX", "")

	// Vector (Qdrant)
	if vectorURL := getEnv("VECTOR_URL", ""); vectorURL != "" {
		if parsed, err := url.Parse(vectorURL); err == nil {
			cfg.Vector.Enabled = true
			host := parsed.Hostname()
			cfg.Vector.Host = host
			if p := parsed.Port(); p != "" {
				if port, err := strconv.Atoi(p); err == nil {
					cfg.Vector.Port = port
				}
			}
			if parsed.User != nil {
				cfg.Vector.APIKey = parsed.User.Username()
			}
		}
	}

	// Storage (S3-compatible)
	if s3URL := getEnv("S3_URL", ""); s3URL != "" {
		if parsed, err := url.Parse(s3URL); err == nil {
			cfg.Storage.Enabled = true
			cfg.Storage.Endpoint = parsed.Host
			if parsed.User != nil {
				cfg.Storage.AccessKey = parsed.User.Username()
				if pwd, ok := parsed.User.Password(); ok {
					cfg.Storage.SecretKey = pwd
				}
			}
			if bucket := strings.TrimPrefix(parsed.Path, "/"); bucket != "" {
				cfg.Storage.Bucket = bucket
			}
			cfg.Storage.UseSSL = parsed.Scheme == "s3s" || parsed.Query().Get("ssl") == "true"
		}
	} else if endpoint := getEnv("S3_ENDPOINT", ""); endpoint != "" {
		cfg.Storage.Enabled = true
		cfg.Storage.Endpoint = endpoint
		cfg.Storage.AccessKey = getEnv("S3_ACCESS_KEY", "")
		cfg.Storage.SecretKey = getEnv("S3_SECRET_KEY", "")
		cfg.Storage.Bucket = getEnv("S3_BUCKET", "commerce")
		cfg.Storage.UseSSL = getEnv("S3_USE_SSL", "false") == "true"
	}

	// Search (Meilisearch)
	if searchURL := getEnv("SEARCH_URL", ""); searchURL != "" {
		cfg.Search.Enabled = true
		cfg.Search.Host = searchURL
		if parsed, err := url.Parse(searchURL); err == nil {
			if parsed.User != nil {
				cfg.Search.APIKey = parsed.User.Username()
			}
		}
	}

	// PubSub (NATS)
	if pubsubURL := getEnv("PUBSUB_URL", ""); pubsubURL != "" {
		cfg.PubSub.Enabled = true
		cfg.PubSub.URL = pubsubURL
		if parsed, err := url.Parse(pubsubURL); err == nil {
			if parsed.User != nil {
				cfg.PubSub.Token = parsed.User.Username()
			}
		}
		cfg.PubSub.EnableJetStream = getEnv("PUBSUB_JETSTREAM", "true") == "true"
	}

	// Tasks (Temporal)
	if tasksURL := getEnv("TASKS_URL", ""); tasksURL != "" {
		if parsed, err := url.Parse(tasksURL); err == nil {
			cfg.Tasks.Enabled = true
			cfg.Tasks.HostPort = parsed.Host
			if ns := strings.TrimPrefix(parsed.Path, "/"); ns != "" {
				cfg.Tasks.Namespace = ns
			}
		}
	}

	return cfg
}

// App is the main Commerce application
type App struct {
	config *Config

	// Root command
	RootCmd *cobra.Command

	// Database manager
	DB *db.Manager

	// Infrastructure manager
	Infra *infra.Manager

	// Hook system
	Hooks *hooks.Registry

	// Events client (sends to analytics-collector via HTTP)
	Events *events.Client

	// Publisher sends commerce events to NATS/JetStream
	Publisher *events.Publisher

	// KMS client for secret management
	KMS *kms.CachedClient

	// ZAP node for inter-service vector operations
	ZAP *infra.ZAPNode

	// HTTP router
	Router *gin.Engine

	// CheckoutResolver maps hostnames (pay.example.com, …) to Tenant
	// configs for the embedded checkout SPA. Mutable at runtime so the
	// admin can add/remove hostnames and toggle providers without a
	// restart. Legacy resolver — new code reads CommerceStore.Tenants.
	CheckoutResolver *checkout.StaticResolver

	// CommerceStore is the hanzo/base-backed persistence seam. When set it
	// provides the authoritative tenants + hostname-claims collections;
	// handlers that have migrated off the legacy resolver use this directly.
	// Initialized in Bootstrap from COMMERCE_DATA_DIR / COMMERCE_BASE_URL.
	CommerceStore *commercestore.Store

	// HTTP server
	server *http.Server

	// Shutdown handling
	shutdownOnce sync.Once
	shutdownCh   chan struct{}

	// State
	bootstrapped bool
	mu           sync.RWMutex
}

// New creates a new Commerce application with default configuration
func New() *App {
	return NewWithConfig(DefaultConfig())
}

// NewWithConfig creates a new Commerce application with the given configuration
func NewWithConfig(config *Config) *App {
	app := &App{
		config:     config,
		Hooks:      hooks.NewRegistry(),
		shutdownCh: make(chan struct{}),
	}

	// Set Gin mode
	if config.Dev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// Initialize CLI
	app.initCLI()

	return app
}

// initCLI sets up the cobra command structure
func (app *App) initCLI() {
	app.RootCmd = &cobra.Command{
		Use:     "commerce",
		Short:   "Hanzo Commerce - Multi-tenant e-commerce platform",
		Version: Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip bootstrap for help/version
			if cmd.Name() == "help" || cmd.Name() == "version" {
				return nil
			}
			return app.Bootstrap()
		},
	}

	// Global flags
	app.RootCmd.PersistentFlags().StringVar(&app.config.DataDir, "dir", app.config.DataDir, "Data directory")
	app.RootCmd.PersistentFlags().BoolVar(&app.config.Dev, "dev", app.config.Dev, "Enable development mode")
	app.RootCmd.PersistentFlags().StringVar(&app.config.Secret, "secret", app.config.Secret, "Encryption secret")

	// Add commands
	app.RootCmd.AddCommand(app.newServeCmd())
	app.RootCmd.AddCommand(app.newMigrateCmd())
	app.RootCmd.AddCommand(app.newAdminCmd())
	app.RootCmd.AddCommand(app.newSeedCmd())
}

// newServeCmd creates the serve command
func (app *App) newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve [address]",
		Short: "Start the Commerce server",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				app.config.HTTPAddr = args[0]
			}
			return app.Serve()
		},
	}

	cmd.Flags().StringSliceVar(&app.config.AllowedOrigins, "origins", app.config.AllowedOrigins, "CORS allowed origins")
	cmd.Flags().StringVar(&app.config.HTTPSAddr, "https", "", "HTTPS address")
	cmd.Flags().StringVar(&app.config.TLSCert, "cert", "", "TLS certificate path")
	cmd.Flags().StringVar(&app.config.TLSKey, "key", "", "TLS key path")

	return cmd
}

// newMigrateCmd creates the migrate command
func (app *App) newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Run database migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Running migrations...")
			// TODO: Implement migration system
			return nil
		},
	}
}

// newAdminCmd creates the admin command
func (app *App) newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Admin user management",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "create [email]",
		Short: "Create an admin user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := args[0]
			fmt.Printf("Creating admin user: %s\n", email)
			// TODO: Implement admin creation
			return nil
		},
	})

	return cmd
}

// newSeedCmd creates the seed command for bootstrapping organizations and plans
func (app *App) newSeedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seed [org-name]",
		Short: "Seed organization and plans for a service",
		Long:  "Bootstrap an organization with API tokens and subscription plans.\nDefault org: bootnode",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			orgName := "bootnode"
			if len(args) > 0 {
				orgName = args[0]
			}
			return app.seedOrganization(orgName)
		},
	}
	return cmd
}

func (app *App) seedOrganization(orgName string) error {
	ctx := context.Background()
	ds := commerceDatastore.New(ctx)

	// Create or get organization
	org := orgModel.New(ds)
	org.Name = orgName
	org.GetOrCreate("Name=", org.Name)
	org.FullName = orgName + " Platform"
	org.Enabled = true
	org.AddDefaultTokens()

	org.MustPut()

	// Write payment credentials to KMS (if enabled)
	if app.KMS != nil {
		client := app.KMS.Client()
		squarePath := "/tenants/" + orgName + "/square"

		seedSecrets := []struct{ path, name, envVar string }{
			// Square — Production
			{squarePath, "SQUARE_PRODUCTION_APPLICATION_ID", "SQUARE_APPLICATION_ID"},
			{squarePath, "SQUARE_PRODUCTION_ACCESS_TOKEN", "SQUARE_ACCESS_TOKEN"},
			{squarePath, "SQUARE_PRODUCTION_LOCATION_ID", "SQUARE_LOCATION_ID"},
			// Square — Sandbox
			{squarePath, "SQUARE_SANDBOX_APPLICATION_ID", "SQUARE_SANDBOX_APPLICATION_ID"},
			{squarePath, "SQUARE_SANDBOX_ACCESS_TOKEN", "SQUARE_SANDBOX_ACCESS_TOKEN"},
			{squarePath, "SQUARE_SANDBOX_LOCATION_ID", "SQUARE_SANDBOX_LOCATION_ID"},
			// Square — Webhook
			{squarePath, "SQUARE_WEBHOOK_SIGNATURE_KEY", "SQUARE_WEBHOOK_SIGNATURE_KEY"},
		}

		for _, s := range seedSecrets {
			if v := os.Getenv(s.envVar); v != "" {
				if err := client.SetSecret(s.path, s.name, v); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to write %s to KMS: %v\n", s.name, err)
				} else {
					fmt.Printf("KMS: wrote %s to %s\n", s.name, s.path)
				}
			}
		}
	}

	// Get the test-secret-key token for API access
	tok, err := org.GetTokenByName("test-secret-key")
	if err != nil {
		return fmt.Errorf("failed to get API token: %w", err)
	}

	fmt.Printf("Organization: %s (ID: %s)\n", org.Name, org.Id())
	fmt.Printf("API Key (test): %s\n", tok.String)

	liveTok, err := org.GetTokenByName("live-secret-key")
	if err == nil {
		fmt.Printf("API Key (live): %s\n", liveTok.String)
	}

	// Create plans in the org's namespace
	nsDs := commerceDatastore.New(ctx)
	nsDs.SetNamespace(org.Namespace())

	plans := []struct {
		slug        string
		name        string
		price       int64
		interval    string
		description string
	}{
		{"bootnode-free", "Bootnode Free", 0, "month", "Free tier: 30M CU/mo, 25 req/s"},
		{"bootnode-payg", "Bootnode Pay-As-You-Go", 0, "month", "PAYG: $0.40/M CU, 300 req/s"},
		{"bootnode-growth", "Bootnode Growth", 4900, "month", "Growth: 100M CU included, $0.35/M overage, 500 req/s"},
		{"bootnode-enterprise", "Bootnode Enterprise", 0, "month", "Enterprise: custom pricing, 1000+ req/s"},
	}

	for _, p := range plans {
		pln := planModel.New(nsDs)
		pln.Slug = p.slug
		pln.GetOrCreate("Slug=", pln.Slug)
		pln.Name = p.name
		pln.Price = currency.Cents(p.price)
		pln.Currency = currency.USD
		pln.Interval = types.Monthly
		pln.IntervalCount = 1
		pln.Description = p.description
		pln.MustPut()
		fmt.Printf("Plan: %s (%s) - $%.2f/mo\n", pln.Name, pln.Slug, float64(pln.Price)/100)
	}

	fmt.Println("\nSeed complete.")
	return nil
}

// Start runs the application
func (app *App) Start() error {
	return app.RootCmd.Execute()
}

// Bootstrap initializes the application
func (app *App) Bootstrap() error {
	app.mu.Lock()
	defer app.mu.Unlock()

	if app.bootstrapped {
		return nil
	}

	// Trigger OnBootstrap hooks
	if err := app.Hooks.TriggerBootstrap(app); err != nil {
		return fmt.Errorf("bootstrap hook error: %w", err)
	}

	// Ensure data directory exists
	if err := os.MkdirAll(app.config.DataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Initialize database manager. Start from db.DefaultConfig() so the per-org
	// SQLite stores inherit the concurrency-safe SQLite settings (WAL journal +
	// 10s busy_timeout + bounded conn pools). The previous literal omitted
	// .SQLite entirely, leaving BusyTimeout=0 / JournalMode="" — which made any
	// concurrent write (deposit credit, usage debit, periodic DB tasks) fail
	// immediately with "database is locked" on the SQLite fallback path.
	dbConfig := db.DefaultConfig()
	dbConfig.DataDir = app.config.DataDir
	dbConfig.DatastoreDSN = app.config.DatastoreDSN
	dbConfig.EnableDatastore = app.config.DatastoreDSN != ""
	dbConfig.VectorDimensions = 1536
	dbConfig.IsDev = app.config.Dev

	var err error
	app.DB, err = db.NewManager(dbConfig)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	// Wire the primary data store. Prefer PostgreSQL (SQL_URL) in production;
	// fall back to per-org SQLite for local/dev environments.
	var systemDB db.DB
	if sqlURL := getEnv("SQL_URL", ""); sqlURL != "" {
		pdb, pgErr := db.NewPostgresDB(&db.PostgresDBConfig{
			DSN:                sqlURL,
			MaxOpenConns:       4,
			MaxIdleConns:       2,
			ConnMaxLifetime:    30 * time.Minute,
			ConnMaxIdleTime:    5 * time.Minute,
			QueryTimeout:       30 * time.Second,
			TenantID:           "system",
			TenantType:         "org",
			EnableVectorSearch: true,
			VectorDimensions:   1536,
		})
		if pgErr != nil {
			return fmt.Errorf("failed to connect to PostgreSQL (SQL_URL): %w", pgErr)
		}
		systemDB = pdb
		fmt.Println("Commerce: using PostgreSQL as primary data store")
	} else {
		// Dev/local fallback: per-org SQLite in DataDir.
		fmt.Fprintln(os.Stderr, "Commerce: SQL_URL not set, falling back to SQLite (not for production)")
		var dbErr error
		systemDB, dbErr = app.DB.Org("system")
		if dbErr != nil {
			return fmt.Errorf("failed to initialize system database: %w", dbErr)
		}
	}
	commerceDatastore.SetDefaultDB(systemDB)
	commerceQuery.SetDefaultDB(systemDB)

	// Route the generic REST merchant datastore (product/order/store/customer/
	// collection/discount/variant/…) to per-org SQLite via db.Manager.Org(<caller
	// org>). systemDB above remains the store for global kinds (organization/user/
	// token, which are DefaultNamespace) and for the billing/checkout subsystems
	// that call datastore.New directly. Merchant models are thus physically
	// isolated per org — no request can read/list/mutate another org's merchant
	// data on the /v1 REST surface, regardless of whether systemDB is Postgres or
	// SQLite. Decomplects the single-tenant Postgres out of the merchant path.
	if app.DB == nil {
		return fmt.Errorf("commerce: database manager not initialized — cannot install the per-org money resolver")
	}
	commerceDatastore.SetOrgDBResolver(app.DB.Org)

	// Fail closed: a production binary must NOT run a money path without the
	// per-org resolver installed. Without it every NewNamespaced money handler
	// (gift cards, orders, checkout, transactions, wire, b2b) would silently fall
	// back to the shared systemDB and cross tenants (Red CRIT-2). Assert here at
	// Bootstrap rather than degrade on the hot path.
	if !commerceDatastore.HasOrgDBResolver() {
		return fmt.Errorf("commerce: per-org DB resolver not installed after SetOrgDBResolver — refusing to start (money paths would use the shared store)")
	}

	// Chain-backed credit ledger (HUSD on the Hanzo EVM). Build the mint+index
	// service from the KMS-injected HUSD config + the org-derivation master seed
	// (all in-memory only). The SEED is the ledger's sole intent signal: with a
	// seed AND token+key the ledger is ENABLED; without a seed the service is
	// DISABLED and the billing money-in handlers keep their existing DB-mint
	// behavior — so a deploy with HUSD token+key but no seed (e.g. the shared
	// config the OSS contributor payout path uses) is a no-op for the ledger, and
	// the migration (Step 6) flips it on deliberately by provisioning the seed.
	// Wired AFTER the DB resolver so its per-org + system stores resolve. Fail
	// closed ONLY on an incoherent ledger config (seed set but token/key missing)
	// — see husdledger.ValidateConfig.
	husdCfg := husd.Config{}
	husdCfg.LoadFromEnv()
	var husdSeed []byte
	if raw := os.Getenv("HUSD_ORG_DERIVATION_SEED"); raw != "" {
		s, seedErr := treasury.SeedFromHex(raw)
		if seedErr != nil {
			return fmt.Errorf("commerce: invalid HUSD_ORG_DERIVATION_SEED: %w", seedErr)
		}
		husdSeed = s
	}
	if err := husdledger.ValidateConfig(husdCfg, husdSeed); err != nil {
		return fmt.Errorf("commerce: %w", err)
	}
	husdSvc := husdledger.New(husdCfg, husdSeed)
	husdledger.SetDefault(husdSvc)
	if husdSvc.Enabled() {
		fmt.Fprintf(os.Stderr, "Commerce: chain-backed credit ledger ENABLED (HUSD chainId=%d token=%s)\n", husdCfg.ChainID, husdCfg.TokenAddress)
	} else {
		fmt.Fprintln(os.Stderr, "Commerce: chain-backed credit ledger disabled (HUSD not configured — using DB credit path)")
	}

	// Hanzo/base-backed commerce store. Hosts the authoritative tenant
	// record + commerce_tenant_hostnames claim table — the source of truth
	// for the /v1/commerce/tenant public JSON and /_/commerce/tenants
	// superadmin CRUD — AND the commerce_kv cache collection that replaced
	// the former Redis/Valkey KV. Built before the infra manager so its KV
	// store can be attached to the manager (no second base app on the same
	// SQLite files). Bootstrap is idempotent; a failure here is fatal because
	// the public endpoint would otherwise 404 every tenant request.
	storeCfg := commercestore.FromEnv()
	if storeCfg.DataDir == "" || storeCfg.DataDir == "./commerce_data" {
		// Align with the app-level DataDir so all commerce persistence lives
		// under one tree.
		storeCfg.DataDir = filepath.Join(app.config.DataDir, "base")
	}
	cStore, storeErr := commercestore.New(storeCfg)
	if storeErr != nil {
		return fmt.Errorf("failed to initialize commerce store: %w", storeErr)
	}
	app.CommerceStore = cStore

	// Initialize infrastructure manager. Attach the base-backed KV client
	// (sharing the commerce store) before Connect so Connect reuses it.
	app.Infra = infra.New(&app.config.Infra)
	if app.config.Infra.KV.Enabled {
		// KV_URL selects an external Hanzo KV instance (the on-demand add-on);
		// unset ⇒ the Base-backed commerce_kv (default — no external datastore).
		// A malformed KV_URL fails closed rather than silently splitting cache +
		// lock state across two backends.
		var kvc *infra.KVClient
		if kvURL := getEnv("KV_URL", ""); kvURL != "" {
			c, kvErr := infra.NewKVClientFromURL(&app.config.Infra.KV, kvURL)
			if kvErr != nil {
				return fmt.Errorf("commerce: invalid KV_URL: %w", kvErr)
			}
			kvc = c
			fmt.Fprintln(os.Stderr, "Commerce: using external Hanzo KV (KV_URL) for cache + locks")
		} else {
			kvc = infra.NewKVClientFromStore(&app.config.Infra.KV, cStore)
		}
		app.Infra.SetKV(kvc)
	}
	ctx, cancel := context.WithTimeout(context.Background(), app.config.Infra.ConnectTimeout)
	defer cancel()

	if err := app.Infra.Connect(ctx); err != nil {
		// Log but don't fail - infrastructure services are optional
		fmt.Fprintf(os.Stderr, "Warning: some infrastructure services unavailable: %v\n", err)
	}

	// Initialize ZAP node for inter-service vector operations
	if vector, err := app.Infra.Vector(); err == nil {
		zapPort := 9090
		if p := getEnv("COMMERCE_ZAP_PORT", ""); p != "" {
			if parsed, convErr := strconv.Atoi(p); convErr == nil {
				zapPort = parsed
			}
		}
		zapCfg := &infra.ZAPConfig{
			Enabled: true,
			NodeID:  getEnv("COMMERCE_ZAP_NODE_ID", "commerce-0"),
			Port:    zapPort,
		}
		zapNode, zapErr := infra.NewZAPNode(zapCfg, vector, slog.Default())
		if zapErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: ZAP node failed to start: %v\n", zapErr)
		} else {
			app.ZAP = zapNode
			// Wire the ZAP-native SBOM ingest path (0x20). Stores via the SAME
			// sbomrecord.Ingest used by the HTTP handler, in the global "system"
			// namespace where SBOMs + OSS accruals live.
			zapNode.RegisterSBOMIngest(func(ctx context.Context, p infra.SBOMIngestPayload) error {
				db := commerceDatastore.New(ctx)
				db.SetNamespace("system")
				comps := make([]sbomrecord.Component, 0, len(p.Components))
				for _, comp := range p.Components {
					scope := comp.Scope
					if scope != "direct" {
						scope = "transitive"
					}
					comps = append(comps, sbomrecord.Component{
						PURL: comp.PURL, Name: comp.Name, Ecosystem: comp.Ecosystem,
						Version: comp.Version, Scope: scope,
					})
				}
				in := sbomrecord.New(db)
				in.ImageRef = p.ImageRef
				in.ImageDigest = p.ImageDigest
				in.Service = p.Service
				in.Format = p.Format
				in.Tool = p.Tool
				in.Components = comps
				_, err := sbomrecord.Ingest(db, in)
				return err
			})
			fmt.Printf("ZAP node started on :%d (vector ops: 0x10=upsert, 0x11=search, 0x12=delete; sbom: 0x20=ingest)\n", zapPort)
		}
	}

	// Initialize KMS client for secret management
	if app.config.KMS.Enabled && app.config.KMS.URL != "" {
		kmsClient := kms.NewClient(&app.config.KMS)
		app.KMS = kms.NewCachedClient(kmsClient)
		fmt.Println("KMS client initialized")
	}

	// Initialize analytics client (sends events to analytics-collector via HTTP)
	if app.config.AnalyticsEndpoint != "" {
		app.Events = events.NewClient(app.config.AnalyticsEndpoint)
	}

	// Initialize NATS/JetStream publisher for commerce events
	if pubsub, err := app.Infra.PubSub(); err == nil {
		if err := events.Bootstrap(ctx, pubsub); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to bootstrap commerce stream: %v\n", err)
		}
		app.Publisher = events.NewPublisher(pubsub)
		fmt.Println("Commerce event publisher initialized (NATS/JetStream)")
	}

	// Initialize router
	app.Router = gin.New()
	app.Router.Use(gin.Recovery())
	if app.config.Dev {
		app.Router.Use(gin.Logger())
	}

	// Wire KV client into IAM middleware for org-lookup caching (if KV is enabled).
	if kv, err := app.Infra.KV(); err == nil {
		iammiddleware.InitKV(kv)
	}

	// Initialize IAM middleware for hanzo.id JWT validation
	if app.config.IAM.Enabled && app.config.IAM.Issuer != "" && app.config.IAM.ClientID != "" {
		iamCfg := &auth.IAMConfig{
			Issuer:            app.config.IAM.Issuer,
			ClientID:          app.config.IAM.ClientID,
			ClientSecret:      app.config.IAM.ClientSecret,
			AcceptedAudiences: app.config.IAM.AcceptedAudiences,
			AcceptedIssuers:   app.config.IAM.AcceptedIssuers,
			JwksURI:           app.config.IAM.JwksURI,
		}
		if err := iammiddleware.Init(iamCfg); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: IAM middleware initialization failed: %v\n", err)
			app.config.IAM.Enabled = false
		}
	} else if app.config.IAM.ClientID == "" {
		app.config.IAM.Enabled = false
	}

	// Stripe catalog seed — ensure @hanzo/plans entries exist as Stripe
	// Products + Prices. Idempotent, cheap, and gated on STRIPE_SECRET_KEY.
	// Set COMMERCE_STRIPE_SEED=false to disable (useful for test environments).
	if os.Getenv("STRIPE_SECRET_KEY") != "" && getEnv("COMMERCE_STRIPE_SEED", "true") != "false" {
		app.runStripeSeed()
	}

	// Platform product catalog seed — populate the CMS source-of-truth for
	// Hanzo's own products (the list docs/console/pricing derive from) on first
	// boot. SeedIfEmpty is a cheap count-gated no-op once populated, so CMS
	// edits are authoritative thereafter. Set COMMERCE_CATALOG_SEED=false to skip.
	if getEnv("COMMERCE_CATALOG_SEED", "true") != "false" {
		app.runCatalogSeed()
	}

	// Anti-spoofing boundary — MUST be installed before any route group so it
	// wraps EVERY route. gin applies engine.Use() middleware only to routes
	// registered AFTER the Use() call, so this runs ahead of setupRoutes (and
	// ahead of the /v1 api.Route() bundle the cmd/* binaries register
	// post-Bootstrap). EdgeAuth strips client-supplied identity and re-mints it
	// from a verified IAM JWT. See server.go installEdgeAuth.
	installEdgeAuth(app)

	// Setup routes (registers /healthz + the /v1/commerce, /_/commerce groups).
	app.setupRoutes()

	// Require-identity gate (auth.Gin) — mounted AFTER setupRoutes so /healthz
	// (k8s probes) and the setupRoutes groups keep their existing exemption,
	// while the admin SPA and the post-Bootstrap /v1 bundle inherit it exactly
	// as before. See server.go installRequireGate.
	installRequireGate(app, app.config.RequireIdentity)

	// Admin SPA — registered after the gate so it inherits both EdgeAuth and
	// auth.Gin (preserving the historical order vs the /_/commerce JSON routes).
	mountAdminSPA(app)

	app.bootstrapped = true
	return nil
}

// runStripeSeed syncs every static plan in api/billing to Stripe. Errors are
// runCatalogSeed populates the platform product catalog (the "system" namespace
// CMS store) on first boot from the embedded seed. Cheap count-gated no-op once
// populated. Failures are logged, never fatal — the catalog projection simply
// returns empty until seeded.
func (app *App) runCatalogSeed() {
	db := commerceDatastore.New(nscontext.WithNamespace(context.Background(), catalogapi.CatalogNamespace))
	created, err := catalogentry.SeedIfEmpty(db)
	if err != nil {
		slog.Error("catalog seed failed", "err", err)
		return
	}
	if created > 0 {
		slog.Info("catalog seeded", "products", created)
	}
}

// logged but do not abort bootstrap — the service remains usable without
// Stripe catalog parity in degraded environments.
func (app *App) runStripeSeed() {
	stripeProv := stripe.NewProvider(stripe.Config{
		SecretKey:      os.Getenv("STRIPE_SECRET_KEY"),
		PublishableKey: os.Getenv("STRIPE_PUBLISHABLE_KEY"),
		WebhookSecret:  os.Getenv("STRIPE_WEBHOOK_SECRET"),
	})

	catalog := billingPkg.StaticPlans()
	plans := make([]seed.Plan, 0, len(catalog))
	for _, p := range catalog {
		plans = append(plans, seed.Plan{
			Slug:        p.Slug,
			Name:        p.Name,
			Description: p.Description,
			Category:    p.Category,
			PriceMonth:  p.PriceMonth,
			PriceYear:   p.PriceYear,
			Currency:    p.Currency,
		})
	}

	started := time.Now()
	category := getEnv("COMMERCE_STRIPE_SEED_CATEGORY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := seed.SyncStripe(ctx, stripeProv, plans, category)
	seed.LogResult(os.Stdout, res, err, started)
}

// canonicalPathHandler used to rewrite /v1/commerce/* → /v1/* internally.
// Deprecated: routes now live at /v1/commerce/* directly. Kept as identity
// for any caller still wrapping with it.
func canonicalPathHandler(next http.Handler) http.Handler { return next }

// setupRoutes configures HTTP routes
func (app *App) setupRoutes() {
	// Health check
	healthHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"service": "commerce",
			"version": Version,
			"commit":  GitCommit,
			"built":   BuildTime,
		})
	}
	app.Router.GET("/healthz", healthHandler)

	// Embedded admin surface — two go:embed'd Next.js bundles sharing the
	// /admin/* mount:
	//   /admin/billing/*  → hanzoai/billing (IAM-gated: admin/billing_admin/
	//                       owner/superadmin; 404 otherwise — no existence
	//                       leak)
	//   /admin/*          → commerce admin SPA (auth enforced by the admin
	//                       app itself via IAM redirect)
	//
	// Gin cannot register overlapping wildcards, so we dispatch in a single
	// /admin/*filepath handler. Billing gets first shot; everything else
	// falls through to the commerce admin bundle. The IAM client reused is
	// the same singleton the /v1/commerce middleware chain uses — nil means
	// fail-closed (every billing request 404s until IAM is initialized).
	adminHandler := admin.Handler("/admin")
	billingUIMount := "/admin/billing"
	billingHandler := billingUI.UIHandler(billingUIMount, iammiddleware.Client())
	adminDispatch := func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, billingUIMount) {
			billingHandler(c)
			return
		}
		adminHandler.ServeHTTP(c.Writer, c.Request)
	}
	app.Router.GET("/admin", gin.WrapH(adminHandler))
	app.Router.GET("/admin/*filepath", adminDispatch)

	// API routes
	api := app.Router.Group("/v1/commerce")
	{
		// Core middleware required by Commerce API handlers
		api.Use(middleware.AddHost())
		api.Use(middleware.RequestContext())
		api.Use(middleware.DetectOverrides())
		api.Use(middleware.ErrorHandlerJSON())
		api.Use(middleware.AccessControl("*"))

		// IAM JWT validation middleware (falls through to legacy auth if not IAM token).
		// EdgeAuth (strip client identity + mint from verified JWT) is installed by
		// Bootstrap (server.go installIdentityBoundary) BEFORE this group is
		// registered, so it already covers this group.
		if app.config.IAM.Enabled {
			api.Use(iammiddleware.IAMTokenRequired())
		}

		// Default cache policy: private, no-store for all API routes.
		// Individual route groups or handlers may override with CachePublic().
		api.Use(middleware.CachePrivate())

		// Inject KMS, Events, Publisher, and KV into gin context for handlers.
		api.Use(func(c *gin.Context) {
			if app.KMS != nil {
				c.Set("kms", app.KMS)
			}
			if app.Events != nil {
				c.Set("events", app.Events)
			}
			if app.Publisher != nil {
				c.Set("publisher", app.Publisher)
			}
			if kv, err := app.Infra.KV(); err == nil {
				c.Set("kv", kv)
			}
			c.Next()
		})

		// Trigger OnRouteSetup hooks to let extensions add routes
		app.Hooks.TriggerRouteSetup(api)
	}

	// Hosted multi-tenant checkout. Mounts:
	//   GET  /v1/commerce/tenant   — public tenant JSON (branding + enabled methods)
	//   POST /v1/commerce/deposits — proxied to tenant Backend.URL (e.g. a broker-dealer backend)
	//   GET  /*                    — embedded Vite SPA with SPA fallback
	//
	// Must be registered LAST: the SPA handler is a gin NoRoute catchall,
	// and everything above this line owns its own route prefix.
	// Org-as-tenant resolution (ONE way): the IAM org IS the tenant. host→brand→
	// org slug (checkout.OrgResolver) is the single source of truth for
	// /v1/commerce/tenant, deposits, and webhooks — no separate commerce-tenant
	// registry to seed or drift. The public tenant JSON carries the org's public
	// Square config (resolved by the same authority as the charge path), so the
	// pay SPA's card iframe initializes with the exact application commerce will
	// charge — no build-time VITE_* env, no per-host seed row, no 404.
	//
	// The Square public config comes from the deployment's per-brand env
	// (SQUARE_APPLICATION_ID/LOCATION_ID, KMS-synced) via a synthetic org keyed by
	// the resolved slug — NOT a per-request DB read. A naive per-request org query
	// on this public, unauthenticated, SPA-boot endpoint blocks under an unbounded
	// context and can exhaust the DB pool (regression seen at 1.42.44). Any future
	// per-org-creds loader passed here MUST be cached + deadline-bounded — see
	// checkout.OrgLoader. nil keeps resolution pure host→brand→env (no I/O, no hang).
	orgResolver := checkout.NewOrgResolver(nil)

	// forwardedHostMiddleware lifts the original customer-facing host from
	// X-Forwarded-Host (set by a trusted upstream) since the ingress overwrites
	// req.Host. brandForHost is exact-suffix, so a spoofed host still only maps
	// to a real brand's org.
	public := app.Router.Group("/v1/commerce")
	public.Use(forwardedHostMiddleware())
	public.GET("/tenant", gin.WrapH(checkout.TenantJSON(orgResolver)))
	// Public platform product catalog projection (the CMS SOT other surfaces —
	// docs, console sidebar, pricing — consume). Public + brand-scoped (?brand).
	public.GET("/catalog", catalogapi.Public)
	public.POST("/deposits", gin.WrapH(checkout.Deposits(orgResolver, checkout.NewHTTPForwarder())))
	public.POST("/deposits/:id/confirm", gin.WrapH(checkout.DepositConfirm(orgResolver, checkout.NewHTTPForwarder())))
	public.GET("/deposits/:id/status", gin.WrapH(checkout.DepositStatus(orgResolver, checkout.NewHTTPForwarder())))
	public.POST("/webhooks/:provider", gin.WrapH(checkout.WebhookIntake(orgResolver)))

	// Superadmin tenant CRUD over the base-backed store stays available for
	// per-org overrides, but it no longer DRIVES resolution. Gated by IAM +
	// handler claim checks; under /_ so the ingress blocks it publicly.
	if app.CommerceStore != nil {
		adminGroup := app.Router.Group("/_/commerce")
		if app.config.IAM.Enabled {
			adminGroup.Use(iammiddleware.IAMTokenRequired())
		}
		checkout.MountTenantAdmin(adminGroup, app.CommerceStore)
	}

	// SPA fallback — MUST be last so every API group wins path resolution.
	checkout.MountSPA(app.Router)
}

// Serve starts the HTTP server
func (app *App) Serve() error {
	// Trigger OnServe hooks
	if err := app.Hooks.TriggerServe(app); err != nil {
		return fmt.Errorf("serve hook error: %w", err)
	}

	// Wrap with canonical path rewrite: /v1/commerce/* -> /v1/*
	handler := canonicalPathHandler(app.Router)

	app.server = &http.Server{
		Addr:              app.config.HTTPAddr,
		Handler:           handler,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	fmt.Printf("Commerce %s starting on %s\n", Version, app.config.HTTPAddr)
	if app.config.Dev {
		fmt.Println("Running in DEVELOPMENT mode")
	}

	// Start HTTPS if configured
	if app.config.HTTPSAddr != "" && app.config.TLSCert != "" && app.config.TLSKey != "" {
		go func() {
			httpsServer := &http.Server{
				Addr:              app.config.HTTPSAddr,
				Handler:           handler,
				ReadTimeout:       30 * time.Second,
				ReadHeaderTimeout: 10 * time.Second,
				WriteTimeout:      60 * time.Second,
				IdleTimeout:       120 * time.Second,
			}
			if err := httpsServer.ListenAndServeTLS(app.config.TLSCert, app.config.TLSKey); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "HTTPS error: %v\n", err)
			}
		}()
	}

	// Start HTTP server
	if err := app.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

// Shutdown gracefully shuts down the application
func (app *App) Shutdown() error {
	var err error
	app.shutdownOnce.Do(func() {
		close(app.shutdownCh)

		// Trigger OnTerminate hooks
		if hookErr := app.Hooks.TriggerTerminate(app); hookErr != nil {
			err = hookErr
		}

		// Shutdown HTTP server
		if app.server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if shutdownErr := app.server.Shutdown(ctx); shutdownErr != nil {
				err = shutdownErr
			}
		}

		// Stop ZAP node
		if app.ZAP != nil {
			app.ZAP.Stop()
		}

		// Close events emitter (flush remaining events)
		if app.Events != nil {
			if eventsErr := app.Events.Close(); eventsErr != nil {
				err = eventsErr
			}
		}

		// Close infrastructure
		if app.Infra != nil {
			if infraErr := app.Infra.Close(); infraErr != nil {
				err = infraErr
			}
		}

		// Close database
		if app.DB != nil {
			if dbErr := app.DB.Close(); dbErr != nil {
				err = dbErr
			}
		}
	})

	return err
}

// Config returns the current configuration
func (app *App) Config() *Config {
	return app.config
}

// DataPath returns the full path within the data directory
func (app *App) DataPath(subpath string) string {
	return filepath.Join(app.config.DataDir, subpath)
}

// IsDev returns true if running in development mode
func (app *App) IsDev() bool {
	return app.config.Dev
}

// getEnv returns environment variable or default
func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// forwardedHostMiddleware lifts the X-Forwarded-Host header into
// req.Host so downstream tenant resolution sees the original customer
// hostname (e.g. world.hanzo.ai) instead of the ingress hostname
// (e.g. commerce-api.hanzo.ai). Tenant resolution is exact-match, so a
// spoofed header still must point at an existing tenant row to do
// anything — there is no probe oracle. Empty header is a no-op.
func forwardedHostMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if xfh := c.GetHeader("X-Forwarded-Host"); xfh != "" {
			c.Request.Host = xfh
		}
		c.Next()
	}
}
