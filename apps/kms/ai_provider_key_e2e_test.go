// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package kms_test

import (
	"os"
	"testing"

	aiobject "github.com/hanzoai/ai/object"
)

// TestAIProviderKeySealsAndResolvesEndToEnd is the whole feature in one test: an
// operator pastes a provider key at admin.hanzo.ai, it is sealed into the
// EMBEDDED KMS, and the AI runtime resolves it back on the completion path.
//
// It runs HERE rather than in hanzoai/ai because only this side can build the
// real in-process client; ai declares the store structurally (object.SecretStore)
// precisely so it needs no KMS dependency, which means ai's own tests can only
// use a fake. A fake proves the logic and nothing about the coordinates.
//
// The coordinates are the part worth proving. A bare ref resolves to path "/",
// which fileOrg treats as the FACADE and lands in the deployment's system
// partition — while the REST surface folds the caller's org and lands the same
// name at /orgs/{org}. Those are different databases. Write through one door and
// read through the other and the secret is simply not there: no error, no
// warning, just a key the gateway cannot find. This asserts the seal and the
// resolve use the SAME door.
func TestAIProviderKeySealsAndResolvesEndToEnd(t *testing.T) {
	cfg := baseCfg(t, masterKeyB64(t))
	_, deps := newApp(t, cfg)
	if deps.KMS == nil {
		t.Fatal("deps.KMS is nil; expected the in-process client")
	}

	// Exactly what ai.Mount does when cloud mounts it.
	aiobject.SetSecretStore(deps.KMS)
	t.Cleanup(func() { aiobject.SetSecretStore(nil) })

	const secretName = "OPENROUTER_API_KEY"
	const key = "sk-or-v1-test-value-not-a-real-key"

	// 1. The admin paste: seal the raw key, get back the reference the row holds.
	ref, err := aiobject.StoreProviderSecret(secretName, key)
	if err != nil {
		t.Fatalf("StoreProviderSecret: %v", err)
	}
	if want := "kms://" + secretName; ref != want {
		t.Fatalf("ref = %q, want %q", ref, want)
	}

	// 2. The completion path: resolve that reference back to the key.
	p := &aiobject.Provider{Name: "openrouter", Category: "Model", ClientSecret: ref}
	if err := aiobject.ResolveProviderSecret(p); err != nil {
		t.Fatalf("ResolveProviderSecret: %v", err)
	}
	if p.ClientSecret != key {
		t.Fatalf("resolved %q, want the sealed key", p.ClientSecret)
	}

	// 3. The admin view agrees with the completion path — keyPresent cannot claim a
	//    key the gateway would fail to find, because both read the same client.
	if !aiobject.ProviderKeyPresent(&aiobject.Provider{Name: "openrouter", ClientSecret: ref}) {
		t.Error("ProviderKeyPresent = false for a key that just sealed and resolved")
	}
}

// TestAIProviderKeyPrefersKMSOverEnv pins the migration's direction. Every
// provider key today comes from an env var injected from a K8s Secret; moving one
// into KMS must actually change what serves, or the migration is a no-op that
// looks complete.
func TestAIProviderKeyPrefersKMSOverEnv(t *testing.T) {
	cfg := baseCfg(t, masterKeyB64(t))
	_, deps := newApp(t, cfg)

	aiobject.SetSecretStore(deps.KMS)
	t.Cleanup(func() { aiobject.SetSecretStore(nil) })

	const secretName = "DO_AI_API_KEY_E2E"
	os.Setenv(secretName, "from-the-k8s-secret")
	t.Cleanup(func() { os.Unsetenv(secretName) })

	// Before the key migrates: the env var serves, and that is correct.
	p := &aiobject.Provider{Name: "do-ai", ClientSecret: "kms://" + secretName}
	if err := aiobject.ResolveProviderSecret(p); err != nil {
		t.Fatalf("pre-migration resolve: %v", err)
	}
	if p.ClientSecret != "from-the-k8s-secret" {
		t.Fatalf("pre-migration resolved %q, want the env value", p.ClientSecret)
	}

	// After it migrates: KMS wins.
	if _, err := aiobject.StoreProviderSecret(secretName, "from-kms"); err != nil {
		t.Fatalf("StoreProviderSecret: %v", err)
	}
	p = &aiobject.Provider{Name: "do-ai", ClientSecret: "kms://" + secretName}
	if err := aiobject.ResolveProviderSecret(p); err != nil {
		t.Fatalf("post-migration resolve: %v", err)
	}
	if p.ClientSecret != "from-kms" {
		t.Fatalf("post-migration resolved %q, want the KMS value", p.ClientSecret)
	}
}
