package registry

// live_test.go — the same mounted app driven against the REAL registries.
//
// The fakes in typed_wire_test.go pin the measured wire; this proves the
// measurement stays true against the deployed backends: oci.hanzo.ai (its /v2/
// challenge naming the IAM realm) and pkg.hanzo.ai (the verdaccio search
// index). Opt-in by env so CI needs no network:
//
//	REGISTRY_E2E=1 make -C apps/registry test
//
// runs the credential-free half (status + packages). Adding the platform
// credential runs the authed half too (projects, images, tags, token):
//
//	REGISTRY_E2E=1 REGISTRY_E2E_ORG=hanzo \
//	REGISTRY_E2E_ID=<client_id> REGISTRY_E2E_SECRET=<client_secret> \
//	  make -C apps/registry test

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
)

func liveApp(t *testing.T) *zip.App {
	t.Helper()
	if os.Getenv("REGISTRY_E2E") == "" {
		t.Skip("REGISTRY_E2E unset — live end-to-end skipped")
	}
	// Default hosts ARE the live hosts; only the credential is injected.
	t.Setenv("REGISTRY_CLIENT_ID", os.Getenv("REGISTRY_E2E_ID"))
	t.Setenv("REGISTRY_CLIENT_SECRET", os.Getenv("REGISTRY_E2E_SECRET"))
	app := zip.New(zip.Config{Logger: luxlog.New("registrylive"), DisableStartupMessage: true})
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// The credential-free half: the OCI challenge and the npm index answer from
// the real hosts.
func TestLiveStatusAndPackages(t *testing.T) {
	app := liveApp(t)

	status, body := do(t, app, http.MethodGet, "/v1/registry/status", "u1", "hanzo", "")
	if status != http.StatusOK || !strings.Contains(body, `"oci":true`) || !strings.Contains(body, `"pkg":true`) {
		t.Fatalf("status = %d %s — are oci.hanzo.ai and pkg.hanzo.ai up?", status, body)
	}
	if !strings.Contains(body, "/v1/iam/registry/token") {
		t.Fatalf("status realm = %s, want the IAM registry token realm", body)
	}

	status, body = do(t, app, http.MethodGet, "/v1/registry/packages", "u1", "hanzo", "")
	if status != http.StatusOK {
		t.Fatalf("packages = %d %s", status, body)
	}
	var pkgs struct {
		Data []struct{ Name string } `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &pkgs); err != nil {
		t.Fatalf("packages unparseable: %s", body)
	}
	for _, p := range pkgs.Data {
		if p.Name != "hanzo" && !strings.HasPrefix(p.Name, "@hanzo/") {
			t.Errorf("live package %q escaped the org scope", p.Name)
		}
	}
}

// The authed half: catalog, tags and a minted pull token through the real IAM
// realm. Needs the platform credential.
func TestLiveImagesAndToken(t *testing.T) {
	app := liveApp(t)
	if os.Getenv("REGISTRY_E2E_ID") == "" {
		t.Skip("REGISTRY_E2E_ID unset — authed live half skipped")
	}
	org := os.Getenv("REGISTRY_E2E_ORG")
	if org == "" {
		org = "hanzo"
	}

	status, body := do(t, app, http.MethodGet, "/v1/registry/images", "u1", org, "")
	if status != http.StatusOK {
		t.Fatalf("images = %d %s", status, body)
	}
	var images struct {
		Data []struct{ Name, Ref string } `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &images); err != nil {
		t.Fatalf("images unparseable: %s", body)
	}

	status, body = do(t, app, http.MethodGet, "/v1/registry/projects", "u1", org, "")
	if status != http.StatusOK || !strings.Contains(body, `"project":"`+org+`"`) {
		t.Fatalf("projects = %d %s", status, body)
	}

	if len(images.Data) == 0 {
		t.Logf("org %q holds no images yet — tags/token stages have nothing to address", org)
		return
	}
	first := images.Data[0].Name
	if status, body = do(t, app, http.MethodGet, "/v1/registry/tags?image="+first, "u1", org, ""); status != http.StatusOK {
		t.Fatalf("tags(%s) = %d %s", first, status, body)
	}
	status, body = do(t, app, http.MethodPost, "/v1/registry/token", "u1", org, `{"image":"`+first+`"}`)
	if status != http.StatusOK || !strings.Contains(body, `"token":`) {
		t.Fatalf("token(%s) = %d %s", first, status, body)
	}
}
