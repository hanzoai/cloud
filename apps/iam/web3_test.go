package iam

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	luxcrypto "github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	wc "github.com/luxwallet/connect/go/walletconnect"
	"github.com/mr-tron/base58"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/iam/pkg/model"
	"github.com/hanzoai/iam/pkg/schema"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"
)

func setupWeb3IAMApp(t *testing.T) *zip.App {
	t.Helper()
	sockDir, err := os.MkdirTemp("", "zipw3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	dir := t.TempDir()
	dbFile := StorePath(dir)
	if err := os.MkdirAll(filepath.Dir(dbFile), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := iamstore.Open("sqlite", dbFile, "")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	cert := orm.New[model.Cert](db)
	cert.Owner, cert.Name = "admin", "cert-web3"
	cert.Type, cert.CryptoAlgorithm, cert.BitSize = "x509", "RS256", 2048
	cert.SetId("admin/cert-web3")
	if err := cert.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}

	org := orm.New[model.Organization](db)
	org.Owner, org.Name = "admin", "hanzo"
	org.DisplayName = "Hanzo"
	org.SetId("admin/hanzo")
	if err := org.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}

	oauthApp := orm.New[model.Application](db)
	oauthApp.Owner, oauthApp.Name = "admin", "hanzo-pay"
	oauthApp.Organization = "hanzo"
	oauthApp.ClientId = "hanzo-pay-client"
	oauthApp.EnableSignUp = true
	oauthApp.OrgChoiceMode = "create"
	oauthApp.Cert = "cert-web3"
	oauthApp.GrantTypes = []string{"authorization_code", "refresh_token"}
	oauthApp.RedirectUris = []string{
		"https://hanzo.ai/pay/callback",
		"https://pay.hanzo.ai/callback",
		"https://hanzo.app/auth/callback",
		"https://hanzo.id/callback",
	}
	oauthApp.ExpireInHours = 1
	oauthApp.SetId("admin/hanzo-pay")
	if err := oauthApp.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	mountSigningKey(t, "cert-web3")
	t.Setenv("IAM_ISSUER", "https://hanzo.id")
	t.Setenv("IAM_ISSUER_MAP", `{"hanzo.id":"https://hanzo.id"}`)
	t.Setenv("initDataFile", filepath.Join(dir, "absent.json"))
	t.Setenv("CLOUD_DATA_DIR", dir)

	app := zip.New(zip.Config{Logger: luxlog.New("web3test"), DisableStartupMessage: true})
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

func doIAMReq(t *testing.T, app *zip.App, method, target, body string, headers map[string]string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rdr)
	req.Host = "hanzo.id"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

type nonceEnvelope struct {
	Status string `json:"status"`
	Msg    string `json:"msg"`
	Data   struct {
		Domain         string `json:"domain"`
		URI            string `json:"uri"`
		Statement      string `json:"statement"`
		Nonce          string `json:"nonce"`
		IssuedAt       string `json:"issuedAt"`
		ExpirationTime string `json:"expirationTime"`
		Version        string `json:"version"`
	} `json:"data"`
}

func buildChallengeMsg(t *testing.T, chain wc.Chain, addr string, env nonceEnvelope) string {
	t.Helper()
	stmt := env.Data.Statement
	ver := env.Data.Version
	exp := env.Data.ExpirationTime
	msg, err := wc.BuildSiwxMessage(wc.BuildParams{
		Chain:   chain,
		Address: addr,
		Challenge: wc.LoginChallenge{
			Domain:         env.Data.Domain,
			URI:            env.Data.URI,
			Statement:      &stmt,
			Nonce:          env.Data.Nonce,
			IssuedAt:       env.Data.IssuedAt,
			ExpirationTime: &exp,
			Version:        &ver,
		},
	})
	if err != nil {
		t.Fatalf("BuildSiwxMessage: %v", err)
	}
	return msg
}

func TestWeb3WalletLoginSameOrgPKCEAndDomainBinding(t *testing.T) {
	app := setupWeb3IAMApp(t)

	// Verify GET /v1/iam/auth/methods advertises web3: true and signup: true for hanzo-pay!
	mCode, rawMethods := doIAMReq(t, app, http.MethodGet,
		"/v1/iam/auth/methods?clientId=hanzo-pay-client&responseType=code", "", nil)
	if mCode != http.StatusOK || !strings.Contains(rawMethods, `"web3":true`) || !strings.Contains(rawMethods, `"signup":true`) {
		t.Fatalf("expected /v1/iam/auth/methods to advertise web3:true and signup:true, got %d %s", mCode, rawMethods)
	}

	priv, err := luxcrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	checksumAddr := wc.AddressFromPublicKey(luxcrypto.FromECDSAPub(&priv.PublicKey))
	lowerAddr := strings.ToLower(checksumAddr)

	signEVM := func(msg string) string {
		sig, serr := luxcrypto.Sign(wc.EIP191Digest(msg), priv)
		if serr != nil {
			t.Fatal(serr)
		}
		sig[64] += 27
		return "0x" + hex.EncodeToString(sig)
	}

	// PKCE verifier + challenge + checkout context across pay.hanzo.ai / hanzo.ai/pay.
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk-hanzo-pay-pkce-verifier"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirectURI := "https://hanzo.ai/pay/callback"
	state := "pay_state_9f8e7d6c"

	// 1. Request challenge using snake_case client_id and mixed-case chain="EVM" from https://hanzo.ai.
	code, rawNonce := doIAMReq(t, app, http.MethodGet,
		"/v1/iam/web3/nonce?chain=EVM&address="+url.QueryEscape(checksumAddr)+"&client_id=hanzo-pay-client",
		"", map[string]string{"Origin": "https://hanzo.ai"})
	if code != http.StatusOK {
		t.Fatalf("nonce status=%d body=%s", code, rawNonce)
	}
	var ch nonceEnvelope
	if err := json.Unmarshal([]byte(rawNonce), &ch); err != nil || ch.Status != "ok" || ch.Data.Domain != "hanzo.ai" {
		t.Fatalf("unexpected challenge: %+v (%s)", ch, rawNonce)
	}

	msg1 := buildChallengeMsg(t, wc.ChainEVM, checksumAddr, ch)
	sig1 := signEVM(msg1)

	// 2. Verify with PKCE & checkout params passed via query string + snake_case; provisions wallet-evm-<hash> with zero email.
	verifyQuery := url.Values{
		"response_type":         {"code"},
		"client_id":             {"hanzo-pay-client"},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"scope":                 {"openid profile"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"plan":                  {"dev-pro"},
		"amount":                {"5000"},
		"network":               {"base"},
		"asset":                 {"USDC"},
		"return_to":             {"/pay?plan=dev-pro&amount=5000&network=base"},
	}
	verifyBody1, _ := json.Marshal(map[string]any{
		"chain":     "EVM",
		"scheme":    "eip191",
		"address":   checksumAddr,
		"message":   msg1,
		"signature": sig1,
	})
	code, rawVerify1 := doIAMReq(t, app, http.MethodPost,
		"/v1/iam/web3/verify?"+verifyQuery.Encode(),
		string(verifyBody1),
		map[string]string{"Content-Type": "application/json", "Origin": "https://hanzo.ai"})
	if code != http.StatusOK {
		t.Fatalf("verify1 status=%d body=%s", code, rawVerify1)
	}
	var vr1 struct {
		Status     string `json:"status"`
		Msg        string `json:"msg"`
		Data       string `json:"data"`
		Sub        string `json:"sub"`
		RedirectTo string `json:"redirectTo"`
	}
	if err := json.Unmarshal([]byte(rawVerify1), &vr1); err != nil || vr1.Status != "ok" || vr1.Data == "" {
		t.Fatalf("verify1 failed: %s", rawVerify1)
	}
	if vr1.Sub != state || !strings.Contains(vr1.RedirectTo, "plan=dev-pro") || !strings.Contains(vr1.RedirectTo, "amount=5000") || !strings.Contains(vr1.RedirectTo, "network=base") || !strings.Contains(vr1.RedirectTo, "asset=USDC") || !strings.Contains(vr1.RedirectTo, "state="+state) {
		t.Fatalf("expected PKCE state & checkout params preserved in verify response: %+v", vr1)
	}

	// 3. Replay of the exact same signed proof MUST fail immediately (single-use challenge burned).
	_, rawReplay := doIAMReq(t, app, http.MethodPost,
		"/v1/iam/web3/verify?"+verifyQuery.Encode(),
		string(verifyBody1),
		map[string]string{"Content-Type": "application/json", "Origin": "https://hanzo.ai"})
	if strings.Contains(rawReplay, `"status":"ok"`) {
		t.Fatalf("replayed challenge must be rejected, got: %s", rawReplay)
	}

	// 4. Exchange authorization code + PKCE code_verifier at POST /v1/iam/oauth/token.
	tokForm := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {"hanzo-pay-client"},
		"code":          {vr1.Data},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	code, rawTok1 := doIAMReq(t, app, http.MethodPost, "/v1/iam/oauth/token", tokForm.Encode(),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if code != http.StatusOK {
		t.Fatalf("oauth/token status=%d body=%s", code, rawTok1)
	}
	var tok1 struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(rawTok1), &tok1); err != nil || tok1.AccessToken == "" {
		t.Fatalf("missing access_token: %s", rawTok1)
	}
	claims1 := decodeJWTClaims(t, tok1.AccessToken)
	org1 := walletOrgFromClaims(claims1)
	if !strings.HasPrefix(org1, "wallet-evm-") {
		t.Fatalf("expected provisioned org wallet-evm-<hash>, got %q (claims=%v)", org1, claims1)
	}

	// 5. Simulate a legacy mixed-case schema.Wallet row on disk, then sign in a second time
	// from https://pay.hanzo.ai using lowercase address: MUST resolve to the EXACT same org
	// with zero duplicate organizations!
	ctx := context.Background()
	wRows, err := orm.TypedQuery[schema.Wallet](DB()).Filter("Chain=", "evm").GetAll(ctx)
	if err != nil || len(wRows) != 1 {
		t.Fatalf("expected 1 wallet row, got %d (%v)", len(wRows), err)
	}
	// Mutate stored Address to mixed-case checksumAddr to test canonicalizeEVMWalletRows healing.
	wRows[0].Address = checksumAddr
	_ = wRows[0].UpdateCtx(ctx)

	_, rawNonce2 := doIAMReq(t, app, http.MethodGet,
		"/v1/iam/web3/nonce?chain=evm&address="+url.QueryEscape(lowerAddr)+"&client_id=hanzo-pay-client",
		"", map[string]string{"Origin": "https://pay.hanzo.ai"})
	var ch2 nonceEnvelope
	_ = json.Unmarshal([]byte(rawNonce2), &ch2)
	if ch2.Data.Domain != "pay.hanzo.ai" {
		t.Fatalf("expected domain pay.hanzo.ai, got %q", ch2.Data.Domain)
	}

	// Domain-bound check: attempting to redeem pay.hanzo.ai's challenge from https://hanzo.app must fail.
	msg2 := buildChallengeMsg(t, wc.ChainEVM, lowerAddr, ch2)
	sig2 := signEVM(msg2)
	badDomainBody, _ := json.Marshal(map[string]any{
		"chain":         "evm",
		"scheme":        "secp256k1-eip191",
		"address":       lowerAddr,
		"message":       msg2,
		"signature":     sig2,
		"client_id":     "hanzo-pay-client",
		"redirect_uri":  "https://pay.hanzo.ai/callback",
		"response_type": "code",
	})
	_, rawWrongDomain := doIAMReq(t, app, http.MethodPost, "/v1/iam/web3/verify", string(badDomainBody),
		map[string]string{"Content-Type": "application/json", "Origin": "https://hanzo.app"})
	if strings.Contains(rawWrongDomain, `"status":"ok"`) {
		t.Fatalf("cross-domain redemption must fail, got: %s", rawWrongDomain)
	}

	// Fresh challenge on pay.hanzo.ai redeemed with lowercase address resolves to org1!
	_, rawNonce3 := doIAMReq(t, app, http.MethodGet,
		"/v1/iam/web3/nonce?chain=evm&address="+url.QueryEscape(lowerAddr)+"&client_id=hanzo-pay-client",
		"", map[string]string{"Origin": "https://pay.hanzo.ai"})
	var ch3 nonceEnvelope
	_ = json.Unmarshal([]byte(rawNonce3), &ch3)
	msg3 := buildChallengeMsg(t, wc.ChainEVM, lowerAddr, ch3)
	sig3 := signEVM(msg3)
	verifyBody3, _ := json.Marshal(map[string]any{
		"chain":                 "evm",
		"scheme":                "secp256k1-eip191",
		"address":               lowerAddr,
		"message":               msg3,
		"signature":             sig3,
		"response_type":         "code",
		"client_id":             "hanzo-pay-client",
		"redirect_uri":          "https://pay.hanzo.ai/callback",
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
	})
	_, rawVerify3 := doIAMReq(t, app, http.MethodPost, "/v1/iam/web3/verify", string(verifyBody3),
		map[string]string{"Content-Type": "application/json", "Origin": "https://pay.hanzo.ai"})
	var vr3 struct {
		Status string `json:"status"`
		Data   string `json:"data"`
	}
	if err := json.Unmarshal([]byte(rawVerify3), &vr3); err != nil || vr3.Status != "ok" {
		t.Fatalf("repeat lowercase verify failed: %s", rawVerify3)
	}
	tokForm3 := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {"hanzo-pay-client"},
		"code":          {vr3.Data},
		"redirect_uri":  {"https://pay.hanzo.ai/callback"},
		"code_verifier": {verifier},
	}
	_, rawTok3 := doIAMReq(t, app, http.MethodPost, "/v1/iam/oauth/token", tokForm3.Encode(),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	var tok3 struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal([]byte(rawTok3), &tok3)
	claims3 := decodeJWTClaims(t, tok3.AccessToken)
	if walletOrgFromClaims(claims3) != org1 || claims3["sub"] != claims1["sub"] {
		t.Fatalf("repeat lowercase wallet login created duplicate org/user: first=%v second=%v", claims1, claims3)
	}

	// Verify only 1 wallet-evm-* organization exists in IAM DB.
	orgs, err := orm.TypedQuery[model.Organization](DB()).GetAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	walletEVMOrgs := 0
	for _, o := range orgs {
		if strings.HasPrefix(o.Name, "wallet-evm-") {
			walletEVMOrgs++
		}
	}
	if walletEVMOrgs != 1 {
		t.Fatalf("expected exactly 1 wallet-evm-* org, got %d", walletEVMOrgs)
	}

	// 6. Solana Ed25519 wallet login on hanzo.app provisions wallet-solana-<hash> cleanly.
	solPub, solPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	solAddr := base58.Encode(solPub)
	_, rawSolNonce := doIAMReq(t, app, http.MethodGet,
		"/v1/iam/web3/nonce?chain=Solana&address="+url.QueryEscape(solAddr)+"&client_id=hanzo-pay-client",
		"", map[string]string{"Origin": "https://hanzo.app"})
	var chSol nonceEnvelope
	_ = json.Unmarshal([]byte(rawSolNonce), &chSol)
	solMsg := buildChallengeMsg(t, wc.ChainSolana, solAddr, chSol)
	solSig := base64.StdEncoding.EncodeToString(ed25519.Sign(solPriv, []byte(solMsg)))
	solVerifyBody, _ := json.Marshal(map[string]any{
		"chain":                 "Solana",
		"scheme":                "ed25519",
		"address":               solAddr,
		"message":               solMsg,
		"signature":             solSig,
		"response_type":         "code",
		"client_id":             "hanzo-pay-client",
		"redirect_uri":          "https://hanzo.app/auth/callback",
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
	})
	_, rawSolVerify := doIAMReq(t, app, http.MethodPost, "/v1/iam/web3/verify", string(solVerifyBody),
		map[string]string{"Content-Type": "application/json", "Origin": "https://hanzo.app"})
	var vrSol struct {
		Status string `json:"status"`
		Data   string `json:"data"`
	}
	if err := json.Unmarshal([]byte(rawSolVerify), &vrSol); err != nil || vrSol.Status != "ok" {
		t.Fatalf("solana verify failed: %s", rawSolVerify)
	}
}

func walletOrgFromClaims(claims map[string]any) string {
	if ba, _ := claims["billing_account"].(string); strings.HasPrefix(ba, "org:") {
		return strings.TrimPrefix(ba, "org:")
	}
	if sub, _ := claims["sub"].(string); strings.Contains(sub, "/") {
		return strings.SplitN(sub, "/", 2)[0]
	}
	org, _ := claims["owner"].(string)
	return org
}

func decodeJWTClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed JWT: %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("unmarshal JWT claims: %v", err)
	}
	return out
}
