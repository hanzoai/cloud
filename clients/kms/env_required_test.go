package kms_test

// Regression coverage for the one-way env contract on the embedded KMS write
// surface. env is a first-class component of the storage key
// (kms/secrets/{path}/{env}/{name}); a POST that omits env must fail loud (400)
// rather than silently landing in a "default" bucket that project/env/path
// readers (the kms-operator, cluster syncs) never resolve. That split is what
// let an IAM z-password land in env=default while prod kept serving the stale
// value.
//
// Secret values are never printed — round-trip fidelity is asserted by
// comparing SHA-256 digests.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// A write that omits env fails loud (400) and lands nowhere.
func TestRESTPutSecret_RequiresEnv(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))

	body, _ := json.Marshal(map[string]string{
		"name": "Z_PASSWORD", "path": "iam-passwords", "value": "irrelevant",
	})
	resp := do(t, app, "POST", "/v1/kms/orgs/hanzo/secrets", "hanzo", string(body), false, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("omitted-env write = %d, want 400", resp.StatusCode)
	}
	if b := readAll(resp.Body); !strings.Contains(b, "'env' is required") {
		t.Fatalf("400 body should name env, got %q", b)
	}

	// The rejected write must not have populated the default bucket.
	g := do(t, app, "GET", "/v1/kms/orgs/hanzo/secrets/iam-passwords/Z_PASSWORD?env=default", "hanzo", "", false, nil)
	if g.StatusCode != http.StatusNotFound {
		t.Fatalf("omitted-env write must not land in env=default: GET = %d, want 404", g.StatusCode)
	}
}

// A write with an explicit env is readable through the exact project/env/path
// resolution the kms-operator uses, and is not visible in any other bucket.
func TestRESTPutSecret_EnvProd_ProjectEnvPath(t *testing.T) {
	app, _ := newApp(t, baseCfg(t, masterKeyB64(t)))

	const secret = "z-password-98f3-do-not-log"
	want := sha256hex(secret)

	body, _ := json.Marshal(map[string]string{
		"name": "Z_PASSWORD", "path": "iam-passwords", "env": "prod", "value": secret,
	})
	resp := do(t, app, "POST", "/v1/kms/orgs/hanzo/secrets", "hanzo", string(body), false, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("explicit-env write = %d, want 200: %s", resp.StatusCode, readAll(resp.Body))
	}

	// Operator resolution: org=hanzo (project), env=prod, path=/iam-passwords.
	g := do(t, app, "GET", "/v1/kms/orgs/hanzo/secrets/iam-passwords/Z_PASSWORD?env=prod", "hanzo", "", false, nil)
	if g.StatusCode != http.StatusOK {
		t.Fatalf("project/env/path read = %d, want 200", g.StatusCode)
	}
	v, _ := decode(t, g.Body)["value"].(string)
	if h := sha256hex(v); h != want {
		t.Fatalf("round-trip value digest mismatch: got %s want %s", h, want)
	}

	// No cross-bucket bleed: env=default must not resolve the prod write.
	gd := do(t, app, "GET", "/v1/kms/orgs/hanzo/secrets/iam-passwords/Z_PASSWORD?env=default", "hanzo", "", false, nil)
	if gd.StatusCode != http.StatusNotFound {
		t.Fatalf("prod write must not be visible in env=default: GET = %d, want 404", gd.StatusCode)
	}
}
