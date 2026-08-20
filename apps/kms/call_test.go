// Copyright 2023-2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kms

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The credential is only safe while the caller cannot choose where it is sent.
// Every input here is a way of smuggling an authority into something that is
// supposed to be a path; each one must be refused before a request is built.
func TestPathCannotNameAnotherHost(t *testing.T) {
	for _, p := range []string{
		"//evil.example.com/v2/droplets", // scheme-relative: resolves to another authority
		"https://evil.example.com/steal", // an absolute URL, not a path
		"http://evil.example.com/steal",
		"/v2/droplets\r\nHost: evil.example.com", // header injection via CRLF
		"/v2/droplets\x00",                       // NUL
		"v2/droplets",                            // not absolute; would append to the host oddly
		"",                                       // nothing at all
	} {
		if reachable(p) {
			t.Errorf("accepted a path that can name another host: %q", p)
		}
	}
	for _, p := range []string{"/", "/v2/droplets", "/v2/droplets?per_page=200"} {
		if !reachable(p) {
			t.Errorf("rejected an ordinary path: %q", p)
		}
	}
}

// A secret is callable only because its path names a vendor we know. Anything
// else must not reach the network at all.
func TestOnlyKnownVendorsAreCallable(t *testing.T) {
	if _, ok := vendors[vendorOf("/orgs/hanzo/cloud/digitalocean")]; !ok {
		t.Error("digitalocean should be callable")
	}
	for _, p := range []string{
		"/orgs/hanzo/cloud/evilcorp",
		"/orgs/hanzo/cloud",
		"/orgs/hanzo/database/postgres",
	} {
		if _, ok := vendors[vendorOf(p)]; ok {
			t.Errorf("path %q resolved to a callable vendor", p)
		}
	}
}

// The host is the vendor's, never the caller's, and the credential rides in the
// header the vendor expects.
func TestComposeSendsCredentialOnlyToTheVendorHost(t *testing.T) {
	v := vendors["digitalocean"]
	r, err := compose(context.Background(), v, "s3cret", http.MethodGet, "/v2/droplets", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.URL.Host != "api.digitalocean.com" {
		t.Errorf("host = %q, want api.digitalocean.com", r.URL.Host)
	}
	if r.URL.Scheme != "https" {
		t.Errorf("scheme = %q, want https", r.URL.Scheme)
	}
	if got := r.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want the bearer credential", got)
	}
}

// A redirect can name a host this credential may not reach. Following it would
// carry the header there, so the hop is never taken.
func TestRedirectsAreNotFollowed(t *testing.T) {
	if out.CheckRedirect == nil {
		t.Fatal("no redirect policy: a 3xx would carry the credential to another host")
	}
	if err := out.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Errorf("redirect policy = %v, want ErrUseLastResponse", err)
	}
}

// Methods are an allowlist so adding one is a decision rather than an accident.
func TestVerbsAreAnAllowlist(t *testing.T) {
	for _, m := range []string{"TRACE", "CONNECT", "OPTIONS", "", "get "} {
		if verbs[m] {
			t.Errorf("verb %q should not be permitted", m)
		}
	}
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if !verbs[m] {
			t.Errorf("verb %q should be permitted", m)
		}
	}
}

// Whatever else changes, the vendor table must stay source. If it ever reads
// from a secret's own fields, a caller who can write secrets can redirect the
// credential — which is the one thing this design exists to prevent.
func TestVendorTableIsNotCallerControlled(t *testing.T) {
	for name, v := range vendors {
		if v.host == "" || strings.ContainsAny(v.host, "/:") {
			t.Errorf("vendor %q has a host that is not a bare hostname: %q", name, v.host)
		}
		if !strings.Contains(v.format, "%s") {
			t.Errorf("vendor %q cannot present a credential: format %q", name, v.format)
		}
	}
}
