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

func TestClient_GetURLMapping(t *testing.T) {
	d := &captureDoer{respBody: `{"secret":{"value":"v"}}`}
	c := newKMSClient("http://kms.hanzo.svc", d)
	_, err := c.getSecret(context.Background(), "tok", "hanzo", "admin-guard-secrets", "prod", "GUARD_HMAC_KEY")
	if err != nil {
		t.Fatalf("getSecret: %v", err)
	}
	wantURL := "http://kms.hanzo.svc/v1/kms/orgs/hanzo/secrets/admin-guard-secrets/GUARD_HMAC_KEY?env=prod"
	if got := d.last.URL.String(); got != wantURL {
		t.Errorf("GET url=\n  %q\nwant\n  %q", got, wantURL)
	}
	if got := d.last.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization=%q, want Bearer tok", got)
	}
}

func TestClient_PutBodyMapping(t *testing.T) {
	d := &captureDoer{status: 200, respBody: `{"stored":true}`}
	c := newKMSClient("http://cloud.hanzo.svc", d)
	if err := c.putSecret(context.Background(), "tok", "hanzo", "admin-guard-secrets", "prod", "GUARD_HMAC_KEY", []byte("s3cr3t")); err != nil {
		t.Fatalf("putSecret: %v", err)
	}
	wantURL := "http://cloud.hanzo.svc/v1/kms/orgs/hanzo/secrets"
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
	c := newKMSClient("http://x", d)
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
