package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type nativeKMS struct {
	issuer      string
	legacyReads int
}

func (k *nativeKMS) GetSecret(_ context.Context, ref string) ([]byte, error) {
	switch ref {
	case TokenRef:
		k.legacyReads++
		return []byte("legacy-token"), nil
	case HostKeyRef:
		return []byte("forge.test ssh-ed25519 AAAAPINNED"), nil
	default:
		return nil, errors.New("not found")
	}
}

func TestSourceNativeIAMUsesExpiringBearerAndCachesOnlyInsideLifetime(t *testing.T) {
	t.Setenv("CLOUD_FORGE_AUTH_MODE", "iam")
	var issuer string
	var minted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/identity-authority":
			_ = json.NewEncoder(w).Encode(map[string]any{"authority": "hanzo-iam", "issuer": issuer, "machineAPI": true, "credentialRole": "machine"})
		case "/v1/iam/oauth/token":

			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || r.Form.Get("assertion") != "projected.assertion" {
				http.Error(w, "bad client", 401)
				return
			}
			minted.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "eyTest.e30.signature", "token_type": "Bearer", "expires_in": 120})
		case "/v1/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"is_admin": true})
		case "/v1/repos/hanzoai/test":
			if r.Header.Get("Authorization") != "Bearer eyTest.e30.signature" {
				http.Error(w, "not a native bearer", 401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "test", "full_name": "hanzoai/test"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	nativeWorkload(t, issuer)
	kms := &nativeKMS{issuer: issuer}
	source := &Source{Host: server.URL}
	for range 2 {
		c, err := source.Client(t.Context(), kms, "api.hanzo.ai")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = c.Machine().Repo(t.Context(), "hanzoai", "test"); err != nil {
			t.Fatal(err)
		}
	}
	if minted.Load() != 1 || kms.legacyReads != 0 || source.window > 90*time.Second || !source.iamSeen {
		t.Fatalf("minted=%d legacy=%d ttl=%s native=%v", minted.Load(), kms.legacyReads, source.window, source.iamSeen)
	}
}

func TestSourceCutoverNeverDowngradesAfterIAMAndStrictModeNeverReadsLegacy(t *testing.T) {
	var ready atomic.Bool
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/identity-authority" {
			if !ready.Load() {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"authority": "hanzo-iam", "issuer": issuer, "machineAPI": true, "credentialRole": "machine"})
			return
		}
		if r.URL.Path == "/v1/iam/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "eyTest.e30.signature", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if r.URL.Path == "/v1/user" {
			_ = json.NewEncoder(w).Encode(map[string]any{"is_admin": true})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	issuer = server.URL
	nativeWorkload(t, issuer)
	kms := &nativeKMS{issuer: issuer}
	source := &Source{Host: server.URL}
	t.Setenv("CLOUD_FORGE_AUTH_MODE", "auto")
	if _, err := source.Client(t.Context(), kms, "api.hanzo.ai"); err != nil {
		t.Fatal(err)
	}
	if kms.legacyReads != 1 || source.window > 5*time.Second {
		t.Fatal("pre-cutover legacy was not bounded")
	}
	ready.Store(true)
	source.when = time.Now().Add(-fresh)
	if _, err := source.Client(t.Context(), kms, "api.hanzo.ai"); err != nil {
		t.Fatal(err)
	}
	ready.Store(false)
	source.when = time.Now().Add(-fresh)
	if _, err := source.Client(t.Context(), kms, "api.hanzo.ai"); err == nil {
		t.Fatal("a native source downgraded")
	}
	t.Setenv("CLOUD_FORGE_AUTH_MODE", "iam")
	if _, err := (&Source{Host: server.URL}).Client(t.Context(), kms, "api.hanzo.ai"); err == nil {
		t.Fatal("strict mode accepted a legacy authority")
	}
	if kms.legacyReads != 1 {
		t.Fatalf("legacy was read after native cutover: %d", kms.legacyReads)
	}
}

func TestSourceIAMRefusesMismatchedAuthorityAndNeverLogsSecretResponses(t *testing.T) {
	t.Setenv("CLOUD_FORGE_AUTH_MODE", "iam")
	var issuer string
	var wrong atomic.Bool
	wrong.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/identity-authority" {
			iss := issuer
			if wrong.Load() {
				iss = "https://elsewhere.example"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"authority": "hanzo-iam", "issuer": iss, "machineAPI": true, "credentialRole": "machine"})
			return
		}
		http.Error(w, "secret-value-never-logged", 401)
	}))
	defer server.Close()
	issuer = server.URL
	nativeWorkload(t, issuer)
	kms := &nativeKMS{issuer: issuer}
	for _, mismatch := range []bool{true, false} {
		wrong.Store(mismatch)
		_, err := (&Source{Host: server.URL}).Client(t.Context(), kms, "api.hanzo.ai")
		if err == nil || strings.Contains(err.Error(), "secret-value-never-logged") {
			t.Fatalf("unavailable authority was accepted or leaked credentials: %v", err)
		}
	}
	if kms.legacyReads != 0 {
		t.Fatal("refusal fell back to legacy")
	}
}

func nativeWorkload(t *testing.T, issuer string) {
	t.Helper()
	path := t.TempDir() + "/token"
	if err := os.WriteFile(path, []byte("projected.assertion"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HANZO_SA_TOKEN", path)
	t.Setenv("HANZO_IAM_URL", issuer)
	t.Setenv("HANZO_IAM_ISSUER", issuer)
}
