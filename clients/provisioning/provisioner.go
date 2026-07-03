package provisioning

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	pgx "github.com/jackc/pgx/v5"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	redis "github.com/redis/go-redis/v9"

	"github.com/hanzoai/base"
	"github.com/hanzoai/base/core"
)

// errAlreadyExists is returned by a Provisioner when the backend reports the
// physical resource already exists. The handler maps it to HTTP 409.
var errAlreadyExists = errors.New("provisioning: backend resource already exists")

// Provisioner creates and drops one kind of logical resource inside a shared,
// already-live backend. Create receives the namespaced physical name plus a
// per-resource user + password (the handler generates these); it returns a
// client connection string, the public host/port of the backend service, and
// the logical database/collection/bucket name. Backends without per-resource
// auth (Qdrant, Meilisearch, S3) ignore user/password and return an empty
// username via the handler's kind map.
type Provisioner interface {
	Create(ctx context.Context, physicalName, user, password string) (connString, host string, port int, db string, err error)
	Drop(ctx context.Context, physicalName, user string) error
}

// newRegistry builds one Provisioner per kind from environment configuration.
// Construction never dials a backend — connections open lazily per request so
// a single down backend cannot block startup.
func newRegistry() map[string]Provisioner {
	return map[string]Provisioner{
		"sql":       newPostgres(),
		"vector":    newQdrant(),
		"datastore": newDatastore(),
		"kv":        newRedis(),
		"search":    newMeili(),
		"s3":        newS3(),
		"docdb":     newDocdb(),
	}
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// ----- Postgres (databases) -------------------------------------------------
// env: CLOUD_SQL_ADMIN_DSN (default postgres://postgres@sql.hanzo.svc:5432/postgres?sslmode=disable)

type postgresProvisioner struct {
	dsn  string
	host string
	port int
}

func newPostgres() *postgresProvisioner {
	dsn := env("CLOUD_SQL_ADMIN_DSN", "postgres://postgres@sql.hanzo.svc:5432/postgres?sslmode=disable")
	host, port := hostPortFromURL(dsn, 5432)
	return &postgresProvisioner{dsn: dsn, host: host, port: port}
}

func (p *postgresProvisioner) Create(ctx context.Context, physical, user, pw string) (string, string, int, string, error) {
	conn, err := pgx.Connect(ctx, p.dsn)
	if err != nil {
		return "", "", 0, "", fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, pgIdent(user), sqlLit(pw))); err != nil {
		if isPGDuplicate(err) {
			return "", "", 0, "", errAlreadyExists
		}
		return "", "", 0, "", fmt.Errorf("create role: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, pgIdent(physical), pgIdent(user))); err != nil {
		// Roll back the role we just created so a retry is clean.
		_, _ = conn.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, pgIdent(user)))
		if isPGDuplicate(err) {
			return "", "", 0, "", errAlreadyExists
		}
		return "", "", 0, "", fmt.Errorf("create database: %w", err)
	}
	cs := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", user, pw, p.host, p.port, physical)
	return cs, p.host, p.port, physical, nil
}

func (p *postgresProvisioner) Drop(ctx context.Context, physical, user string) error {
	conn, err := pgx.Connect(ctx, p.dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, pgIdent(physical))); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, pgIdent(user))); err != nil {
		return fmt.Errorf("drop role: %w", err)
	}
	return nil
}

func isPGDuplicate(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "already exists") || strings.Contains(s, "duplicate")
}

// ----- datastore (ClickHouse HTTP wire protocol) -----------------------------
// env: CLOUD_DATASTORE_ADMIN_ADDR (default datastore.hanzo.svc:8123),
//      CLOUD_DATASTORE_ADMIN_USER (default), CLOUD_DATASTORE_ADMIN_PASSWORD

type datastoreProvisioner struct {
	addr string
	user string
	pass string
	host string
	port int
}

func newDatastore() *datastoreProvisioner {
	addr := env("CLOUD_DATASTORE_ADMIN_ADDR", "datastore.hanzo.svc:8123")
	host, port := splitAddr(addr, 8123)
	return &datastoreProvisioner{
		addr: addr,
		user: env("CLOUD_DATASTORE_ADMIN_USER", "default"),
		pass: os.Getenv("CLOUD_DATASTORE_ADMIN_PASSWORD"),
		host: host,
		port: port,
	}
}

func (p *datastoreProvisioner) open() (interface {
	Exec(ctx context.Context, query string, args ...any) error
	Close() error
}, error) {
	return clickhouse.Open(&clickhouse.Options{
		Addr:     []string{p.addr},
		Protocol: clickhouse.HTTP,
		Auth:     clickhouse.Auth{Username: p.user, Password: p.pass},
	})
}

func (p *datastoreProvisioner) Create(ctx context.Context, physical, user, pw string) (string, string, int, string, error) {
	conn, err := p.open()
	if err != nil {
		return "", "", 0, "", fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", chIdent(physical))); err != nil {
		if isCHDuplicate(err) {
			return "", "", 0, "", errAlreadyExists
		}
		return "", "", 0, "", fmt.Errorf("create database: %w", err)
	}
	if err := conn.Exec(ctx, fmt.Sprintf("CREATE USER %s IDENTIFIED BY '%s'", chIdent(user), sqlLit(pw))); err != nil {
		_ = conn.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", chIdent(physical)))
		return "", "", 0, "", fmt.Errorf("create user: %w", err)
	}
	if err := conn.Exec(ctx, fmt.Sprintf("GRANT ALL ON %s.* TO %s", chIdent(physical), chIdent(user))); err != nil {
		_ = conn.Exec(ctx, fmt.Sprintf("DROP USER IF EXISTS %s", chIdent(user)))
		_ = conn.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", chIdent(physical)))
		return "", "", 0, "", fmt.Errorf("grant: %w", err)
	}
	cs := fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s?protocol=http", user, pw, p.host, p.port, physical)
	return cs, p.host, p.port, physical, nil
}

func (p *datastoreProvisioner) Drop(ctx context.Context, physical, user string) error {
	conn, err := p.open()
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", chIdent(physical))); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	if err := conn.Exec(ctx, fmt.Sprintf("DROP USER IF EXISTS %s", chIdent(user))); err != nil {
		return fmt.Errorf("drop user: %w", err)
	}
	return nil
}

func isCHDuplicate(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "already exists") || strings.Contains(s, "code: 82")
}

// ----- Redis / Valkey (kv) --------------------------------------------------
// env: CLOUD_KV_ADMIN_ADDR (default kv.hanzo.svc:6379),
//      CLOUD_KV_ADMIN_USER (default), CLOUD_KV_ADMIN_PASSWORD
//
// The logical resource is an ACL user constrained to the key prefix
// "<physical>:*" with full command access inside that keyspace.

type redisProvisioner struct {
	addr string
	user string
	pass string
	host string
	port int
}

func newRedis() *redisProvisioner {
	addr := env("CLOUD_KV_ADMIN_ADDR", "kv.hanzo.svc:6379")
	host, port := splitAddr(addr, 6379)
	return &redisProvisioner{
		addr: addr,
		user: env("CLOUD_KV_ADMIN_USER", "default"),
		pass: os.Getenv("CLOUD_KV_ADMIN_PASSWORD"),
		host: host,
		port: port,
	}
}

func (p *redisProvisioner) client() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: p.addr, Username: p.user, Password: p.pass})
}

func (p *redisProvisioner) Create(ctx context.Context, physical, user, pw string) (string, string, int, string, error) {
	rdb := p.client()
	defer func() { _ = rdb.Close() }()

	prefix := physical + ":"
	// ACL SETUSER is idempotent (overwrites); the control-plane UNIQUE index is
	// the real duplicate guard. Restrict to the resource keyspace + all commands.
	if err := rdb.Do(ctx, "ACL", "SETUSER", user, "reset", "on", ">"+pw, "~"+prefix+"*", "+@all").Err(); err != nil {
		return "", "", 0, "", fmt.Errorf("acl setuser: %w", err)
	}
	cs := fmt.Sprintf("redis://%s:%s@%s:%d/0", user, pw, p.host, p.port)
	return cs, p.host, p.port, prefix, nil
}

func (p *redisProvisioner) Drop(ctx context.Context, physical, user string) error {
	rdb := p.client()
	defer func() { _ = rdb.Close() }()
	if err := rdb.Do(ctx, "ACL", "DELUSER", user).Err(); err != nil {
		return fmt.Errorf("acl deluser: %w", err)
	}
	return nil
}

// ----- docdb (Hanzo Base — document collections on SQLite + realtime) -------
// env: CLOUD_DOCDB_PUBLIC_URL (default https://api.hanzo.ai)
//
// The "document database" product is a Hanzo Base document Collection: a
// schemaless JSON document store (each record carries a `data` json field)
// with native realtime subscriptions (SSE at /v1/base/realtime), on Base's
// per-tenant encrypted SQLite (HIP-0302, HIP-0105). It is IAM-native and
// carries ZERO MongoDB / Mongo-wire / FerretDB — Base is the only backend.
//
// Base is a co-resident in-process cloud subsystem (registered at order 60,
// mounted at /v1/base). We create the Collection DIRECTLY on the mounted app
// via base.App(): the REST /v1/base/collections surface is superuser-gated and
// Base local password auth is retired (IAM-only), so an out-of-band HTTP create
// is not available to this server-side control plane. Each provisioned resource
// is ONE Collection named by the caller's physical name (the same
// "o"<orgHash>_<name> tenant-namespacing every other kind uses), so isolation
// is by collection name + the control-plane UNIQUE(physical_name) guard —
// identical to how vector→Qdrant-collection and search→Meili-index isolate. The
// customer reaches it at
// {CLOUD_DOCDB_PUBLIC_URL}/v1/base/collections/<physical>/records (+ /realtime).

type docdbProvisioner struct {
	publicURL string
	host      string
	port      int
}

func newDocdb() *docdbProvisioner {
	pub := strings.TrimRight(env("CLOUD_DOCDB_PUBLIC_URL", "https://api.hanzo.ai"), "/")
	host, port := hostPortFromURL(pub, 443)
	return &docdbProvisioner{publicURL: pub, host: host, port: port}
}

// app returns the co-resident Base app or an error if the base subsystem is
// not mounted (fail-closed — never a silent no-op provision).
func (p *docdbProvisioner) app() (core.App, error) {
	app := base.App()
	if app == nil {
		return nil, errors.New("base subsystem not mounted")
	}
	return app, nil
}

// docdbAuthRule gates document CRUD + realtime to authenticated principals.
// Cross-tenant isolation is the unguessable 64-bit-orgHash collection name plus
// the control-plane UNIQUE(physical_name) guard — the same posture the other
// shared-backend kinds (vector, search) rely on.
const docdbAuthRule = `@request.auth.id != ""`

func (p *docdbProvisioner) Create(_ context.Context, physical, _, _ string) (string, string, int, string, error) {
	app, err := p.app()
	if err != nil {
		return "", "", 0, "", err
	}
	if existing, _ := app.FindCollectionByNameOrId(physical); existing != nil {
		return "", "", 0, "", errAlreadyExists
	}

	col := core.NewBaseCollection(physical)
	col.Fields.Add(&core.JSONField{Name: "data", MaxSize: core.DefaultJSONFieldMaxSize})
	rule := docdbAuthRule
	col.ListRule = &rule
	col.ViewRule = &rule
	col.CreateRule = &rule
	col.UpdateRule = &rule
	col.DeleteRule = &rule

	if err := app.Save(col); err != nil {
		return "", "", 0, "", fmt.Errorf("create collection: %w", err)
	}
	cs := p.publicURL + "/v1/base/collections/" + physical + "/records"
	return cs, p.host, p.port, physical, nil
}

func (p *docdbProvisioner) Drop(_ context.Context, physical, _ string) error {
	app, err := p.app()
	if err != nil {
		return err
	}
	col, err := app.FindCollectionByNameOrId(physical)
	if err != nil || col == nil {
		return nil // already gone — idempotent
	}
	if err := app.Delete(col); err != nil {
		return fmt.Errorf("drop collection: %w", err)
	}
	return nil
}

// ----- Qdrant (vector) ------------------------------------------------------
// env: CLOUD_VECTOR_ADMIN_URL (default http://vector.hanzo.svc:6333),
//      CLOUD_VECTOR_ADMIN_KEY, CLOUD_VECTOR_DEFAULT_DIM (1536), CLOUD_VECTOR_DISTANCE (Cosine)
//
// Qdrant has no per-collection credential; auth is the cluster api-key. The
// collection is created with a default unnamed vector config (size+distance).

type qdrantProvisioner struct {
	base     string
	key      string
	host     string
	port     int
	dim      int
	distance string
}

func newQdrant() *qdrantProvisioner {
	base := strings.TrimRight(env("CLOUD_VECTOR_ADMIN_URL", "http://vector.hanzo.svc:6333"), "/")
	host, port := hostPortFromURL(base, 6333)
	return &qdrantProvisioner{
		base:     base,
		key:      os.Getenv("CLOUD_VECTOR_ADMIN_KEY"),
		host:     host,
		port:     port,
		dim:      atoiEnv("CLOUD_VECTOR_DEFAULT_DIM", 1536),
		distance: env("CLOUD_VECTOR_DISTANCE", "Cosine"),
	}
}

func (p *qdrantProvisioner) headers() map[string]string {
	return map[string]string{"api-key": p.key}
}

func (p *qdrantProvisioner) Create(ctx context.Context, physical, _, _ string) (string, string, int, string, error) {
	body := map[string]any{"vectors": map[string]any{"size": p.dim, "distance": p.distance}}
	status, rb, err := httpRequest(ctx, http.MethodPut, p.base+"/collections/"+physical, p.headers(), body)
	if err != nil {
		return "", "", 0, "", fmt.Errorf("connect: %w", err)
	}
	if status == http.StatusConflict {
		return "", "", 0, "", errAlreadyExists
	}
	if status < 200 || status >= 300 {
		return "", "", 0, "", fmt.Errorf("qdrant status %d: %s", status, truncate(rb))
	}
	cs := p.base + "/collections/" + physical
	return cs, p.host, p.port, physical, nil
}

func (p *qdrantProvisioner) Drop(ctx context.Context, physical, _ string) error {
	status, rb, err := httpRequest(ctx, http.MethodDelete, p.base+"/collections/"+physical, p.headers(), nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("qdrant status %d: %s", status, truncate(rb))
	}
	return nil
}

// ----- Meilisearch (search) -------------------------------------------------
// env: CLOUD_SEARCH_ADMIN_URL (default http://search.hanzo.svc:7700), CLOUD_SEARCH_ADMIN_KEY
//
// Meilisearch authenticates with API keys (Bearer), not per-index passwords;
// the logical resource is the index.

type meiliProvisioner struct {
	base string
	key  string
	host string
	port int
}

func newMeili() *meiliProvisioner {
	base := strings.TrimRight(env("CLOUD_SEARCH_ADMIN_URL", "http://search.hanzo.svc:7700"), "/")
	host, port := hostPortFromURL(base, 7700)
	return &meiliProvisioner{base: base, key: os.Getenv("CLOUD_SEARCH_ADMIN_KEY"), host: host, port: port}
}

func (p *meiliProvisioner) headers() map[string]string {
	h := map[string]string{}
	if p.key != "" {
		h["Authorization"] = "Bearer " + p.key
	}
	return h
}

func (p *meiliProvisioner) Create(ctx context.Context, physical, _, _ string) (string, string, int, string, error) {
	body := map[string]any{"uid": physical, "primaryKey": "id"}
	status, rb, err := httpRequest(ctx, http.MethodPost, p.base+"/indexes", p.headers(), body)
	if err != nil {
		return "", "", 0, "", fmt.Errorf("connect: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", "", 0, "", fmt.Errorf("meilisearch status %d: %s", status, truncate(rb))
	}
	cs := p.base + "/indexes/" + physical
	return cs, p.host, p.port, physical, nil
}

func (p *meiliProvisioner) Drop(ctx context.Context, physical, _ string) error {
	status, rb, err := httpRequest(ctx, http.MethodDelete, p.base+"/indexes/"+physical, p.headers(), nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("meilisearch status %d: %s", status, truncate(rb))
	}
	return nil
}

// ----- S3 / MinIO (s3) ------------------------------------------------------
// env: S3_ADMIN_ENDPOINT (default s3.hanzo.svc:9000),
//      S3_ADMIN_ACCESS_KEY, S3_ADMIN_SECRET_KEY,
//      S3_SECURE (false), S3_REGION (us-east-1)
//
// S3 access uses the shared admin credentials scoped by bucket policy out of
// band; there is no per-bucket password. The logical resource is the bucket.

type s3Provisioner struct {
	endpoint string
	ak       string
	sk       string
	secure   bool
	region   string
	host     string
	port     int
}

func newS3() *s3Provisioner {
	endpoint := env("S3_ADMIN_ENDPOINT", "s3.hanzo.svc:9000")
	host, port := splitAddr(endpoint, 9000)
	return &s3Provisioner{
		endpoint: endpoint,
		ak:       os.Getenv("S3_ADMIN_ACCESS_KEY"),
		sk:       os.Getenv("S3_ADMIN_SECRET_KEY"),
		secure:   boolEnv("S3_SECURE", false),
		region:   env("S3_REGION", "us-east-1"),
		host:     host,
		port:     port,
	}
}

func (p *s3Provisioner) client() (*minio.Client, error) {
	return minio.New(p.endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(p.ak, p.sk, ""),
		Secure: p.secure,
		Region: p.region,
	})
}

func (p *s3Provisioner) Create(ctx context.Context, physical, _, _ string) (string, string, int, string, error) {
	bucket := bucketName(physical)
	cli, err := p.client()
	if err != nil {
		return "", "", 0, "", fmt.Errorf("connect: %w", err)
	}
	if err := cli.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: p.region}); err != nil {
		if exists, _ := cli.BucketExists(ctx, bucket); exists {
			return "", "", 0, "", errAlreadyExists
		}
		return "", "", 0, "", fmt.Errorf("make bucket: %w", err)
	}
	scheme := "http"
	if p.secure {
		scheme = "https"
	}
	cs := fmt.Sprintf("%s://%s/%s", scheme, p.endpoint, bucket)
	return cs, p.host, p.port, bucket, nil
}

func (p *s3Provisioner) Drop(ctx context.Context, physical, _ string) error {
	bucket := bucketName(physical)
	cli, err := p.client()
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if err := cli.RemoveBucket(ctx, bucket); err != nil {
		return fmt.Errorf("remove bucket: %w", err)
	}
	return nil
}

// bucketName converts a physical identifier ("o"<orgHash>_<ident>) into a
// DNS-safe S3 bucket name: lowercase [a-z0-9-], 3–63 chars, no leading or
// trailing hyphen. Folding '_'→'-' is a bijection on physical names (which
// contain no '-'), so the fixed-width org-hash prefix that makes physicalName
// injective makes the bucket injective too — distinct tenants get distinct
// buckets, and the single UNIQUE(physical_name) control-plane guard therefore
// also guarantees bucket uniqueness. Deterministic, so Drop recomputes it.
func bucketName(physical string) string {
	b := strings.Trim(strings.ToLower(strings.ReplaceAll(physical, "_", "-")), "-")
	if len(b) > 63 { // unreachable for nameRE-bounded input (physical ≤ 58); defensive.
		b = strings.Trim(b[:63], "-")
	}
	return b
}

// ----- shared helpers -------------------------------------------------------

func httpRequest(ctx context.Context, method, rawURL string, headers map[string]string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, rb, nil
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// genToken returns n bytes of crypto-random data as URL-safe base64 (no
// padding). The alphabet [A-Za-z0-9_-] contains no quote characters, so the
// password is safe to interpolate into single-quoted SQL string literals.
func genToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pgIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func chIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
func sqlLit(s string) string  { return strings.ReplaceAll(s, "'", "''") }

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func atoiEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func boolEnv(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func hostPortFromURL(raw string, def int) (string, int) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return splitAddr(raw, def)
	}
	host := u.Hostname()
	port := def
	if ps := u.Port(); ps != "" {
		if n, err := strconv.Atoi(ps); err == nil {
			port = n
		}
	}
	if host == "" {
		host = raw
	}
	return host, port
}

func splitAddr(addr string, def int) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, def
	}
	port := def
	if n, err := strconv.Atoi(portStr); err == nil {
		port = n
	}
	return host, port
}
