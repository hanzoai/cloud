// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/base/core"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// iam stands in for Hanzo IAM: it signs tokens and serves the keys a Base checks
// them against, at the address cloud derives from the issuer.
type iam struct {
	key    *rsa.PrivateKey
	issuer string
}

func newIAM(t *testing.T) *iam {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "iam-test", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}}
	// an address of its own: a JWKS document is cached by address, and a closed
	// test server's port is soon another's
	path := "/" + rand.Text()
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == path+cloud.WellKnown {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": ts.URL + path, "jwks_uri": ts.URL + path + "/jwks"})
			return
		}
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(ts.Close)
	return &iam{key: key, issuer: ts.URL + path}
}

func (i *iam) token(t *testing.T, sub, org, role string) string {
	t.Helper()
	jt := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": sub, "email": sub + "@test", "name": sub,
		"orgs": []any{map[string]any{"org": org, "role": role}},
		"iat":  time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	jt.Header["kid"] = "iam-test"
	s, err := jt.SignedString(i.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// serveBases mounts the subsystem against i and serves it on a loopback port.
func serveBases(t *testing.T, i *iam) string {
	t.Helper()
	t.Setenv("CLOUD_BASE_EMBED", "1")
	t.Setenv("BASE_API_PREFIX", apiPrefix)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Use(app, cloud.Deps{Discovery: i.issuer + cloud.WellKnown}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	mounted.pool.changes = nil
	return "http://" + listen(t, app)
}

// call is a request as the gateway forwards one: the validated principal in the
// identity headers, the caller's own bearer beside them.
func call(t *testing.T, method, url, org, token string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq, _ := http.NewRequest(method, url, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	if token != "" {
		rq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// sse reads a stream's events by name.
type sse struct {
	events chan [2]string // name, data
}

func openStream(t *testing.T, url string) (*sse, int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rq, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, resp.StatusCode
	}
	s := &sse{events: make(chan [2]string, 16)}
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var name, data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			case line == "":
				if name != "" {
					s.events <- [2]string{name, data}
				}
				name, data = "", ""
			}
		}
	}()
	return s, http.StatusOK
}

func (s *sse) next(t *testing.T, name string) string {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-s.events:
			if ev[0] == name {
				return ev[1]
			}
		case <-timeout:
			t.Fatalf("no %q event on the stream", name)
			return ""
		}
	}
}

// A browser opens an org's realtime stream with EventSource, which sends no
// header, so the gateway forwards no principal for it. The grant the org's Base
// minted names the org, and the stream is served by that Base: a record written
// there reaches the subscriber.
func TestARealtimeStreamReachesTheOrgsBase(t *testing.T) {
	i := newIAM(t)
	base := serveBases(t, i)
	token := i.token(t, "ann", "acme", "member")

	app, err := mounted.pool.appFor("acme")
	if err != nil {
		t.Fatal(err)
	}
	notes := core.NewBaseCollection("notes")
	member := `@request.auth.id != ""`
	notes.ListRule, notes.ViewRule = &member, &member
	notes.Fields.Add(&core.TextField{Name: "title"})
	if err := app.Save(notes); err != nil {
		t.Fatal(err)
	}

	code, body := call(t, http.MethodPost, base+"/v1/base/realtime/token", "acme", token, nil)
	if code != http.StatusOK {
		t.Fatalf("grant: %d %s", code, body)
	}
	var grant struct{ Token string }
	_ = json.Unmarshal(body, &grant)

	stream, code := openStream(t, base+"/v1/base/realtime?token="+grant.Token)
	if code != http.StatusOK {
		t.Fatalf("the stream did not open: %d", code)
	}
	var connect struct{ ClientId string }
	_ = json.Unmarshal([]byte(stream.next(t, "CONNECT")), &connect)

	code, body = call(t, http.MethodPost, base+"/v1/base/realtime", "acme", token,
		map[string]any{"clientId": connect.ClientId, "subscriptions": []string{"notes/*"}})
	if code != http.StatusNoContent {
		t.Fatalf("subscribe: %d %s", code, body)
	}

	r := core.NewRecord(notes)
	r.Set("title", "live")
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	ev := stream.next(t, "notes/*")
	if !strings.Contains(ev, `"action":"create"`) || !strings.Contains(ev, `"title":"live"`) {
		t.Fatalf("event: %s", ev)
	}

	// A grant opens one stream once.
	if _, code := openStream(t, base+"/v1/base/realtime?token="+grant.Token); code == http.StatusOK {
		t.Fatal("a spent grant opened a second stream")
	}
	// No grant and no principal is no stream at all.
	if _, code := openStream(t, base+"/v1/base/realtime"); code == http.StatusOK {
		t.Fatal("a stream opened with nothing to say whose Base it reads")
	}
}

// A token acting in another org is refused by the org's Base itself, whatever
// routed the request to it.
func TestAnOrgsBaseRefusesAnotherOrgsToken(t *testing.T) {
	i := newIAM(t)
	base := serveBases(t, i)
	zoo := i.token(t, "zed", "zoo", "owner")

	code, body := call(t, http.MethodGet, base+"/v1/base/collections/users/records", "acme", zoo, nil)
	if code != http.StatusForbidden {
		t.Fatalf("acme's Base answered zoo's token with %d %s", code, body)
	}
}

// listen serves app on a loopback port and returns its address once it accepts.
// zip runs on fasthttp, which httptest.Server cannot serve.
func listen(t *testing.T, app *zip.App) string {
	t.Helper()
	f := app.Fiber()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = f.Listener(ln) }()
	t.Cleanup(func() { _ = f.ShutdownWithTimeout(2 * time.Second) })

	addr := ln.Addr().String()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return addr
}
