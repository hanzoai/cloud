package account

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/zap-proto/zip"
)

// Cloud's own credential never writes IAM for a support session: a key mint, an
// org, a membership or a profile write stops before IAM is asked, while the
// session's reads and every write the person makes themselves go through.
func TestASupportSessionWritesNothingToIAM(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{}}`))
	}))
	t.Cleanup(srv.Close)
	c := &iamClient{base: srv.URL, clientID: "c", clientSecret: "s", http: srv.Client()}
	support := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-ada", ActedBy: "u-op"})
	own := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u-ada"})

	for _, w := range []struct{ method, path string }{
		{http.MethodPost, "/v1/iam/keys"},
		{http.MethodPut, "/v1/iam/keys/acme/ada-secret-1"},
		{http.MethodPost, "/v1/iam/organizations"},
		{http.MethodPost, "/v1/iam/memberships"},
		{http.MethodPost, "/v1/iam/delete-membership"},
		{http.MethodPost, "/v1/iam/applications"},
		{http.MethodPut, "/v1/iam/users/acme/ada"},
	} {
		if _, err := c.do(support, w.method, w.path, nil, []byte(`{}`)); !errors.Is(err, errSupport) {
			t.Errorf("support session %s %s: %v, want errSupport", w.method, w.path, err)
		}
		if _, err := c.do(own, w.method, w.path, nil, []byte(`{}`)); err != nil {
			t.Errorf("the person's own %s %s: %v", w.method, w.path, err)
		}
	}
	if _, err := c.do(support, http.MethodGet, "/v1/iam/keys", nil, nil); err != nil {
		t.Errorf("support session read: %v", err)
	}
	if n := asked.Load(); n != 8 {
		t.Fatalf("IAM asked %d times, want 8: the person's seven writes and the session's one read", n)
	}
}
