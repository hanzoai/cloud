package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// captureDoer records the last request and returns a canned response, so the
// byte-exact URL/body mapping (the migration's highest-risk surface) is asserted
// without a live server.
type captureDoer struct {
	last     *http.Request
	lastBody string
	status   int
	respBody string
}

func (d *captureDoer) Do(r *http.Request) (*http.Response, error) {
	d.last = r
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		d.lastBody = string(b)
	}
	code := d.status
	if code == 0 {
		code = 200
	}
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(d.respBody)),
		Header:     http.Header{},
	}, nil
}

// The two faces spell the tenant differently, and a client that sends one face's
// grammar to the other reads nothing — so both are pinned byte-exact, together.
func TestClient_GetURLMapping(t *testing.T) {
	for _, tc := range []struct {
		face     string
		base     string
		route    route
		wantURL  string
		wantList string
	}{
		{
			face: "standalone", base: "http://kms.hanzo.svc", route: standalone,
			wantURL:  "http://kms.hanzo.svc/v1/kms/orgs/hanzo/secrets/admin-guard-secrets/GUARD_HMAC_KEY?env=prod",
			wantList: "http://kms.hanzo.svc/v1/kms/orgs/hanzo/secrets?env=prod&path=admin-guard-secrets",
		},
		{
			face: "cloud", base: "http://cloud.hanzo.svc", route: embedded,
			wantURL:  "http://cloud.hanzo.svc/v1/kms/secrets/admin-guard-secrets/GUARD_HMAC_KEY?env=prod",
			wantList: "http://cloud.hanzo.svc/v1/kms/secrets?env=prod&path=admin-guard-secrets",
		},
	} {
		d := &captureDoer{respBody: `{"secret":{"value":"v"},"names":["GUARD_HMAC_KEY"]}`}
		c := newKMSClient(tc.base, tc.route, d)
		if _, err := c.getSecret(context.Background(), "tok", "hanzo", "admin-guard-secrets", "prod", "GUARD_HMAC_KEY"); err != nil {
			t.Fatalf("%s getSecret: %v", tc.face, err)
		}
		if got := d.last.URL.String(); got != tc.wantURL {
			t.Errorf("%s GET url=\n  %q\nwant\n  %q", tc.face, got, tc.wantURL)
		}
		if got := d.last.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("%s Authorization=%q, want Bearer tok", tc.face, got)
		}
		// LIST resolves folder-sync CRs, so its URL and its {"names":[…]} envelope
		// (the shape both faces emit) are pinned on the same client.
		keys, err := c.listFolder(context.Background(), "tok", "hanzo", "admin-guard-secrets", "prod")
		if err != nil {
			t.Fatalf("%s listFolder: %v", tc.face, err)
		}
		if got := d.last.URL.String(); got != tc.wantList {
			t.Errorf("%s LIST url=\n  %q\nwant\n  %q", tc.face, got, tc.wantList)
		}
		if len(keys) != 1 || keys[0] != "GUARD_HMAC_KEY" {
			t.Errorf("%s listFolder keys=%v, want [GUARD_HMAC_KEY]", tc.face, keys)
		}
	}
}

func TestClient_PutBodyMapping(t *testing.T) {
	d := &captureDoer{status: 200, respBody: `{"stored":true}`}
	c := newKMSClient("http://cloud.hanzo.svc", embedded, d)
	if err := c.putSecret(context.Background(), "tok", "hanzo", "admin-guard-secrets", "prod", "GUARD_HMAC_KEY", []byte("s3cr3t")); err != nil {
		t.Fatalf("putSecret: %v", err)
	}
	wantURL := "http://cloud.hanzo.svc/v1/kms/secrets"
	if got := d.last.URL.String(); got != wantURL {
		t.Errorf("POST url=%q, want %q", got, wantURL)
	}
	// env is ALWAYS explicit (cloud refuses empty); value + name + path present.
	for _, want := range []string{`"env":"prod"`, `"name":"GUARD_HMAC_KEY"`, `"path":"admin-guard-secrets"`, `"value":"s3cr3t"`} {
		if !strings.Contains(d.lastBody, want) {
			t.Errorf("POST body %s missing %s", d.lastBody, want)
		}
	}
}

func TestClient_DecodesBothResponseShapes(t *testing.T) {
	// standalone: {"secret":{"value":...}}
	if v, err := decodeSecretValue([]byte(`{"secret":{"value":"abc"},"version":3}`)); err != nil || string(v) != "abc" {
		t.Errorf("standalone shape decode = %q,%v", v, err)
	}
	// cloud: {"value":...}
	if v, err := decodeSecretValue([]byte(`{"name":"K","env":"prod","value":"xyz"}`)); err != nil || string(v) != "xyz" {
		t.Errorf("cloud shape decode = %q,%v", v, err)
	}
}

func TestClient_NotFoundIsSentinel(t *testing.T) {
	d := &captureDoer{status: 404, respBody: `{"message":"not found"}`}
	c := newKMSClient("http://x", embedded, d)
	_, err := c.getSecret(context.Background(), "tok", "hanzo", "p", "prod", "K")
	if err != errSecretNotFound {
		t.Errorf("404 → err=%v, want errSecretNotFound", err)
	}
}

func TestClient_EscapesRestSegments(t *testing.T) {
	// A key that could smuggle a query char is percent-escaped per segment.
	if got := escapeRest(restOf("a/b", "K EY")); got != "a/b/K%20EY" {
		t.Errorf("escapeRest=%q, want a/b/K%%20EY", got)
	}
	if got := restOf("", "ROOTKEY"); got != "ROOTKEY" {
		t.Errorf("restOf root=%q, want ROOTKEY", got)
	}
}
