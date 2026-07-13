package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/iam/oauth/device" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("client_id"); got != "hanzo-app" {
			t.Fatalf("client_id = %q", got)
		}
		if got := r.URL.Query().Get("response_type"); got != "device_code" {
			t.Fatalf("response_type = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "dc-1",
			"user_code":                 "WDJB-MJHT",
			"verification_uri":          srv0 + "/login/oauth/device",
			"verification_uri_complete": srv0 + "/login/oauth/device/WDJB-MJHT",
			"expires_in":                900,
			"interval":                  1,
		})
	}))
	defer srv.Close()
	srv0 = srv.URL

	iam := newIAMClient(srv.URL, "hanzo-app")
	da, err := iam.deviceAuth(context.Background(), "openid profile email")
	if err != nil {
		t.Fatal(err)
	}
	if da.DeviceCode != "dc-1" || da.UserCode != "WDJB-MJHT" {
		t.Fatalf("unexpected device auth: %+v", da)
	}
	if !strings.HasSuffix(da.VerificationURIComplete, "/WDJB-MJHT") {
		t.Fatalf("verification_uri_complete = %q", da.VerificationURIComplete)
	}
}

// srv0 lets the handler reference its own server URL in the response body.
var srv0 string

func TestPollDeviceTokenPendingThenIssued(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/iam/oauth/token" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = r.ParseForm()
		if got := r.Form.Get("grant_type"); got != deviceGrantType {
			t.Fatalf("grant_type = %q", got)
		}
		if got := r.Form.Get("device_code"); got != "dc-1" {
			t.Fatalf("device_code = %q", got)
		}
		polls++
		if polls < 3 {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-1", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer srv.Close()

	iam := newIAMClient(srv.URL, "hanzo-console")
	tr, err := iam.pollDeviceToken(context.Background(), &deviceAuthResp{DeviceCode: "dc-1", ExpiresIn: 30, Interval: 0})
	if err != nil {
		t.Fatal(err)
	}
	if tr.AccessToken != "tok-1" || polls != 3 {
		t.Fatalf("token = %q polls = %d", tr.AccessToken, polls)
	}
}

func TestPollDeviceTokenExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "expired_token"})
	}))
	defer srv.Close()

	iam := newIAMClient(srv.URL, "hanzo-console")
	_, err := iam.pollDeviceToken(context.Background(), &deviceAuthResp{DeviceCode: "dc-1", ExpiresIn: 30, Interval: 0})
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v", err)
	}
}

func TestPollDeviceTokenDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "access_denied", "error_description": "user refused"})
	}))
	defer srv.Close()

	iam := newIAMClient(srv.URL, "hanzo-console")
	_, err := iam.pollDeviceToken(context.Background(), &deviceAuthResp{DeviceCode: "dc-1", ExpiresIn: 30, Interval: 0})
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v", err)
	}
}
