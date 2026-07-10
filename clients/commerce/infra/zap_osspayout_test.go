package infra

import (
	"context"
	"encoding/json"
	"testing"
)

// TestRegisterSBOMIngest_Decode proves the ZAP OpSBOMIngest handler decodes the
// payload framing and invokes the injected store with the parsed SBOM — the
// ZAP-native data path that mirrors the HTTP ingest. The store is a fake so the
// test needs no datastore.
func TestRegisterSBOMIngest_Decode(t *testing.T) {
	var got SBOMIngestPayload
	called := false

	// Build a request message exactly as a ZAP client would (BuildZAPRequest
	// frames status+payload; the opcode rides in flags).
	payload, _ := json.Marshal(SBOMIngestPayload{
		ImageRef:    "ghcr.io/hanzoai/commerce:1.0.0",
		ImageDigest: "sha256:abc",
		Service:     "commerce",
		Tool:        "syft",
		Components: []SBOMIngestComponent{
			{PURL: "pkg:golang/github.com/gin-gonic/gin@v1.12.0", Name: "gin", Scope: "direct"},
		},
	})
	req := BuildZAPRequest(OpSBOMIngest, payload)

	// sbomHandlerFor is exactly what RegisterSBOMIngest wires onto the node.
	h := sbomHandlerFor(func(_ context.Context, p SBOMIngestPayload) error {
		called = true
		got = p
		return nil
	})
	resp, err := h(context.Background(), "", req)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if resp.Root().Uint8(zapFieldStatus) != 0 {
		t.Fatalf("expected OK status, got error response")
	}
	if !called {
		t.Fatal("store was not called")
	}
	if got.ImageDigest != "sha256:abc" || len(got.Components) != 1 {
		t.Fatalf("decoded payload wrong: %+v", got)
	}
	if got.Components[0].PURL != "pkg:golang/github.com/gin-gonic/gin@v1.12.0" {
		t.Fatalf("component PURL wrong: %q", got.Components[0].PURL)
	}
}

// TestRegisterSBOMIngest_BadPayload returns an error response (not a crash) on
// missing digest / bad JSON — fail-soft framing.
func TestRegisterSBOMIngest_Validation(t *testing.T) {
	h := sbomHandlerFor(func(_ context.Context, _ SBOMIngestPayload) error { return nil })

	// Missing digest.
	noDigest, _ := json.Marshal(SBOMIngestPayload{ImageRef: "x"})
	resp, _ := h(context.Background(), "", BuildZAPRequest(OpSBOMIngest, noDigest))
	if resp.Root().Uint8(zapFieldStatus) != 1 {
		t.Fatal("expected error status for missing digest")
	}

	// Bad JSON.
	resp2, _ := h(context.Background(), "", BuildZAPRequest(OpSBOMIngest, []byte("{bad")))
	if resp2.Root().Uint8(zapFieldStatus) != 1 {
		t.Fatal("expected error status for bad JSON")
	}
}
