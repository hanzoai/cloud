package principal_test

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// probe mounts a route that resolves the tenant via principal.Tenant and reports
// {org, ok, validated}. It drives the SAME zip.Ctx header accessors the gateway
// feeds in production, so a test sets X-User-Id / X-Org-Id exactly as
// SanitizeIdentity would: X-User-Id is present ONLY for a validated principal, and
// on the bearer-less path the client's X-Org-Id is restored while X-User-Id stays
// empty — the forge these tests exercise.
func probe(t *testing.T, headers map[string]string) (org string, ok, validated bool) {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/probe", func(c *zip.Ctx) error {
		o, k := principal.Tenant(c)
		return c.JSON(200, map[string]any{"org": o, "ok": k, "validated": principal.Validated(c)})
	})
	req := httptest.NewRequest("GET", "/probe", nil)
	for h, v := range headers {
		req.Header.Set(h, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Org       string `json:"org"`
		OK        bool   `json:"ok"`
		Validated bool   `json:"validated"`
	}
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("probe decode: %v (%s)", err, b)
	}
	return out.Org, out.OK, out.Validated
}

// TestTenant_ForgedOrgNoPrincipalRefused is THE break: an off-gateway caller
// forges X-Org-Id with NO validated principal (no X-User-Id). The tenant MUST NOT
// resolve — otherwise a bearer-less request reads/writes another tenant's data.
func TestTenant_ForgedOrgNoPrincipalRefused(t *testing.T) {
	org, ok, validated := probe(t, map[string]string{"X-Org-Id": "victim"})
	if ok || org != "" {
		t.Fatalf("forged X-Org-Id with no principal resolved a tenant: org=%q ok=%v", org, ok)
	}
	if validated {
		t.Fatalf("Validated must be false with no X-User-Id")
	}
}

// TestTenant_ValidatedPrincipalResolves: a validated principal (X-User-Id set)
// with an org resolves that org — the legitimate console-BFF path.
func TestTenant_ValidatedPrincipalResolves(t *testing.T) {
	org, ok, validated := probe(t, map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_1"})
	if !ok || !validated || org != "acme" {
		t.Fatalf("validated principal should resolve org=acme, got org=%q ok=%v validated=%v", org, ok, validated)
	}
}

// TestTenant_VerbatimNoFold: the org is the tenant KEY, used verbatim — never
// case-folded (folding would collapse distinct owners into one bucket).
func TestTenant_VerbatimNoFold(t *testing.T) {
	org, ok, _ := probe(t, map[string]string{"X-Org-Id": "ACME", "X-User-Id": "u_1"})
	if !ok || org != "ACME" {
		t.Fatalf("org must be verbatim %q, got %q (ok=%v)", "ACME", org, ok)
	}
}

// TestTenant_EmptyOrgWithPrincipalRefused: a validated principal but NO org is a
// true refusal — there is no magic default/admin bucket at this layer.
func TestTenant_EmptyOrgWithPrincipalRefused(t *testing.T) {
	org, ok, validated := probe(t, map[string]string{"X-User-Id": "u_1"})
	if ok || org != "" {
		t.Fatalf("validated principal with empty org must not resolve, got org=%q ok=%v", org, ok)
	}
	if !validated {
		t.Fatalf("X-User-Id set should be Validated()=true")
	}
}

// TestTenant_OverlongOrgRefused: an org past MaxOrgLen is malformed/hostile and is
// rejected before it can become a storage key.
func TestTenant_OverlongOrgRefused(t *testing.T) {
	long := strings.Repeat("a", principal.MaxOrgLen+1)
	org, ok, _ := probe(t, map[string]string{"X-Org-Id": long, "X-User-Id": "u_1"})
	if ok || org != "" {
		t.Fatalf("over-long org must be refused, got ok=%v len=%d", ok, len(org))
	}
}

// TestValidated_OnlyFromUserId: Validated tracks X-User-Id alone — a forged
// X-Org-Id never makes a request "validated".
func TestValidated_OnlyFromUserId(t *testing.T) {
	if _, _, v := probe(t, map[string]string{"X-Org-Id": "x"}); v {
		t.Fatal("Validated must be false without X-User-Id")
	}
	if _, _, v := probe(t, map[string]string{"X-User-Id": "u"}); !v {
		t.Fatal("Validated must be true with X-User-Id")
	}
}
