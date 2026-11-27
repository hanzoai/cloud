package provisioning

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/text"
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
func newRegistry(deps cloud.Deps) map[string]Provisioner {
	return map[string]Provisioner{
		"vector": newQdrant(deps),
		"search": newMeili(deps),
		"s3":     newS3(),
	}
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// ----- Qdrant (vector) ------------------------------------------------------
// env: CLOUD_VECTOR_ADMIN_URL (default http://vector.hanzo.svc:6333),
//      CLOUD_VECTOR_ADMIN_KEY_REF (KMS ref, not the key), CLOUD_VECTOR_DEFAULT_DIM (1536), CLOUD_VECTOR_DISTANCE (Cosine)
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

func newQdrant(deps cloud.Deps) *qdrantProvisioner {
	base := strings.TrimRight(env.Or("CLOUD_VECTOR_ADMIN_URL", "http://vector.hanzo.svc:6333"), "/")
	host, port := hostPortFromURL(base, 6333)
	return &qdrantProvisioner{
		base:     base,
		key:      string(deps.SecretFromEnv(context.Background(), "CLOUD_VECTOR_ADMIN_KEY_REF")),
		host:     host,
		port:     port,
		dim:      atoiEnv("CLOUD_VECTOR_DEFAULT_DIM", 1536),
		distance: env.Or("CLOUD_VECTOR_DISTANCE", "Cosine"),
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

// getJSON reads one Qdrant path into out. It is the READ half of this client —
// the operator's inventory (inventory.go) uses it — so a collection is described
// by the same base and api-key that created it.
func (p *qdrantProvisioner) getJSON(ctx context.Context, path string, out any) error {
	status, rb, err := httpRequest(ctx, http.MethodGet, p.base+path, p.headers(), nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("qdrant status %d: %s", status, truncate(rb))
	}
	return json.Unmarshal(rb, out)
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

func newMeili(deps cloud.Deps) *meiliProvisioner {
	base := strings.TrimRight(env.Or("CLOUD_SEARCH_ADMIN_URL", "http://search.hanzo.svc:7700"), "/")
	host, port := hostPortFromURL(base, 7700)
	return &meiliProvisioner{base: base, key: string(deps.SecretFromEnv(context.Background(), "CLOUD_SEARCH_ADMIN_KEY_REF")), host: host, port: port}
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

// ----- S3 (s3) --------------------------------------------------------------
// The bucket lives in the process's object store (sdk/object); the connection
// string is the store's address, which only a store with a server of its own (the
// S3 driver, configured by S3_ADMIN_*) has to give out.
//
// S3 access uses the shared admin credentials scoped by bucket policy out of
// band; there is no per-bucket password. The logical resource is the bucket.

type s3Provisioner struct {
	store object.Store
}

func newS3() *s3Provisioner { return &s3Provisioner{store: object.Default()} }

// address is the store's own endpoint, or the reason it has none to give.
func (p *s3Provisioner) address() (object.Delegation, string, int, error) {
	if err := object.Check(); err != nil {
		return object.Delegation{}, "", 0, fmt.Errorf("connect: %w", err)
	}
	d, ok := object.Delegate()
	if !ok {
		return object.Delegation{}, "", 0, errors.New("this deployment's object store is local disk and has no endpoint to connect to")
	}
	u, err := url.Parse(d.URL)
	if err != nil {
		return object.Delegation{}, "", 0, fmt.Errorf("object store endpoint: %w", err)
	}
	host, port := splitAddr(u.Host, 9000)
	return d, host, port, nil
}

func (p *s3Provisioner) Create(ctx context.Context, physical, _, _ string) (string, string, int, string, error) {
	bucket := bucketName(physical)
	d, host, port, err := p.address()
	if err != nil {
		return "", "", 0, "", err
	}
	if exists, err := p.store.Has(ctx, bucket); err != nil {
		return "", "", 0, "", fmt.Errorf("make bucket: %w", err)
	} else if exists {
		return "", "", 0, "", errAlreadyExists
	}
	if err := p.store.Ensure(ctx, bucket); err != nil {
		return "", "", 0, "", fmt.Errorf("make bucket: %w", err)
	}
	return d.URL + "/" + bucket, host, port, bucket, nil
}

func (p *s3Provisioner) Drop(ctx context.Context, physical, _ string) error {
	if err := p.store.Drop(ctx, bucketName(physical)); err != nil {
		return fmt.Errorf("remove bucket: %w", err)
	}
	return nil
}

// bucketName is the S3 bucket for a physical identifier ("o"<orgHash>_<ident>): the
// one derivation, object.BucketOf, which clients/s3 and every other bucket user shares,
// so a bucket allocated here is the one they operate on. Deterministic, so Drop
// recomputes it.
func bucketName(physical string) string { return object.BucketOf(physical) }

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
		return text.Cut(s, 200)
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

func atoiEnv(key string, def int) int {
	if v := env.Or(key, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func boolEnv(key string, def bool) bool {
	if v := env.Or(key, ""); v != "" {
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
