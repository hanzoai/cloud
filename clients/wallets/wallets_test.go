package wallets

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/kms"
	"github.com/luxfi/crypto"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── harness ──────────────────────────────────────────────────────────────────

// newSvc builds a wallets svc over a fresh temp store with the given custody set,
// installs it as the process singleton, and mounts the routes on a fresh app.
func newSvc(t *testing.T, custody map[Kind]Custody, def Kind) (*svc, *zip.App) {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "wallets.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := luxlog.New("test")
	s := &svc{store: st, custody: custody, defaultCustody: def, log: log}
	mounted = s
	t.Cleanup(func() { mounted = nil })
	app := zip.New(zip.Config{Logger: log})
	app.Post("/v1/wallets/accounts", s.createAccount)
	app.Get("/v1/wallets/accounts", s.listAccounts)
	app.Post("/v1/wallets", s.createWallet)
	app.Get("/v1/wallets", s.listWallets)
	app.Get("/v1/wallets/:id", s.getWallet)
	app.Post("/v1/wallets/:id/keys", s.rotateKeys)
	app.Post("/v1/wallets/:id/sign", s.sign)
	app.Post("/v1/wallets/:id/safe-tx", s.proposeSafeTx)
	return s, app
}

// testKMS builds a REAL in-process luxfi/kms client (sealed store under dir/kms)
// and returns it plus its data dir (for the at-rest seal assertion).
func testKMS(t *testing.T) (*kms.Client, string) {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	c, err := kms.New(kms.Config{DataDir: dir, MasterKeyB64: base64.StdEncoding.EncodeToString(raw)}, luxlog.New("test-kms"))
	if err != nil {
		t.Fatalf("kms.New: %v", err)
	}
	if !c.Ready() {
		t.Fatal("kms not ready (master key not accepted)")
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, dir
}

// req drives one HTTP request. org (when non-empty) sets a VALIDATED principal
// (X-Org-Id + X-User-Id, exactly as the gateway mints them).
func req(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	hr := httptest.NewRequest(method, path, r)
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		hr.Header.Set("X-Org-Id", org)
		hr.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// mkAccount creates an account for org and returns its id.
func mkAccount(t *testing.T, app *zip.App, org string) string {
	t.Helper()
	code, body := req(t, app, http.MethodPost, "/v1/wallets/accounts", org, map[string]any{"name": "ops"})
	if code != http.StatusOK {
		t.Fatalf("create account = %d (%s)", code, body)
	}
	var a Account
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatalf("decode account: %v (%s)", err, body)
	}
	return a.ID
}

// mkWallet creates a wallet under acctID and returns it.
func mkWallet(t *testing.T, app *zip.App, org, acctID, custody, tier, chain string) Wallet {
	t.Helper()
	code, body := req(t, app, http.MethodPost, "/v1/wallets", org, map[string]any{
		"accountId": acctID, "name": "w", "custody": custody, "tier": tier, "chain": chain,
	})
	if code != http.StatusOK {
		t.Fatalf("create wallet = %d (%s)", code, body)
	}
	var w Wallet
	if err := json.Unmarshal(body, &w); err != nil {
		t.Fatalf("decode wallet: %v (%s)", err, body)
	}
	return w
}

// signOnce POSTs a digest to a wallet's sign route and returns the recovered sig bytes.
func signOnce(t *testing.T, app *zip.App, org, walletID string, digest []byte) []byte {
	t.Helper()
	code, body := req(t, app, http.MethodPost, "/v1/wallets/"+walletID+"/sign", org,
		map[string]any{"digest": "0x" + hex.EncodeToString(digest)})
	if code != http.StatusOK {
		t.Fatalf("sign = %d (%s)", code, body)
	}
	var sr struct {
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(body, &sr); err != nil {
		t.Fatalf("decode sig: %v (%s)", err, body)
	}
	sig, err := hex.DecodeString(trim0x(sr.Signature))
	if err != nil {
		t.Fatalf("sig not hex: %v", err)
	}
	return sig
}

func isHexAddr(s string) bool {
	if !strings.HasPrefix(s, "0x") || len(s) != 42 {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}

// ── 1. KMS single-sig custody, end-to-end (the fully-exercised spine) ─────────

func TestKMSSingleSigEndToEnd(t *testing.T) {
	k, dir := testKMS(t)
	_, app := newSvc(t, map[Kind]Custody{KindKMS: kmsCustody{kms: k}}, KindKMS)

	acct := mkAccount(t, app, "acme")
	w := mkWallet(t, app, "acme", acct, "kms", "hot", "eip155:1")
	if !isHexAddr(w.Address) {
		t.Fatalf("wallet address not a 0x address: %q", w.Address)
	}

	// Sign a digest; the signature must recover to the wallet address — proof the
	// custody actually holds and uses the wallet's own key.
	digest := crypto.Keccak256([]byte("hello wallets"))
	sig := signOnce(t, app, "acme", w.ID, digest)
	pub, err := crypto.SigToPub(digest, sig)
	if err != nil {
		t.Fatalf("SigToPub: %v", err)
	}
	if got := crypto.PubkeyToAddress(*pub).Hex(); got != w.Address {
		t.Fatalf("signature recovered to %s, want wallet address %s", got, w.Address)
	}

	// Custody is REAL, not a passthrough: the custodied secret is 32-byte key
	// material (not the public address), and it is the key backing the address.
	stored, err := k.GetSecret(context.Background(), keyRef(&Wallet{Org: "acme", ID: w.ID}))
	if err != nil {
		t.Fatalf("GetSecret: %v", err)
	}
	if string(stored) == w.Address || "0x"+string(stored) == w.Address {
		t.Fatal("custodied secret must not be the public address (passthrough)")
	}
	priv2, err := crypto.HexToECDSA(strings.TrimSpace(string(stored)))
	if err != nil {
		t.Fatalf("custodied secret not a private key: %v", err)
	}
	if crypto.PubkeyToAddress(priv2.PublicKey).Hex() != w.Address {
		t.Fatal("custodied secret is not the key backing the wallet address")
	}
	// Sealed at rest: the plaintext key hex must NEVER appear on disk (the KMS
	// seals the value AND block-encrypts the store).
	assertSealedAtRest(t, dir, string(stored))

	// Rotate → the address changes and the NEW signature recovers to the NEW address.
	code, body := req(t, app, http.MethodPost, "/v1/wallets/"+w.ID+"/keys", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("rotate = %d (%s)", code, body)
	}
	var w2 Wallet
	if err := json.Unmarshal(body, &w2); err != nil {
		t.Fatalf("decode rotated: %v", err)
	}
	if w2.Address == w.Address {
		t.Fatal("rotate must change the address (fresh key material)")
	}
	if !isHexAddr(w2.Address) {
		t.Fatalf("rotated address not a 0x address: %q", w2.Address)
	}
	sig2 := signOnce(t, app, "acme", w.ID, digest)
	pub2, err := crypto.SigToPub(digest, sig2)
	if err != nil {
		t.Fatalf("SigToPub after rotate: %v", err)
	}
	if got := crypto.PubkeyToAddress(*pub2).Hex(); got != w2.Address {
		t.Fatalf("post-rotate signature recovered to %s, want %s", got, w2.Address)
	}
}

// assertSealedAtRest fails if the plaintext key hex is found in any file under dir.
func assertSealedAtRest(t *testing.T, dir, plaintextHex string) {
	t.Helper()
	needle := []byte(plaintextHex)
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		if bytes.Contains(b, needle) {
			t.Fatalf("plaintext key hex found on disk at %s — custody is not sealed", p)
		}
		return nil
	})
}

// ── 2. per-tenant isolation ──────────────────────────────────────────────────

func TestPerTenantIsolation(t *testing.T) {
	k, _ := testKMS(t)
	_, app := newSvc(t, map[Kind]Custody{KindKMS: kmsCustody{kms: k}}, KindKMS)

	// Org A owns a wallet.
	acctA := mkAccount(t, app, "orga")
	wa := mkWallet(t, app, "orga", acctA, "kms", "hot", "eip155:1")

	// Org B cannot GET org A's wallet.
	if code, _ := req(t, app, http.MethodGet, "/v1/wallets/"+wa.ID, "orgb", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant GET = %d, want 404", code)
	}
	// Org B cannot SIGN with org A's wallet.
	if code, _ := req(t, app, http.MethodPost, "/v1/wallets/"+wa.ID+"/sign", "orgb",
		map[string]any{"message": "steal"}); code != http.StatusNotFound {
		t.Fatalf("cross-tenant sign = %d, want 404", code)
	}
	// Org B's list excludes org A's wallet.
	_, body := req(t, app, http.MethodGet, "/v1/wallets", "orgb", nil)
	var list struct {
		Wallets []Wallet `json:"wallets"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	for _, w := range list.Wallets {
		if w.ID == wa.ID {
			t.Fatal("org B's wallet list leaked org A's wallet")
		}
	}
	// Unauthenticated (no validated principal) → 403 across the surface.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/wallets"},
		{http.MethodGet, "/v1/wallets/accounts"},
		{http.MethodPost, "/v1/wallets/accounts"},
		{http.MethodGet, "/v1/wallets/" + wa.ID},
		{http.MethodPost, "/v1/wallets/" + wa.ID + "/sign"},
	} {
		if code, _ := req(t, app, tc.method, tc.path, "", map[string]any{}); code != http.StatusForbidden {
			t.Fatalf("%s %s unauth = %d, want 403", tc.method, tc.path, code)
		}
	}
}

// ── 3. the custody seam selects the backend by config ────────────────────────

func TestCustodySeamSelectsBackend(t *testing.T) {
	k, _ := testKMS(t)
	// Only KMS is configured — mpc/treasury must fail closed.
	s, app := newSvc(t, map[Kind]Custody{KindKMS: kmsCustody{kms: k}}, KindKMS)

	// Resolver: kms resolves; mpc/treasury fail closed; unknown is a distinct error.
	if _, err := s.custodyFor(KindKMS); err != nil {
		t.Fatalf("kms custody must resolve: %v", err)
	}
	if _, err := s.custodyFor(KindMPC); err != ErrMPCNotConfigured {
		t.Fatalf("mpc custody unconfigured = %v, want ErrMPCNotConfigured", err)
	}
	if _, err := s.custodyFor(KindTreasury); err != ErrMPCNotConfigured {
		t.Fatalf("treasury custody unconfigured = %v, want ErrMPCNotConfigured", err)
	}
	if _, err := s.custodyFor(Kind("bogus")); err == nil {
		t.Fatal("unknown custody must error")
	}

	// HTTP: kms works, mpc fails closed (503), unknown is 400.
	acct := mkAccount(t, app, "acme")
	if code, body := req(t, app, http.MethodPost, "/v1/wallets", "acme",
		map[string]any{"accountId": acct, "custody": "kms", "tier": "hot"}); code != http.StatusOK {
		t.Fatalf("kms wallet create = %d (%s), want 200", code, body)
	}
	if code, _ := req(t, app, http.MethodPost, "/v1/wallets", "acme",
		map[string]any{"accountId": acct, "custody": "mpc", "tier": "hot"}); code != http.StatusServiceUnavailable {
		t.Fatalf("mpc wallet create unconfigured = %d, want 503", code)
	}
	if code, _ := req(t, app, http.MethodPost, "/v1/wallets", "acme",
		map[string]any{"accountId": acct, "custody": "bogus", "tier": "hot"}); code != http.StatusBadRequest {
		t.Fatalf("unknown custody create = %d, want 400", code)
	}
}

// ── 4. MPC path wired end-to-end against a faithful stub ─────────────────────

func TestMPCPathWiredCompiles(t *testing.T) {
	const (
		stubAddr = "0x00112233445566778899aabbccddeeff00112233"
		stubKey  = "shared-mpc-internal-api-key"
		// 65-byte recoverable-shaped signature (content is opaque to the client).
		stubSig = "0x" + "11" + sigHexBody
	)
	// The stub emulates the luxfi/mpc ring's INTERNAL threshold API: it gates on
	// the static bearer key and asserts every call carries org_id=acme.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+stubKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if org, _ := body["org_id"].(string); org != "acme" {
			http.Error(w, "bad org_id", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/keygen":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"wallet_id": "wal_stub", "result_type": "success", "evm_address": stubAddr,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/sign":
			// The ring requires wallet_id + a 32-byte payload_hash + an
			// idempotency_key — assert the client populated them.
			if body["wallet_id"] != "wal_stub" || body["idempotency_key"] == "" {
				http.Error(w, "missing sign fields", http.StatusBadRequest)
				return
			}
			if h, _ := body["payload_hash"].(string); len(h) != 64 {
				http.Error(w, "payload_hash not 32-byte hex", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"signature": stubSig, "status": "success"})
		default:
			http.Error(w, "unexpected route: "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client := newMPCClient([]string{srv.URL}, []byte(stubKey))
	if !client.configured() {
		t.Fatal("mpc client should be configured with a node + key")
	}
	s, app := newSvc(t, map[Kind]Custody{KindMPC: mpcCustody{http: client}}, KindMPC)

	acct := mkAccount(t, app, "acme")
	w := mkWallet(t, app, "acme", acct, "mpc", "warm", "eip155:1")
	if w.Custody != KindMPC {
		t.Fatalf("wallet custody = %q, want mpc", w.Custody)
	}
	if w.Address != stubAddr {
		t.Fatalf("mpc wallet address = %q, want stub %q", w.Address, stubAddr)
	}
	// KeyRef is never serialized over the API (json:"-"); verify via the store
	// that the ring wallet id was recorded as the sign handle.
	stored, found, err := s.store.getWallet(context.Background(), "acme", w.ID)
	if err != nil || !found {
		t.Fatalf("store getWallet: found=%v err=%v", found, err)
	}
	if stored.KeyRef != "wal_stub" {
		t.Fatalf("mpc key ref = %q, want stub wallet id", stored.KeyRef)
	}

	// Sign reaches the ring's /sign route and returns the threshold signature.
	digest := crypto.Keccak256([]byte("mpc sign"))
	sig := signOnce(t, app, "acme", w.ID, digest)
	if hex.EncodeToString(sig) != trim0x(stubSig) {
		t.Fatalf("mpc signature = %s, want stub %s", hex.EncodeToString(sig), trim0x(stubSig))
	}
}

// sigHexBody is 128 hex chars (64 bytes); with the leading "11" byte the
// stub signature is 65 bytes — the recoverable secp256k1 shape. Its content is
// opaque to the client, which only hex-decodes and returns it.
const sigHexBody = "0000000000000000000000000000000000000000000000000000000000000000" +
	"0000000000000000000000000000000000000000000000000000000000000000"
