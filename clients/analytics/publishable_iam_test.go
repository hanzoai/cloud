// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package analytics

// The IAM-issued write-only publishable key (pk-) on the canonical door, and the
// retirement of cloud's self-mint. The pk- resolves to an org through the ONE key
// seam (the injectable resolveKeyOrg, so no IAM is needed here); the observable proxy
// for "resolved to a tenant" is "passed the tenant gate" — 503 (datastore down), not
// 403. The read-gate itself (a pk- can never READ) is proven in the parent package
// (middleware_identity_publishable_test.go): here we prove the WRITE door admits it.

import (
	"net/http"
	"strings"
	"testing"
)

// TestEvent_IAMPublishableKeyAdmitted: an IAM write-only pk- presented as a bearer on
// /v1/event resolves through the ONE key seam and is ADMITTED (503, datastore down) —
// a first-class ingest auth mode alongside the interim pk_. The resolver is handed the
// exact key, and org is the RESOLVED tenant (never body/header).
func TestEvent_IAMPublishableKeyAdmitted(t *testing.T) {
	app := mountApp(t)
	got := stubResolver(t, func(string) (string, bool) { return "acme", true })
	code := postKeyed(t, app, "/v1/event", "hanzo.ai",
		`{"batch":[{"type":"event","event":"signup_completed"}],"org":"attacker"}`,
		map[string]string{"Authorization": "Bearer pk-live-abc", "X-Org-Id": "attacker"})
	if *got != "pk-live-abc" {
		t.Fatalf("resolver handed key %q, want pk-live-abc", *got)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("pk- on /v1/event want 503 (admitted as resolved org, datastore down), got %d", code)
	}
}

// TestEvent_IAMPublishableKeyViaIngestHeaders: the pk- is accepted in the SAME
// browser-friendly slots as the interim pk_ — the x-hanzo-ingest-key header and the
// ?ingest_key= query (sendBeacon can't set headers) — not only the bearer.
func TestEvent_IAMPublishableKeyViaIngestHeaders(t *testing.T) {
	app := mountApp(t)
	t.Run("x-hanzo-ingest-key", func(t *testing.T) {
		got := stubResolver(t, func(string) (string, bool) { return "acme", true })
		code := postKeyed(t, app, "/v1/event", "", `{"batch":[{"type":"pageview"}]}`,
			map[string]string{"x-hanzo-ingest-key": "pk-live-hdr"})
		if *got != "pk-live-hdr" || code != http.StatusServiceUnavailable {
			t.Fatalf("x-hanzo-ingest-key pk- key=%q code=%d, want pk-live-hdr / 503", *got, code)
		}
	})
	t.Run("ingest_key query", func(t *testing.T) {
		got := stubResolver(t, func(string) (string, bool) { return "acme", true })
		code := postKeyed(t, app, "/v1/event?ingest_key=pk-live-q", "", `{"batch":[{"type":"pageview"}]}`, nil)
		if *got != "pk-live-q" || code != http.StatusServiceUnavailable {
			t.Fatalf("?ingest_key pk- key=%q code=%d, want pk-live-q / 503", *got, code)
		}
	})
}

// TestEvent_IAMPublishableKeyUnresolvableFailsClosed: a pk- IAM refuses to resolve
// (unknown, or not scope=publish) is refused 403 on the canonical door — no brand-host
// escape, exactly like an unverifiable pk_.
func TestEvent_IAMPublishableKeyUnresolvableFailsClosed(t *testing.T) {
	app := mountApp(t)
	stubResolver(t, func(string) (string, bool) { return "", false }) // nothing resolves
	code := postKeyed(t, app, "/v1/event", "hanzo.ai", `{"batch":[{"type":"pageview"}]}`,
		map[string]string{"Authorization": "Bearer pk-live-nope"})
	if code != http.StatusForbidden {
		t.Fatalf("unresolvable pk- on /v1/event want 403 (fail closed, no brand-host escape), got %d", code)
	}
}

// TestEvent_InterimPkStillHMACVerified: the interim pk_ (underscore) still verifies via
// HMAC with NO IAM hop — the resolver is NEVER consulted for it, proving the two
// generations dispatch by prefix and the compat path is untouched.
func TestEvent_InterimPkStillHMACVerified(t *testing.T) {
	t.Setenv(ingestKeySecretEnv, testSecret)
	app := mountApp(t)
	stubResolver(t, func(key string) (string, bool) {
		t.Fatalf("resolver must NOT be consulted for an interim pk_ (HMAC-verified), got key=%q", key)
		return "", false
	})
	key, _ := mintPublishableKey(testSecret, "acme")
	code := postKeyed(t, app, "/v1/event", "", `{"batch":[{"type":"pageview"}]}`,
		map[string]string{"Authorization": "Bearer " + key})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("interim pk_ on /v1/event want 503 (HMAC-admitted, datastore down), got %d", code)
	}
}

// TestMintKey_DeprecatedToIAM: POST /v1/ingest/keys no longer self-mints — cloud is
// not a key authority. A validated principal gets 410 Gone (issuance moved to IAM);
// an anonymous caller still 403s, so the deprecation notice never leaks.
func TestMintKey_DeprecatedToIAM(t *testing.T) {
	t.Setenv(ingestKeySecretEnv, testSecret) // even WITH the old secret set, no self-mint
	app := mountApp(t)

	// anonymous → 403 (unchanged; never leaks the deprecation notice)
	if code := postKeyed(t, app, "/v1/ingest/keys", "", ``, nil); code != http.StatusForbidden {
		t.Fatalf("anonymous mint want 403, got %d", code)
	}
	// validated principal → 410 Gone (issuance moved to IAM), and NOT a minted key.
	code, body := doBody(t, app, http.MethodPost, "/v1/ingest/keys", "user-x", "acme", ``)
	if code != http.StatusGone {
		t.Fatalf("validated mint want 410 Gone (moved to IAM), got %d", code)
	}
	// A guard that the deprecated endpoint returns a NOTICE, not a credential.
	if s := string(body); strings.Contains(s, `"key":"pk_`) || strings.Contains(s, `"key":"pk-`) {
		t.Fatalf("deprecated mint must NOT return a self-minted key, got body %s", body)
	}
}
