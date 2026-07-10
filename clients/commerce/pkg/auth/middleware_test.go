// Copyright © 2026 Hanzo AI. MIT License.

package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireIdentity(t *testing.T) {
	cases := []struct {
		name      string
		require   bool
		headers   map[string]string
		wantCode  int
		wantOrg   string
		wantUser  string
		wantEmail string
		wantNext  bool
	}{
		{
			name:    "headers present, require true",
			require: true,
			headers: map[string]string{
				HeaderOrgID:     "hanzo",
				HeaderUserID:    "user-123",
				HeaderUserEmail: "z@hanzo.ai",
			},
			wantCode:  http.StatusOK,
			wantOrg:   "hanzo",
			wantUser:  "user-123",
			wantEmail: "z@hanzo.ai",
			wantNext:  true,
		},
		{
			name:     "missing headers, require true → 401",
			require:  true,
			headers:  nil,
			wantCode: http.StatusUnauthorized,
			wantNext: false,
		},
		{
			name:     "missing headers, require false → next with empty ctx",
			require:  false,
			headers:  nil,
			wantCode: http.StatusOK,
			wantNext: true,
		},
		{
			name:     "partial headers (only org), require true → allowed",
			require:  true,
			headers:  map[string]string{HeaderOrgID: "lux"},
			wantCode: http.StatusOK,
			wantOrg:  "lux",
			wantNext: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				called   bool
				gotOrg   string
				gotUser  string
				gotEmail string
			)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				gotOrg = OrgID(r.Context())
				gotUser = UserID(r.Context())
				gotEmail = UserEmail(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			handler := RequireIdentity(tc.require)(next)
			req := httptest.NewRequest(http.MethodGet, "/v1/commerce/health", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("status: want %d got %d (body=%q)", tc.wantCode, rec.Code, rec.Body.String())
			}
			if called != tc.wantNext {
				t.Fatalf("next called: want %v got %v", tc.wantNext, called)
			}
			if gotOrg != tc.wantOrg {
				t.Fatalf("org: want %q got %q", tc.wantOrg, gotOrg)
			}
			if gotUser != tc.wantUser {
				t.Fatalf("user: want %q got %q", tc.wantUser, gotUser)
			}
			if gotEmail != tc.wantEmail {
				t.Fatalf("email: want %q got %q", tc.wantEmail, gotEmail)
			}
		})
	}
}

func TestGinRequireIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Gin(true))
	r.GET("/x", func(c *gin.Context) {
		c.String(http.StatusOK, OrgID(c.Request.Context())+":"+UserID(c.Request.Context()))
	})

	t.Run("401 without identity", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("want 401 got %d", w.Code)
		}
	})

	t.Run("200 with identity", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set(HeaderOrgID, "hanzo")
		req.Header.Set(HeaderUserID, "u1")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200 got %d body=%q", w.Code, w.Body.String())
		}
		if w.Body.String() != "hanzo:u1" {
			t.Fatalf("body: want hanzo:u1 got %q", w.Body.String())
		}
	})
}

func TestAccessorsEmptyContext(t *testing.T) {
	ctx := context.Background()
	if OrgID(ctx) != "" || UserID(ctx) != "" || UserEmail(ctx) != "" {
		t.Fatalf("expected all-empty accessors on bare context")
	}
}
