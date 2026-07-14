// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// The identity boundary (SanitizeIdentity) validates a JWT and mints the identity
// headers every subsystem trusts. An opaque API key (hk-/sk-/pk-/fw_/hz_) is not a
// JWT, so it yielded no principal — and a subsystem that gates on the minted
// identity (zen's billing gate) refused an API-key request as anonymous, though the
// key is a first-class credential. keyResolver closes that gap: it turns a key into
// the SAME idClaims a JWT yields, so ONE minting path serves both credentials and
// key auth and session auth can never disagree on who a request is.

// keyResolver turns an opaque API key into the principal it authenticates, or nil
// for an unknown key or an unconfigured resolver (the request stays anonymous — a
// bad key never grants trust).
type keyResolver interface {
	resolve(ctx context.Context, key string) *idClaims
}

// iamKeys resolves an `hk-` key against IAM's get-user?accessKey endpoint,
// authenticating as the confidential `hanzo-console` client (the credential
// clients/account already uses). The resolved user is exactly what a JWT for that
// user carries, so SanitizeIdentity mints identical headers for a key and a session.
// A brief cache keeps the hot auth path off the network; it caches misses too, so a
// bad key cannot hammer IAM.
type iamKeys struct {
	base   string
	auth   string // client_secret_basic, or "" when unconfigured
	http   *http.Client
	cache  cache[string, *idClaims]
}

// newIAMKeys reads the same IAM env clients/account does. With no confidential
// credential it returns a resolver that resolves nothing (keys stay anonymous —
// never a fabricated principal), so a deployment lacking the credential is safe.
func newIAMKeys() *iamKeys {
	base := strings.TrimRight(env("IAM_URL", "IAM_INTERNAL_URL"), "/")
	id := strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_ID"))
	secret := strings.TrimSpace(os.Getenv("IAM_MINT_CLIENT_SECRET"))
	var auth string
	if id != "" && secret != "" {
		auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(id+":"+secret))
	}
	return &iamKeys{
		base:  base,
		auth:  auth,
		http:  &http.Client{Timeout: 5 * time.Second},
		cache: newCache[string, *idClaims](60 * time.Second),
	}
}

func env(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func (k *iamKeys) resolve(ctx context.Context, key string) *idClaims {
	if k.auth == "" || k.base == "" || key == "" {
		return nil
	}
	if c, ok := k.cache.get(key); ok {
		return c // may be a cached nil — a valid "anonymous" answer
	}
	c := k.lookup(ctx, key)
	k.cache.put(key, c)
	return c
}

// lookup performs the authenticated get-user?accessKey call and maps the user row
// to idClaims. Any failure (unreachable, denied, unknown key) yields nil. Name is
// both the username IAM's owner/name lookups parse and the id fallback: a key has
// no UUID subject, so userID() falls through to name — the gateway's historical
// X-User-Id==name behavior the owner/name path expects.
func (k *iamKeys) lookup(ctx context.Context, key string) *idClaims {
	u := k.base + "/v1/iam/get-user?" + url.Values{"accessKey": {key}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", k.auth)
	req.Header.Set("Accept", "application/json")
	resp, err := k.http.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	var env struct {
		Status string `json:"status"`
		Data   *struct {
			Owner   string `json:"owner"`
			Name    string `json:"name"`
			Email   string `json:"email"`
			IsAdmin bool   `json:"isAdmin"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Status != "ok" || env.Data == nil {
		return nil
	}
	if strings.TrimSpace(env.Data.Owner) == "" {
		return nil
	}
	return &idClaims{
		Owner:             strings.TrimSpace(env.Data.Owner),
		Name:              strings.TrimSpace(env.Data.Name),
		PreferredUsername: strings.TrimSpace(env.Data.Name),
		Email:             strings.TrimSpace(env.Data.Email),
		IsAdmin:           env.Data.IsAdmin,
	}
}

// cache is a tiny concurrency-safe TTL map — one generic type for every
// resolve-once-reuse-briefly lookup, so no bespoke cache is hand-rolled per caller.
type cache[K comparable, V any] struct {
	ttl time.Duration
	mu  sync.Mutex
	m   map[K]entry[V]
}

type entry[V any] struct {
	v   V
	exp time.Time
}

func newCache[K comparable, V any](ttl time.Duration) cache[K, V] {
	return cache[K, V]{ttl: ttl, m: make(map[K]entry[V])}
}

func (c *cache[K, V]) get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.exp) {
		var zero V
		return zero, false
	}
	return e.v, true
}

func (c *cache[K, V]) put(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[k] = entry[V]{v: v, exp: time.Now().Add(c.ttl)}
}
