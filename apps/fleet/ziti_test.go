// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package fleet

import (
	"errors"
	"testing"
)

// A ".ziti" apiserver is a fabric service name, not an address: the guard
// accepts it without resolving it, and SafeRESTConfig hands exactly those hosts
// the fabric dialer. Everything else keeps the ordinary dial and the ordinary
// guard — a private IP is still refused, so the fabric suffix is not a way past
// the SSRF gate for anything that is not actually on the fabric.
func TestSafeRESTConfig_FabricHostDialsTheFabricAndOnlyIt(t *testing.T) {
	cfg, err := SafeRESTConfig([]byte(kc("https://k3s.acme.ziti:6443", tokenUser)))
	if err != nil {
		t.Fatalf("a .ziti apiserver must be accepted without resolving, got %v", err)
	}
	if cfg.Dial == nil {
		t.Fatal("a .ziti apiserver must be dialed through the fabric, not TCP")
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
	if _, err := SafeRESTConfig([]byte(kc("https://ziti.example.invalid:6443", tokenUser))); err == nil {
		t.Fatal("a non-fabric host must still go through the resolving guard")
	}
	// And the fabric does not waive the credential-plugin rejection.
	if _, err := SafeRESTConfig([]byte(kc("https://k3s.acme.ziti:6443", execUser))); err == nil {
		t.Fatal("a .ziti apiserver must not waive the exec-plugin rejection")
	}
}

// The fabric dialer needs the deployment's own enrolled identity; without it a
// dial says which configuration is missing instead of timing out into a TCP
// error about a name nothing resolves.
func TestFabricDialWithoutAnIdentitySaysSo(t *testing.T) {
	t.Setenv(ztIdentityEnv, "")
	if _, err := fabricDial(t.Context(), "tcp", "k3s.acme.ziti:6443"); err == nil {
		t.Fatalf("fabricDial with no %s must fail with the reason", ztIdentityEnv)
	}
}

// A disabled registry (no KMS) holds no clusters, and an unknown name in an
// enabled one answers the same sentinel — the absence a caller maps to 404.
func TestRESTForOrgClusterAbsenceIsTheSentinel(t *testing.T) {
	var r *Registry
	if _, err := r.RESTForOrgCluster("acme", "", "lab"); !errors.Is(err, ErrNoCluster) {
		t.Fatalf("nil registry: err = %v, want ErrNoCluster", err)
	}
	disabled := &Registry{}
	if _, err := disabled.RESTForOrgCluster("acme", "", "lab"); !errors.Is(err, ErrNoCluster) {
		t.Fatalf("disabled registry: err = %v, want ErrNoCluster", err)
	}
}
