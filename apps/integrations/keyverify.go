package integrations

// keyverify.go is the ONE mechanism for the dominant connector shape: a
// customer-held API key verified LIVE by calling a cheap authenticated endpoint
// and accepting it iff the call returns 2xx. It decomplects the verify TRANSPORT
// (here) from the provider DATA (each catalog entry's keySpec), so a new
// key-based connector is a declarative registration, not another hand-rolled
// HTTP dance. Fail-closed and token-free by construction — parity with
// anthropicVerify (which predates this and keeps its bespoke dual-mode): any
// non-2xx stores nothing, and no error ever carries the credential value.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// apiKeySecret is the canonical KMS slot a single customer-held key seals into,
// shared by every key-based provider (namespaced per provider by the store, so
// reuse is correct, not a collision).
const apiKeySecret = "api_key"

// placement selects where the credential rides on the verify request. Closed set.
type placement int

const (
	bearer   placement = iota // Authorization: Bearer <key>
	tokenAuth                 // Authorization: Token <key>
	headerAt                  // <Name>: [<Scheme> ]<key>
	queryAt                   // ?<Name>=<key>
	basicAt                   // Authorization: Basic base64(<user>:<key>)
	basicRaw                  // Authorization: Basic base64(<key>)  — key already "user:pass"
)

// accountToken marks a keySpec field (basic username, or "{account}" in the URL)
// that resolves to the caller-supplied VerifyInput.AccountID at call time.
const accountToken = "{account}"

// keySpec is the per-provider verify recipe: pure data interpreted by keyVerify.
type keySpec struct {
	// provider is the slug, used ONLY for token-free error text.
	provider string
	// origin builds the request scheme+host from the input. Most providers ignore
	// the input and return a constant (constOrigin); shopify derives it from the
	// store domain, mailchimp from the key's data-center suffix. It returns a
	// token-free error when a required account/format is absent.
	origin func(VerifyInput) (string, error)
	// path is appended to origin. An "{account}" token is replaced by the escaped
	// AccountID (twilio's Accounts/<sid>.json), which then becomes required.
	path string
	// method defaults to GET; POST providers (paypal, linear) set it with a body.
	method, body, bodyType string
	// place + name + scheme locate the credential on the request. name is the
	// header/query name or basic username ("{account}" => AccountID); scheme is the
	// optional headerAt prefix (e.g. "Klaviyo-API-Key").
	place  placement
	name   string
	scheme string
	// extra are static headers every request carries (api versions, Accept).
	extra map[string]string
	// minLen/prefix are OFFLINE rejects: a syntactically impossible key never
	// reaches the network. 0/"" skips the check.
	minLen int
	prefix string
	// secret is the KMS slot the verified key seals into (default apiKeySecret).
	secret string
	// echoAccount sets ExchangeResult.ExternalID from AccountID (the shop domain /
	// Twilio SID the caller supplied — the only identity source on these).
	echoAccount bool
}

// envBase returns a trimmed, slash-normalized origin override from env, or "".
// It is the ONE httptest/operator seam every origin builder consults first.
func envBase(key string) string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv(key)), "/")
}

// constOrigin returns a fixed origin, overridable via env (the httptest seam),
// so an operator or a test can repoint a provider without a rebuild.
func constOrigin(envKey, def string) func(VerifyInput) (string, error) {
	return func(VerifyInput) (string, error) {
		if v := envBase(envKey); v != "" {
			return v, nil
		}
		return def, nil
	}
}

// keyVerify compiles a keySpec into a Provider.Verify. It is the ONE key-based
// verify path: fail-closed and token-free. Any non-2xx stores nothing; no error
// ever carries the credential value (the query-placement key rides the URL, so
// transport errors are returned unwrapped — never with the *url.Error).
func keyVerify(sp keySpec) func(context.Context, VerifyInput) (*ExchangeResult, error) {
	return func(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
		key := strings.TrimSpace(in.Token)
		if key == "" {
			return nil, fmt.Errorf("empty credential")
		}
		if sp.minLen > 0 && len(key) < sp.minLen {
			return nil, fmt.Errorf("%s credential is too short", sp.provider)
		}
		if sp.prefix != "" && !strings.HasPrefix(key, sp.prefix) {
			return nil, fmt.Errorf("%s credential has an unexpected format", sp.provider)
		}
		origin, err := sp.origin(in)
		if err != nil {
			return nil, err // account-shape errors only; never the token
		}
		target := strings.TrimRight(origin, "/") + sp.path
		if strings.Contains(target, accountToken) {
			acct := strings.TrimSpace(in.AccountID)
			if acct == "" {
				return nil, fmt.Errorf("%s needs an account identifier", sp.provider)
			}
			target = strings.ReplaceAll(target, accountToken, url.PathEscape(acct))
		}
		if sp.place == queryAt {
			sep := "?"
			if strings.Contains(target, "?") {
				sep = "&"
			}
			target += sep + url.QueryEscape(sp.name) + "=" + url.QueryEscape(key)
		}
		method := sp.method
		if method == "" {
			method = http.MethodGet
		}
		var body io.Reader
		if sp.body != "" {
			body = strings.NewReader(sp.body)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, body)
		if err != nil {
			return nil, fmt.Errorf("%s verify: build request", sp.provider)
		}
		applyAuth(req, sp, key, in)
		for k, v := range sp.extra {
			req.Header.Set(k, v)
		}
		if sp.body != "" && sp.bodyType != "" {
			req.Header.Set("Content-Type", sp.bodyType)
		}
		resp, err := authClient.Do(req)
		if err != nil {
			// Deliberately unwrapped: for queryAt the key is in the URL, and the
			// wrapped *url.Error would echo it. The message stays token-free.
			return nil, fmt.Errorf("%s verify request failed", sp.provider)
		}
		defer func() { _ = resp.Body.Close() }()
		// Drain (bounded) so the connection can be reused; the content is unused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode <= 299:
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("%s rejected the credential (%d)", sp.provider, resp.StatusCode)
		default:
			return nil, fmt.Errorf("%s verify failed (%d)", sp.provider, resp.StatusCode)
		}
		secret := sp.secret
		if secret == "" {
			secret = apiKeySecret
		}
		res := &ExchangeResult{Tokens: map[string]string{secret: key}}
		if sp.echoAccount {
			res.ExternalID = strings.TrimSpace(in.AccountID)
		}
		return res, nil
	}
}

// applyAuth places the credential on the request per the spec's placement.
func applyAuth(req *http.Request, sp keySpec, key string, in VerifyInput) {
	switch sp.place {
	case bearer:
		req.Header.Set("Authorization", "Bearer "+key)
	case tokenAuth:
		req.Header.Set("Authorization", "Token "+key)
	case headerAt:
		v := key
		if sp.scheme != "" {
			v = sp.scheme + " " + key
		}
		req.Header.Set(sp.name, v)
	case queryAt:
		// Placed on the URL in keyVerify (before the request is built).
	case basicAt:
		user := sp.name
		if user == accountToken {
			user = strings.TrimSpace(in.AccountID)
		}
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+key)))
	case basicRaw:
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(key)))
	}
}
