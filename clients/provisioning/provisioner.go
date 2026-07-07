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

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
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

// newRegistry builds one Provisioner per SHARED-logical kind from environment
// configuration. Construction never dials a backend — connections open lazily
// per request so a single down backend cannot block startup.
// The registry holds ONLY the shared-logical kinds (vector, search, s3). The
// four on-demand data add-ons (kv, sql, docdb, datastore) are provisioned by the
// dedicated-instance strategy (dedicated.go, dedicatedEngines) — each org owns
// its instance — and are deliberately absent here: create() routes a dedicated
// kind before consulting reg.
func newRegistry() map[string]Provisioner {
	return map[string]Provisioner{
		"vector": newQdrant(),
		"search": newMeili(),
		"s3":     newS3(),
	}
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

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
// padding). The alphabet [A-Za-z0-9_-] carries no quote/space/URL-delimiter
// characters, so the generated password is safe to interpolate verbatim into a
// DSN's userinfo, a valkey `requirepass <pw>` config line, or a quoted
// identifier — no escaping and no injection surface.
func genToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

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
