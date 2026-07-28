package integrations

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// teams_verify.go is Teams' INBOUND trust boundary: verification of the Bot
// Framework JWT that signs every inbound activity. This is NOT HMAC — it is an
// RS256 JWT signed by Microsoft, verified against Bot Framework's published JWKS.
//
// The signing algorithm is HARD-PINNED to RS256 and the key is ALWAYS an RSA public
// key fetched from Bot Framework's JWKS — so the classic JWT algorithm-confusion
// attacks (alg=none, or alg=HS256 with the RSA public key used as an HMAC secret)
// are structurally impossible: a token whose header alg != "RS256" is rejected
// before any key is consulted, and the only verify path is rsa.VerifyPKCS1v15.

const (
	// botFrameworkIssuer is the issuer Teams-channel Bot Framework tokens carry.
	botFrameworkIssuer = "https://api.botframework.com"
	// botFrameworkOpenID is Bot Framework's OpenID metadata document; its jwks_uri
	// points at the signing keys.
	botFrameworkOpenID = "https://login.botframework.com/v1/.well-known/openidconfiguration"
	// teamsClockSkew tolerates small clock differences on exp/nbf.
	teamsClockSkew = 5 * time.Minute
	// teamsKeysTTL bounds how long the JWKS cache is trusted before a refresh.
	teamsKeysTTL = time.Hour
)

var (
	teamsKeysMu sync.RWMutex
	teamsKeys   map[string]*rsa.PublicKey
	teamsKeysAt time.Time
)

// teamsClaims are the Bot Framework JWT claims we enforce.
type teamsClaims struct {
	Iss        string `json:"iss"`
	Aud        string `json:"aud"`
	Exp        int64  `json:"exp"`
	Nbf        int64  `json:"nbf"`
	ServiceURL string `json:"serviceurl"`
}

// verifyBotFrameworkJWT returns true iff the Authorization header carries a valid
// Bot Framework RS256 JWT: signature verified against the JWKS key for its kid,
// issuer == Bot Framework, audience == our MICROSOFT_APP_ID, within exp/nbf (±skew),
// and — critically — the token's serviceurl claim matches the activity's serviceUrl
// (so a valid token cannot be used to redirect our reply to an attacker host).
// `now`=0 → time.Now (injectable for tests).
func verifyBotFrameworkJWT(ctx context.Context, authHeader, appID, activityServiceURL string, now int64) bool {
	tok := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authHeader), "Bearer "))
	if tok == "" || appID == "" {
		return false
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	// Header: hard-pin RS256 and read the kid.
	hraw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hraw, &hdr); err != nil || hdr.Alg != "RS256" || hdr.Kid == "" {
		return false
	}
	key, err := teamsSigningKey(ctx, hdr.Kid)
	if err != nil || key == nil {
		return false
	}
	// Verify the RS256 signature over "header.payload".
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return false
	}
	// Claims.
	praw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var cl teamsClaims
	if err := json.Unmarshal(praw, &cl); err != nil {
		return false
	}
	if cl.Iss != botFrameworkIssuer || cl.Aud != appID {
		return false
	}
	if now == 0 {
		now = time.Now().Unix()
	}
	skew := int64(teamsClockSkew / time.Second)
	if cl.Exp != 0 && now > cl.Exp+skew {
		return false
	}
	if cl.Nbf != 0 && now < cl.Nbf-skew {
		return false
	}
	// serviceurl binding: if present it MUST match the activity's serviceUrl. A
	// token without the claim is rejected (we always have the activity's serviceUrl).
	if !strings.EqualFold(strings.TrimRight(cl.ServiceURL, "/"), strings.TrimRight(activityServiceURL, "/")) {
		return false
	}
	return true
}

// teamsSigningKey resolves the RSA public key for a kid from the cached JWKS,
// refreshing on a cache miss or when stale. A concurrent refresh is serialized by
// the write lock; the cheap read path holds only the read lock.
func teamsSigningKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	teamsKeysMu.RLock()
	if teamsKeys != nil && time.Since(teamsKeysAt) < teamsKeysTTL {
		if k := teamsKeys[kid]; k != nil {
			teamsKeysMu.RUnlock()
			return k, nil
		}
	}
	teamsKeysMu.RUnlock()

	teamsKeysMu.Lock()
	defer teamsKeysMu.Unlock()
	// Re-check under the write lock (another goroutine may have refreshed).
	if teamsKeys != nil && time.Since(teamsKeysAt) < teamsKeysTTL {
		if k := teamsKeys[kid]; k != nil {
			return k, nil
		}
	}
	keys, err := fetchTeamsKeys(ctx)
	if err != nil {
		return nil, err
	}
	teamsKeys = keys
	teamsKeysAt = time.Now()
	if k := teamsKeys[kid]; k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("teams: no JWKS key for kid %q", kid)
}

// fetchTeamsKeys loads Bot Framework's OpenID metadata → jwks_uri → the RSA signing
// keys, keyed by kid.
func fetchTeamsKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	jwksURI, err := teamsJWKSURI(ctx)
	if err != nil {
		return nil, err
	}
	body, err := teamsGetJSON(ctx, jwksURI)
	if err != nil {
		return nil, err
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("teams jwks decode: %w", err)
	}
	out := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" {
			continue
		}
		pub, err := rsaPubFromJWK(k.N, k.E)
		if err != nil {
			continue
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("teams jwks: no RSA keys")
	}
	return out, nil
}

// teamsJWKSURI reads the jwks_uri from the OpenID metadata document.
func teamsJWKSURI(ctx context.Context) (string, error) {
	body, err := teamsGetJSON(ctx, teamsOpenIDURL())
	if err != nil {
		return "", err
	}
	var cfg struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return "", fmt.Errorf("teams openid decode: %w", err)
	}
	if cfg.JWKSURI == "" {
		return "", fmt.Errorf("teams openid: no jwks_uri")
	}
	return cfg.JWKSURI, nil
}

// teamsOpenIDURL is a var (defaulting to the real doc) so tests can point the JWKS
// discovery at an httptest stub.
var teamsOpenIDURL = func() string { return botFrameworkOpenID }

func teamsGetJSON(ctx context.Context, urlStr string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("teams metadata http %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, bridgeMaxBody))
}

// rsaPubFromJWK builds an RSA public key from a JWK's base64url modulus (n) and
// exponent (e).
func rsaPubFromJWK(nB64, eB64 string) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	if len(nb) == 0 || len(eb) == 0 {
		return nil, fmt.Errorf("teams jwk: empty n/e")
	}
	e := 0
	for _, b := range eb {
		e = e<<8 | int(b)
	}
	if e < 2 {
		return nil, fmt.Errorf("teams jwk: bad exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
}
