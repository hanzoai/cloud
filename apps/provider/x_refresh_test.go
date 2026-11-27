package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// An X connection outlives its first access token: a token about to expire is
// traded for a new one with the refresh token, the pair is sealed again, and
// the connection's expiry moves on — so an automation posts past the second hour.
func TestAnXConnectionRefreshesItsToken(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("X_CLIENT_ID", "app")
		t.Setenv("X_CLIENT_SECRET", "secret")
		asked := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "r1" || r.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			asked++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"a2","refresh_token":"r2","expires_in":7200,"scope":"tweet.read tweet.write users.read offline.access"}`))
		}))
		t.Cleanup(srv.Close)
		orig := xTokenURL
		xTokenURL = srv.URL
		t.Cleanup(func() { xTokenURL = orig })

		kc := newKMS(t)
		newApp(t, kc)
		ctx := context.Background()
		s := mounted
		path := kmsPath("acme", xProvider)
		for name, v := range map[string]string{accessSecret: "a1", refreshSecret: "r1"} {
			if err := kmsPut(s, path, name, []byte(v)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.State.store.Upsert(ctx, Connection{Org: "acme", Provider: xProvider, ExternalID: "u1", AccountLabel: "acme", ExpiresAt: time.Now().Add(-time.Minute).Unix()}); err != nil {
			t.Fatal(err)
		}
		tok, err := TokenFor(ctx, "acme", xProvider, accessSecret)
		if err != nil || string(tok) != "a2" || asked != 1 {
			t.Fatalf("token = %q %v, asked %d", tok, err, asked)
		}
		if ref, _ := kmsGet(s, path, refreshSecret); string(ref) != "r2" {
			t.Fatalf("refresh token sealed = %q", ref)
		}
		// Fresh now: read, not traded again.
		if tok, err := TokenFor(ctx, "acme", xProvider, accessSecret); err != nil || string(tok) != "a2" || asked != 1 {
			t.Fatalf("second read = %q %v, asked %d", tok, err, asked)
		}
	})
}
