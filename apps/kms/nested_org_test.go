package kms

// A write's path is beneath the caller's own org root, which is already
// `/orgs/<org>`. A path that starts with `/orgs/` was stored one level down —
// `--path /orgs/hanzo` under a credential acting in org admin wrote
// /orgs/admin/orgs/hanzo — where no reader of the org it named looks. These pin
// that the write is refused with the address it would have landed at, and that a
// record already written there is still reachable to remove.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func TestNamesAnOrg(t *testing.T) {
	for sub, want := range map[string]bool{
		"/orgs/hanzo": true,
		"orgs/hanzo":  true,
		"/orgs":       true,
		"/orgs/":      true,
		" /orgs/x ":   true,
		"":            false,
		"/":           false,
		"/ci/orgs":    false,
		"/orgsx":      false,
		"/agents/bot": false,
	} {
		if got := namesAnOrg(sub); got != want {
			t.Errorf("namesAnOrg(%q) = %v, want %v", sub, got, want)
		}
	}
}

// strayBroker is broker with the store in hand, so a test can seed the nested
// record a refused write would have made.
func strayBroker(t *testing.T) (*zip.App, *Client) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	c, err := New(Config{DataDir: t.TempDir(), MasterKeyB64: testMasterKey}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	t.Setenv("CLOUD_BRAND", "hanzo")
	UseGrants(t, `
grants:
- holder: u@acme
  org: acme
  read:  [{path: /orgs/hanzo, env: prod, keys: [NPM_TOKEN]}]
  write: [{path: /orgs/hanzo, env: prod, keys: [NPM_TOKEN]}, {path: /, env: prod, keys: [NPM_TOKEN]}]
`)
	app.Use(HeaderBoundary)
	chain, err := audit.Open(t.TempDir(), "kms", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	if err := Use(app, cloud.Deps{KMS: c, IAMIssuer: "https://hanzo.id/", Audit: chain}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	return app, c
}

func TestAWriteThatNamesAnOrgIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, c := strayBroker(t)
		for _, path := range []string{"/orgs/hanzo", "orgs/hanzo/", "/orgs"} {
			status, body := ask(t, app, http.MethodPost, "/v1/kms/secrets", "acme", true,
				`{"path":"`+path+`","name":"NPM_TOKEN","env":"prod","value":"v"}`)
			if status != http.StatusBadRequest {
				t.Fatalf("path %q: %d %s, want 400 — even a holder granted that path may not nest an org", path, status, body)
			}
			for _, want := range []string{"/orgs/acme/orgs", "--as"} {
				if !strings.Contains(string(body), want) {
					t.Errorf("path %q: the refusal does not say %q: %s", path, want, body)
				}
			}
		}
		if _, err := c.Get("/orgs/acme/orgs/hanzo", "NPM_TOKEN", "prod"); err == nil {
			t.Fatal("a refused write was stored anyway")
		}
		// The org root itself is still written.
		if s, b := ask(t, app, http.MethodPost, "/v1/kms/secrets", "acme", true,
			`{"path":"/","name":"NPM_TOKEN","env":"prod","value":"v"}`); s != http.StatusOK {
			t.Fatalf("a write at the org root: %d %s", s, b)
		}
	})
}

// A record a nested write already made is found and removed through the same
// op that removes any other, so a stray can be cleaned up without a store edit.
func TestANestedRecordIsStillRemovable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, c := strayBroker(t)
		if err := c.Put("/orgs/acme/orgs/hanzo", "NPM_TOKEN", "prod", []byte("stray")); err != nil {
			t.Fatalf("seed: %v", err)
		}
		status, body := ask(t, app, http.MethodDelete, "/v1/kms/secrets/orgs/hanzo/NPM_TOKEN?env=prod", "acme", false, "")
		if status != http.StatusOK {
			t.Fatalf("delete: %d %s", status, body)
		}
		if _, err := c.Get("/orgs/acme/orgs/hanzo", "NPM_TOKEN", "prod"); err == nil {
			t.Fatal("the stray survived its delete")
		}
	})
}
