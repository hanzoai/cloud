package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/engine"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// withSystemCtx returns a gin context wired so systemDB(c) resolves to the
// ae-backed SQLite datastore in the "system" namespace.
func withSystemCtx(c *gin.Context) {
	ctx := context.Background()
	c.Set("context", ctx)
}

// TestOSSPayout_HTTP_EndToEnd drives the real HTTP handlers through a gin
// router against a live SQLite datastore:
//
//	POST /sbom            (arcd ingest)  ->
//	engine.AccrueOSSPayout (the usage hook, called synchronously here) ->
//	GET  /oss-payout/summary  shows per-package owed + resolved maintainer.
//
// This is the sandbox proof of the HTTP surface (routing + JSON + storage +
// rollup), complementing the engine-level integration test.
func TestOSSPayout_HTTP_EndToEnd(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()

	gin.SetMode(gin.TestMode)

	// ── 1. Ingest an SBOM via the HTTP handler ───────────────────────────
	ingestBody, _ := json.Marshal(sbomIngestRequest{
		ImageRef:    "ghcr.io/hanzoai/commerce:1.785.15",
		ImageDigest: "sha256:httpproof",
		Service:     "commerce",
		Tool:        "syft",
		Components: []sbomComponent{
			{PURL: "pkg:golang/github.com/gin-gonic/gin@v1.12.0", Name: "gin", Ecosystem: "golang", Version: "v1.12.0", Scope: "direct"},
			{PURL: "pkg:golang/github.com/google/uuid@v1.6.0", Name: "uuid", Ecosystem: "golang", Version: "v1.6.0", Scope: "direct"},
			{PURL: "pkg:npm/react@18.3.1", Name: "react", Ecosystem: "npm", Version: "18.3.1", Scope: "transitive"},
		},
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	withSystemCtx(c)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/billing/sbom", bytes.NewReader(ingestBody))
	c.Request.Header.Set("Content-Type", "application/json")
	IngestSBOM(c)

	if w.Code != 201 {
		t.Fatalf("ingest status = %d, body=%s", w.Code, w.Body.String())
	}
	var ingestResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &ingestResp)
	if ingestResp["componentCount"].(float64) != 3 {
		t.Fatalf("ingest componentCount = %v, want 3", ingestResp["componentCount"])
	}

	// ── 2. Accrue against a $100 org charge (the usage hook, sync) ────────
	orgDB := datastore.New(context.Background())
	orgDB.SetNamespace("acme")
	engine.AccrueOSSPayout(orgDB, "acme", "acme/alice", 10_000, "usd", "txn_http_proof", false)

	// ── 3. Read the payout summary via the HTTP handler ──────────────────
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	withSystemCtx(c2)
	c2.Request = httptest.NewRequest(http.MethodGet, "/v1/billing/oss-payout/summary", nil)
	GetOSSPayoutSummary(c2)

	if w2.Code != 200 {
		t.Fatalf("summary status = %d, body=%s", w2.Code, w2.Body.String())
	}

	var summary struct {
		TotalAccruedCents int64 `json:"totalAccruedCents"`
		HeldCents         int64 `json:"heldCents"`
		PackageCount      int   `json:"packageCount"`
		Maintainers       []struct {
			PURL          string `json:"purl"`
			AccruedCents  int64  `json:"accruedCents"`
			FundingTarget string `json:"fundingTarget"`
			FundingKind   string `json:"fundingKind"`
			Status        string `json:"status"`
		} `json:"maintainers"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary: %v; body=%s", err, w2.Body.String())
	}

	// 25% of $100 = $25.00 = 2500 cents. Weights: 2 direct (1.0) + 1 transitive
	// (0.25) = 2.25 total. direct = 2500*(1/2.25)=1111 (largest-remainder rounds
	// to keep the pool exact), transitive react = 2500*(0.25/2.25)=277.
	if summary.TotalAccruedCents != 2500 {
		t.Fatalf("total accrued = %d, want 2500 (25%% cap)", summary.TotalAccruedCents)
	}
	if summary.PackageCount != 3 {
		t.Fatalf("package count = %d, want 3", summary.PackageCount)
	}

	byPURL := map[string]int64{}
	target := map[string]string{}
	status := map[string]string{}
	for _, m := range summary.Maintainers {
		byPURL[m.PURL] = m.AccruedCents
		target[m.PURL] = m.FundingTarget
		status[m.PURL] = m.Status
	}

	// gin resolves to github_sponsors:gin-gonic, status pending.
	if target["pkg:golang/github.com/gin-gonic/gin@v1.12.0"] != "github_sponsors:gin-gonic" {
		t.Errorf("gin funding target = %q", target["pkg:golang/github.com/gin-gonic/gin@v1.12.0"])
	}
	if status["pkg:golang/github.com/gin-gonic/gin@v1.12.0"] != "pending" {
		t.Errorf("gin status = %q, want pending", status["pkg:golang/github.com/gin-gonic/gin@v1.12.0"])
	}
	// react (npm, no VCS coordinate) is held.
	if status["pkg:npm/react@18.3.1"] != "held" {
		t.Errorf("react status = %q, want held", status["pkg:npm/react@18.3.1"])
	}
	if summary.HeldCents != byPURL["pkg:npm/react@18.3.1"] {
		t.Errorf("heldCents = %d, want react's %d", summary.HeldCents, byPURL["pkg:npm/react@18.3.1"])
	}

	// Conservation across the HTTP rollup: directs + held == total.
	var sum int64
	for _, v := range byPURL {
		sum += v
	}
	if sum != 2500 {
		t.Fatalf("summary rollup sum = %d, want 2500", sum)
	}

	t.Logf("PROOF: $100 spend -> %d cents (25%%) across %d packages; gin=%dc->%s react=%dc->held",
		summary.TotalAccruedCents, summary.PackageCount,
		byPURL["pkg:golang/github.com/gin-gonic/gin@v1.12.0"], target["pkg:golang/github.com/gin-gonic/gin@v1.12.0"],
		byPURL["pkg:npm/react@18.3.1"])
}
