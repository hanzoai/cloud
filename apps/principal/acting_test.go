package principal_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// acting drives Acting over the SAME seam a typed op uses: the facts are parked
// off a real request exactly as cloud.Bridge parks them, and read back off a
// context and nothing else.
func acting(t *testing.T, headers map[string]string) (org string, refusal string) {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/acting", func(c *zip.Ctx) error {
		ctx := principal.WithValidated(principal.WithOrg(c.Context(), c), c)
		got, err := principal.Acting(ctx)
		if err != nil {
			return err
		}
		return c.JSON(200, map[string]any{"org": got})
	})
	req := httptest.NewRequest("GET", "/acting", nil)
	for h, v := range headers {
		req.Header.Set(h, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("acting: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", string(raw)
	}
	var out struct{ Org string }
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out.Org, ""
}

// TestActingAnswersForAValidatedOrg: the ordinary path, where a gateway-minted
// request carries both facts.
func TestActingAnswersForAValidatedOrg(t *testing.T) {
	org, refusal := acting(t, map[string]string{"X-User-Id": "u_1", "X-Org-Id": "acme"})
	if refusal != "" {
		t.Fatalf("a validated request was refused: %s", refusal)
	}
	if org != "acme" {
		t.Fatalf("Acting = %q, want acme", org)
	}
}

// TestActingRefusesTheForgery is the property the thirty-three copies existed to
// enforce and the reason this cannot be a bare header read: an off-gateway caller
// naming a victim org with NO credential resolves nothing.
func TestActingRefusesTheForgery(t *testing.T) {
	org, refusal := acting(t, map[string]string{"X-Org-Id": "victim"})
	if org != "" {
		t.Fatalf("Acting resolved %q for an unvalidated caller — that is the forge", org)
	}
	if !strings.Contains(refusal, "validated org") {
		t.Fatalf("refusal was %q; it must name what is missing", refusal)
	}
}

// TestActingRefusesAValidatedCallerWithNoOrg: validated is not enough. A machine
// token, or one minted before IAM's orgs claim, names no home org — and a plane
// with per-org rows has nothing to scope by.
func TestActingRefusesAValidatedCallerWithNoOrg(t *testing.T) {
	if org, refusal := acting(t, map[string]string{"X-User-Id": "u_1"}); org != "" || refusal == "" {
		t.Fatalf("Acting = %q, refusal %q — want a refusal", org, refusal)
	}
}

// TestActingRefusesOffTheHTTPPath: with no request behind it there is no attested
// caller, so there is no org to act for. The CLI's local invoke lands here.
func TestActingRefusesOffTheHTTPPath(t *testing.T) {
	org, err := principal.Acting(context.Background())
	if err == nil || org != "" {
		t.Fatalf("Acting off the HTTP path = %q,%v — want a refusal", org, err)
	}
}
