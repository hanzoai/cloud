// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package fleet

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fullsailor/pkcs7"
	"golang.org/x/oauth2"
)

// A ".zt" apiserver is a fabric service name, not an address: the guard
// accepts it without resolving it, and SafeRESTConfig hands exactly those hosts
// the fabric dialer. Everything else keeps the ordinary dial and the ordinary
// guard — a private IP is still refused, so the fabric suffix is not a way past
// the SSRF gate for anything that is not actually on the fabric.
func TestSafeRESTConfig_FabricHostDialsTheFabricAndOnlyIt(t *testing.T) {
	cfg, err := SafeRESTConfig([]byte(kc("https://k3s.acme.zt:6443", tokenUser)))
	if err != nil {
		t.Fatalf("a .zt apiserver must be accepted without resolving, got %v", err)
	}
	if cfg.Dial == nil {
		t.Fatal("a .zt apiserver must be dialed through the fabric, not TCP")
	}

	pub, err := SafeRESTConfig([]byte(kc("https://8.8.8.8:6443", tokenUser)))
	if err != nil {
		t.Fatalf("public https apiserver: %v", err)
	}
	if pub.Dial != nil {
		t.Fatal("a public apiserver must keep client-go's own dial")
	}

	if _, err := SafeRESTConfig([]byte(kc("https://10.0.0.5:6443", tokenUser))); err == nil {
		t.Fatal("a private apiserver must still be refused")
	}
	// The suffix marks the fabric's namespace, not a spelling trick: a name that
	// merely contains it somewhere is an ordinary host and resolves (here, fails
	// to) like one.
	if _, err := SafeRESTConfig([]byte(kc("https://zt.example.invalid:6443", tokenUser))); err == nil {
		t.Fatal("a non-fabric host must still go through the resolving guard")
	}
	// And the fabric does not waive the credential-plugin rejection.
	if _, err := SafeRESTConfig([]byte(kc("https://k3s.acme.zt:6443", execUser))); err == nil {
		t.Fatal("a .zt apiserver must not waive the exec-plugin rejection")
	}
}

// The fabric dialer needs the deployment's own IAM identity; without it a dial
// says which configuration is missing instead of timing out into a TCP error
// about a name nothing resolves.
func TestFabricDialWithoutAnIdentitySaysSo(t *testing.T) {
	t.Setenv("IAM_CLIENT_ID", "")
	t.Setenv("IAM_CLIENT_SECRET", "")
	if _, err := fabricDial(t.Context(), "tcp", "k3s.acme.zt:6443"); err == nil {
		t.Fatal("fabricDial with no IAM identity must fail with the reason")
	}
}

// rotating is a machine token source whose token the test moves on.
type rotating struct {
	mu sync.Mutex
	n  int
}

func (r *rotating) Token() (*oauth2.Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &oauth2.Token{AccessToken: fmt.Sprintf("iam-%d", r.n), Expiry: time.Now().Add(time.Hour)}, nil
}

func (r *rotating) next() {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
}

// fakeController admits exactly one thing: an ext-jwt login bearing the
// machine's current token. It answers the two unauthenticated reads a client
// makes first — its CAs, and a version offering OIDC, so a client that would
// take that flow does — and refuses everything else.
type fakeController struct {
	src *rotating
	ca  []byte // the degenerate PKCS#7 of the CA it publishes

	mu      sync.Mutex
	logins  []string
	refused []string
}

func (f *fakeController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	want, _ := f.src.Token()
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/.well-known/est/cacerts":
		w.Header().Set("Content-Type", "application/pkcs7-mime")
		_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(f.ca)))
	case r.Method == http.MethodGet && r.URL.Path == "/edge/client/v1/version":
		_, _ = w.Write([]byte(`{"data":{"version":"v1","capabilities":["OIDC_AUTH"]},"meta":{}}`))
	case r.Method == http.MethodPost && r.URL.Path == "/edge/client/v1/authenticate" &&
		r.URL.Query().Get("method") == "ext-jwt" && r.Header.Get("Authorization") == "Bearer "+want.AccessToken:
		f.logins = append(f.logins, want.AccessToken)
		_, _ = fmt.Fprintf(w, `{"data":{"id":"s-%d","token":"sess-%d","identity":{"id":"i-cloud","name":"admin/hanzo-cloud"},"expiresAt":%q,"expirationSeconds":1800},"meta":{}}`,
			len(f.logins), len(f.logins), time.Now().Add(30*time.Minute).Format(time.RFC3339))
	default:
		f.refused = append(f.refused, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"not ext-jwt"}}`))
	}
}

// The cloud is on the fabric as its own IAM subject: it trusts the CAs the
// controller publishes, logs in by ext-jwt with the machine token, and a later
// login presents the token the source holds THEN, never the first one.
func TestFabricLogsInByExtJwtWithTheMachineToken(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fabric-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	p7, err := pkcs7.DegenerateCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	src := &rotating{}
	f := &fakeController{src: src, ca: p7}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())

	ztx, err := open(srv.URL, src, roots)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(ztx.Close)

	if _, err := ca.Verify(x509.VerifyOptions{Roots: ztx.GetCredentials().GetCaPool()}); err != nil {
		t.Fatalf("the published CA must be trusted: %v", err)
	}
	if _, err := ca.Verify(x509.VerifyOptions{Roots: roots}); err == nil {
		t.Fatal("the published CA must come from the controller, not the roots")
	}

	// The token moves on after the context opened, and the login presents the
	// one the source holds when it happens. The fake admits nothing after the
	// login, so Authenticate ends in an error about a later step.
	src.next()
	_ = ztx.Authenticate()

	f.mu.Lock()
	defer f.mu.Unlock()
	if want := []string{"iam-1"}; !slices.Equal(f.logins, want) {
		t.Fatalf("logins = %v, want %v (refused: %v)", f.logins, want, f.refused)
	}
	for _, r := range f.refused {
		if strings.Contains(r, "/authenticate") || strings.Contains(r, "/oidc") {
			t.Fatalf("the client tried a way in other than ext-jwt: %s", r)
		}
	}
}

// A disabled registry (no KMS) holds no clusters, and an unknown name in an
// enabled one answers the same sentinel — the absence a caller maps to 404.
func TestRESTForOrgClusterAbsenceIsTheSentinel(t *testing.T) {
	var r *Registry
	if _, err := r.RESTForOrgCluster(context.Background(), "acme", "", "lab"); !errors.Is(err, ErrNoCluster) {
		t.Fatalf("nil registry: err = %v, want ErrNoCluster", err)
	}
	disabled := &Registry{}
	if _, err := disabled.RESTForOrgCluster(context.Background(), "acme", "", "lab"); !errors.Is(err, ErrNoCluster) {
		t.Fatalf("disabled registry: err = %v, want ErrNoCluster", err)
	}
}
