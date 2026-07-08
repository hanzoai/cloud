package wallets

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luxfi/crypto"
)

// TestSafeCustody exercises the KindSafe backend end-to-end against a stub that
// emulates BOTH ring planes: the :9800 internal threshold API (bearer-gated
// keygen/sign) and the :8081 product API (ring-JWT-gated Safe deploy/propose).
// It asserts (a) Provision keygens an owner EOA then deploys a Safe and records
// both handles, (b) the owner-sign path works, and (c) proposeSafeTx mints a
// VALID HS256 ring JWT (iss/aud/role/org) the server verifies, and returns the
// EIP-712 hash + signature.
func TestSafeCustody(t *testing.T) {
	const (
		internalKey = "shared-mpc-internal-api-key"
		jwtSecret   = "shared-mpc-jwt-secret"
		mpcEOA      = "0x00112233445566778899aabbccddeeff00112233"
		safeAddr    = "0xaabbccddeeff00112233445566778899aabbccdd"
		ringWallet  = "wal_stub"
		smartWallet = "sw_stub"
		safeTxHash  = "0x" + "11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff"
		sigR        = "0x1111111111111111111111111111111111111111111111111111111111111111"
		sigS        = "0x2222222222222222222222222222222222222222222222222222222222222222"
	)
	// stubSig is a 65-byte recoverable-shaped signature (opaque to the client).
	stubSig := "0x11" + strings.Repeat("00", 64)

	// verifyRingJWT checks the Authorization bearer is a valid HS256 ring token.
	// Returns the org_id claim, or "" (with a written 401) on any failure.
	verifyRingJWT := func(w http.ResponseWriter, auth string) string {
		tok := strings.TrimPrefix(auth, "Bearer ")
		parts := strings.Split(tok, ".")
		if len(parts) != 3 {
			http.Error(w, "not a jwt", http.StatusUnauthorized)
			return ""
		}
		mac := hmac.New(sha256.New, []byte(jwtSecret))
		mac.Write([]byte(parts[0] + "." + parts[1]))
		want, _ := base64.RawURLEncoding.DecodeString(parts[2])
		if !hmac.Equal(mac.Sum(nil), want) {
			http.Error(w, "bad jwt signature", http.StatusUnauthorized)
			return ""
		}
		payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var c struct {
			Org  string   `json:"org_id"`
			Role string   `json:"role"`
			Iss  string   `json:"iss"`
			Aud  []string `json:"aud"`
		}
		_ = json.Unmarshal(payload, &c)
		if c.Iss != ringJWTIssuer || c.Role != ringJWTRole || len(c.Aud) == 0 || c.Aud[0] != ringJWTAudience {
			http.Error(w, "bad jwt claims", http.StatusUnauthorized)
			return ""
		}
		return c.Org
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		switch {
		// ── :9800 internal threshold API (static bearer) ──
		case r.URL.Path == "/keygen":
			if r.Header.Get("Authorization") != "Bearer "+internalKey {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if body["org_id"] != "acme" {
				http.Error(w, "bad org_id", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"wallet_id": ringWallet, "result_type": "success", "evm_address": mpcEOA,
			})
		case r.URL.Path == "/sign":
			if r.Header.Get("Authorization") != "Bearer "+internalKey {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if body["wallet_id"] != ringWallet {
				http.Error(w, "bad wallet_id", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"signature": stubSig, "status": "success"})

		// ── :8081 product API (ring JWT) ──
		case r.URL.Path == "/v1/wallets/"+ringWallet+"/smart-wallet":
			if verifyRingJWT(w, r.Header.Get("Authorization")) != "acme" {
				return
			}
			if body["wallet_type"] != "safe" {
				http.Error(w, "bad wallet_type", http.StatusBadRequest)
				return
			}
			owners, _ := body["owners"].([]any)
			if len(owners) == 0 || owners[0] != mpcEOA {
				http.Error(w, "mpc EOA must be an owner", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": smartWallet, "walletId": ringWallet, "contractAddress": safeAddr,
				"chain": body["chain"], "walletType": "safe", "threshold": 1, "status": "pending",
			})
		case r.URL.Path == "/v1/smart-wallets/"+smartWallet+"/propose":
			if verifyRingJWT(w, r.Header.Get("Authorization")) != "acme" {
				return
			}
			if body["chain_id"] == nil || body["chain_id"].(float64) == 0 {
				http.Error(w, "chain_id required", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"safe_tx_hash": safeTxHash, "signature_r": sigR, "signature_s": sigS,
			})
		default:
			http.Error(w, "unexpected route: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	mpcCl := newMPCClient([]string{srv.URL}, []byte(internalKey))
	safeCl := newSafeClient(srv.URL, []byte(jwtSecret))
	if !safeCl.configured() {
		t.Fatal("safe client should be configured with base + secret")
	}
	s, app := newSvc(t, map[Kind]Custody{KindSafe: safeCustody{mpc: mpcCl, safe: safeCl}}, KindSafe)

	acct := mkAccount(t, app, "acme")
	w := mkWallet(t, app, "acme", acct, "safe", "warm", "eip155:36963")
	if w.Custody != KindSafe {
		t.Fatalf("custody = %q, want safe", w.Custody)
	}
	if w.Address != safeAddr {
		t.Fatalf("safe address = %q, want %q", w.Address, safeAddr)
	}
	// KeyRef encodes both ring handles.
	stored, found, err := s.store.getWallet(context.Background(), "acme", w.ID)
	if err != nil || !found {
		t.Fatalf("store getWallet: found=%v err=%v", found, err)
	}
	if stored.KeyRef != ringWallet+"|"+smartWallet {
		t.Fatalf("safe key ref = %q, want %q", stored.KeyRef, ringWallet+"|"+smartWallet)
	}

	// Owner-sign via the uniform /sign route reaches :9800 and returns the sig.
	digest := crypto.Keccak256([]byte("safe owner sign"))
	sig := signOnce(t, app, "acme", w.ID, digest)
	if hex.EncodeToString(sig) != trim0x(stubSig) {
		t.Fatalf("owner sign = %s, want %s", hex.EncodeToString(sig), trim0x(stubSig))
	}

	// Safe-tx propose mints a valid ring JWT and returns the EIP-712 hash + sig.
	code, respBody := req(t, app, http.MethodPost, "/v1/wallets/"+w.ID+"/safe-tx", "acme",
		map[string]any{"to": mpcEOA, "value": "0", "chainId": 36963})
	if code != http.StatusOK {
		t.Fatalf("safe-tx status = %d, body=%s", code, respBody)
	}
	var out struct {
		SafeTxHash string `json:"safeTxHash"`
		R          string `json:"r"`
		S          string `json:"s"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		t.Fatalf("decode safe-tx resp: %v", err)
	}
	if out.SafeTxHash != safeTxHash || out.R != sigR || out.S != sigS {
		t.Fatalf("safe-tx result = %+v, want hash/r/s = %s/%s/%s", out, safeTxHash, sigR, sigS)
	}
}

// TestSafeCustody_FailClosed proves an unconfigured safe backend (no base/secret)
// never fabricates a wallet — it fails closed like mpc/treasury.
func TestSafeCustody_FailClosed(t *testing.T) {
	sc := safeCustody{mpc: newMPCClient(nil, nil), safe: newSafeClient("", nil)}
	if _, err := sc.Provision(context.Background(), &Wallet{Org: "acme", Chain: "eip155:36963"}); err != ErrMPCNotConfigured {
		t.Fatalf("unconfigured safe Provision err = %v, want ErrMPCNotConfigured", err)
	}
}
