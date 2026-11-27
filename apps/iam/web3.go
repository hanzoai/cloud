package iam

// web3.go hardens the Web3 challenge/verify HTTP surface in front of github.com/hanzoai/iam:
//   - Accepts RFC 6749 snake_case and query-string OAuth2/PKCE parameters (client_id,
//     redirect_uri, state, scope, nonce, code_challenge, code_challenge_method,
//     response_type=code) alongside camelCase JSON fields across hanzo.id, hanzo.ai/pay,
//     pay.hanzo.ai, and hanzo.app, preserving checkout query parameters on redirect_uri.
//   - Normalizes chain names ("EVM" -> "evm", "Solana" -> "solana") and canonicalizes any
//     legacy mixed-case EVM schema.Wallet rows before SQL lookup so repeat wallet sign-ins
//     always resolve to the exact same wallet-evm-<hash> organization with zero duplicates
//     and zero email requirement.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanzoai/iam/pkg/schema"
	"github.com/hanzoai/orm"
	"github.com/zap-proto/zip"
)

const (
	pathWeb3Nonce  = "/v1/iam/web3/nonce"
	pathWeb3Verify = "/v1/iam/web3/verify"
	maxNormalizeSz = 16 * 1024
)

func web3Middleware() zip.Handler {
	return func(c *zip.Ctx) error {
		switch c.Path() {
		case pathWeb3Nonce:
			if c.Method() == http.MethodGet {
				normalizeNonceQuery(c)
			}
			return c.Next()
		case pathWeb3Verify:
			if c.Method() != http.MethodPost {
				return c.Next()
			}
			meta := normalizeVerifyRequest(c)
			if meta.chain == "evm" && meta.address != "" {
				canonicalizeEVMWalletRows(c.Context(), DB(), meta.address)
			}
			err := c.Next()
			if err == nil && meta.flowType == "code" {
				enrichVerifyResponse(c, meta)
			}
			return err
		default:
			return c.Next()
		}
	}
}

type verifyMeta struct {
	chain       string
	address     string
	flowType    string
	redirectURI string
	state       string
	checkout    map[string]string
}

func normalizeNonceQuery(c *zip.Ctx) {
	raw := string(c.Fiber().Request().URI().QueryString())
	if raw == "" {
		return
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return
	}
	changed := false
	if strings.TrimSpace(vals.Get("clientId")) == "" {
		if cid := strings.TrimSpace(vals.Get("client_id")); cid != "" {
			vals.Set("clientId", cid)
			changed = true
		}
	}
	if ch := vals.Get("chain"); ch != "" {
		norm := strings.ToLower(strings.TrimSpace(ch))
		if norm != ch {
			vals.Set("chain", norm)
			changed = true
		}
	}
	if changed {
		c.Fiber().Request().URI().SetQueryString(vals.Encode())
	}
}

func normalizeVerifyRequest(c *zip.Ctx) verifyMeta {
	var meta verifyMeta
	body := c.Body()
	if len(body) == 0 || len(body) > maxNormalizeSz {
		return meta
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return meta
	}

	strOf := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	adopt := func(destKey string, bodyAliases []string, queryKeys ...string) string {
		cur := strOf(append([]string{destKey}, bodyAliases...)...)
		if cur == "" {
			for _, qk := range queryKeys {
				if qv := strings.TrimSpace(c.Query(qk)); qv != "" {
					cur = qv
					break
				}
			}
		}
		if cur != "" {
			m[destKey] = cur
		}
		return cur
	}

	changed := false
	beforeChain, _ := m["chain"].(string)
	normChain := strings.ToLower(strings.TrimSpace(beforeChain))
	if normChain == "" {
		normChain = strings.ToLower(strings.TrimSpace(c.Query("chain")))
	}
	if normChain != "" && normChain != beforeChain {
		m["chain"] = normChain
		changed = true
	}
	meta.chain = normChain
	meta.address = strOf("address")

	if rawScheme, ok := m["scheme"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(rawScheme)) {
		case "eip191", "eip-191", "personal_sign":
			m["scheme"] = "secp256k1-eip191"
			changed = true
		}
	}

	prevSnapshot, _ := json.Marshal(m)
	_ = adopt("clientId", []string{"client_id"}, "clientId", "client_id")
	meta.redirectURI = adopt("redirectUri", []string{"redirect_uri"}, "redirectUri", "redirect_uri")
	meta.state = adopt("state", nil, "state")
	_ = adopt("scope", nil, "scope")
	_ = adopt("nonce", nil, "nonce")
	_ = adopt("codeChallenge", []string{"code_challenge"}, "codeChallenge", "code_challenge")
	_ = adopt("codeChallengeMethod", []string{"code_challenge_method"}, "codeChallengeMethod", "code_challenge_method")

	flowType := adopt("type", nil, "type")
	if flowType == "" {
		rt := strOf("response_type", "responseType")
		if rt == "" {
			rt = strings.TrimSpace(c.Query("response_type"))
		}
		if rt == "" {
			rt = strings.TrimSpace(c.Query("responseType"))
		}
		if strings.EqualFold(rt, "code") {
			flowType = "code"
			m["type"] = "code"
		}
	}
	meta.flowType = strings.ToLower(strings.TrimSpace(flowType))

	for _, ck := range []string{"amount", "plan", "network", "asset", "return_to", "returnUrl"} {
		val := strOf(ck)
		if val == "" {
			val = strings.TrimSpace(c.Query(ck))
		}
		if val != "" && len(val) <= 2048 {
			if meta.checkout == nil {
				meta.checkout = make(map[string]string, 6)
			}
			meta.checkout[ck] = val
		}
	}

	updated, err := json.Marshal(m)
	if err == nil && (changed || string(updated) != string(prevSnapshot)) {
		req := c.Fiber().Request()
		req.SetBodyRaw(updated)
		req.Header.SetContentLength(len(updated))
	}
	return meta
}

func canonicalizeEVMWalletRows(ctx context.Context, db orm.DB, address string) {
	if db == nil {
		return
	}
	want := strings.ToLower(strings.TrimSpace(address))
	if want == "" {
		return
	}
	rows, err := orm.TypedQuery[schema.Wallet](db).Filter("Chain=", "evm").GetAll(ctx)
	if err != nil {
		return
	}
	for _, w := range rows {
		if w == nil {
			continue
		}
		trimmed := strings.TrimSpace(w.Address)
		if !strings.EqualFold(trimmed, want) || w.Address == want {
			continue
		}
		_ = db.RunInTransaction(ctx, func(tx orm.DB) error {
			old := orm.New[schema.Wallet](tx)
			old.SetId(w.Id())
			_ = old.DeleteCtx(ctx)

			fresh := orm.New[schema.Wallet](tx)
			fresh.Owner = w.Owner
			fresh.User = w.User
			fresh.Chain = "evm"
			fresh.Address = want
			fresh.Scheme = w.Scheme
			fresh.PublicKey = w.PublicKey
			fresh.CreatedTime = w.CreatedTime
			fresh.SetId(w.Owner + "/" + w.User + "/evm/" + want)
			return fresh.CreateCtx(ctx)
		})
	}
}

func enrichVerifyResponse(c *zip.Ctx, meta verifyMeta) {
	if c.Fiber().Response().StatusCode() != http.StatusOK {
		return
	}
	raw := c.Fiber().Response().Body()
	if len(raw) == 0 {
		return
	}
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil || env == nil {
		return
	}
	status, _ := env["status"].(string)
	code, _ := env["data"].(string)
	if status != "ok" || strings.TrimSpace(code) == "" {
		return
	}
	changed := false
	if meta.state != "" {
		if sub, _ := env["sub"].(string); sub == "" {
			env["sub"] = meta.state
			changed = true
		}
	}
	if meta.redirectURI != "" {
		if dest := buildOAuthRedirect(meta.redirectURI, code, meta.state, meta.checkout); dest != "" {
			env["redirectTo"] = dest
			changed = true
		}
	}
	if changed {
		if out, err := json.Marshal(env); err == nil {
			resp := c.Fiber().Response()
			resp.SetBodyRaw(out)
			resp.Header.SetContentLength(len(out))
		}
	}
}

func buildOAuthRedirect(redirectURI, code, state string, checkout map[string]string) string {
	u, err := url.Parse(strings.TrimSpace(redirectURI))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	q := u.Query()
	for k, v := range checkout {
		if v != "" && q.Get(k) == "" {
			q.Set(k, v)
		}
	}
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
