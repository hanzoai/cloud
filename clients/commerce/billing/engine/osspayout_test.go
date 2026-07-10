package engine

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/ossaccrual"
	"github.com/hanzoai/cloud/clients/commerce/models/sbomrecord"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// TestAccrueOSSPayout_EndToEnd is the sandbox proof of the whole pipeline:
//
//	a deploy emits an SBOM  ->  an org's spend accrues the <=25% share across
//	that SBOM's packages in the ledger  ->  per-package accrual + resolved
//	maintainers are queryable.
func TestAccrueOSSPayout_EndToEnd(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	// The accrual engine reads SBOMs + writes accruals in the "system"
	// namespace (platform-wide liability). Build a system-scoped db to seed and
	// assert against.
	sysDB := datastore.New(c)
	sysDB.SetNamespace("system")

	// ── 1. A deploy emits an SBOM ────────────────────────────────────────
	// Simulate the arcd build pipeline ingesting the SBOM of a built image.
	// 2 direct deps + 2 transitive deps.
	rec := sbomrecord.New(sysDB)
	rec.ImageRef = "ghcr.io/hanzoai/commerce:1.785.15"
	rec.ImageDigest = "sha256:deadbeef"
	rec.Service = "commerce"
	rec.Tool = "syft"
	rec.Components = []sbomrecord.Component{
		{PURL: "pkg:golang/github.com/gin-gonic/gin@v1.12.0", Name: "gin", Ecosystem: "golang", Version: "v1.12.0", Scope: "direct"},
		{PURL: "pkg:golang/github.com/google/uuid@v1.6.0", Name: "uuid", Ecosystem: "golang", Version: "v1.6.0", Scope: "direct"},
		{PURL: "pkg:golang/golang.org/x/sys@v0.20.0", Name: "x/sys", Ecosystem: "golang", Version: "v0.20.0", Scope: "transitive"},
		{PURL: "pkg:npm/react@18.3.1", Name: "react", Ecosystem: "npm", Version: "18.3.1", Scope: "transitive"},
	}
	if err := rec.Create(); err != nil {
		t.Fatalf("seed SBOM: %v", err)
	}

	// ── 2. An org spends; the accrual engine attributes <=25% ────────────
	// The spending org's db is namespaced to the org; the engine internally
	// switches to the system namespace for SBOMs + accruals.
	orgDB := datastore.New(c)
	orgDB.SetNamespace("acme")

	const chargeCents = 10_000 // $100.00 of Hanzo cloud spend
	AccrueOSSPayout(orgDB, "acme", "acme/alice", chargeCents, "usd", "txn_proof_1", false)

	// ── 3. Read back the accrual ledger ──────────────────────────────────
	accruals := make([]*ossaccrual.OSSAccrual, 0, 8)
	if _, err := ossaccrual.Query(sysDB).
		Ancestor(sysDB.NewKey("synckey", "", 1, nil)).
		Filter("SourceTxnID=", "txn_proof_1").
		GetAll(&accruals); err != nil {
		t.Fatalf("query accruals: %v", err)
	}

	if len(accruals) != 4 {
		t.Fatalf("expected 4 accrual lines (one per package), got %d", len(accruals))
	}

	// The pool is 25% of $100 = $25.00 = 2500 cents. Weights: direct 1.0 x2,
	// transitive 0.25 x2 => total 2.5. So:
	//   each direct  = 2500 * (1.0/2.5)  = 1000 cents
	//   each transitive = 2500 * (0.25/2.5) = 250 cents
	// Sum = 1000+1000+250+250 = 2500. Cap respected exactly.
	var total currency.Cents
	byPURL := map[string]*ossaccrual.OSSAccrual{}
	for _, a := range accruals {
		total += a.Amount
		byPURL[a.PURL] = a
		if a.SpendOrg != "acme" {
			t.Errorf("accrual %s has wrong spendOrg %q", a.PURL, a.SpendOrg)
		}
	}

	if total != 2500 {
		t.Fatalf("total accrued = %d cents, want 2500 (25%% of 10000) — CAP VIOLATED if higher", total)
	}
	if total > chargeCents/4 {
		t.Fatalf("accrued %d exceeds 25%% cap of %d", total, chargeCents/4)
	}

	gin := byPURL["pkg:golang/github.com/gin-gonic/gin@v1.12.0"]
	react := byPURL["pkg:npm/react@18.3.1"]
	if gin == nil || react == nil {
		t.Fatalf("missing expected packages in accruals")
	}
	if gin.Amount != 1000 {
		t.Errorf("gin (direct) accrued %d, want 1000", gin.Amount)
	}
	if react.Amount != 250 {
		t.Errorf("react (transitive) accrued %d, want 250", react.Amount)
	}

	// ── 4. Maintainer resolution ─────────────────────────────────────────
	// gin's PURL embeds github.com/gin-gonic/gin -> github_sponsors:gin-gonic.
	if gin.FundingTarget != "github_sponsors:gin-gonic" {
		t.Errorf("gin funding target = %q, want github_sponsors:gin-gonic", gin.FundingTarget)
	}
	if gin.Status != ossaccrual.Pending {
		t.Errorf("gin status = %q, want pending (resolved)", gin.Status)
	}
	// react's npm PURL has no VCS coordinate -> unresolved -> held.
	if react.FundingKind != "unresolved" {
		t.Errorf("react funding kind = %q, want unresolved", react.FundingKind)
	}
	if react.Status != ossaccrual.Held {
		t.Errorf("react status = %q, want held (unresolved target)", react.Status)
	}
}

// TestAccrueOSSPayout_Idempotent proves the same (org, txn, package) accrues
// exactly once even when the hook fires twice (retry safety).
func TestAccrueOSSPayout_Idempotent(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	sysDB := datastore.New(c)
	sysDB.SetNamespace("system")

	rec := sbomrecord.New(sysDB)
	rec.ImageRef = "ghcr.io/hanzoai/gateway:1.0.0"
	rec.ImageDigest = "sha256:cafebabe"
	rec.Service = "gateway"
	rec.Components = []sbomrecord.Component{
		{PURL: "pkg:golang/github.com/gin-gonic/gin@v1.12.0", Name: "gin", Scope: "direct"},
	}
	if err := rec.Create(); err != nil {
		t.Fatalf("seed SBOM: %v", err)
	}

	orgDB := datastore.New(c)
	orgDB.SetNamespace("beta")

	// Fire twice with the same source transaction id.
	AccrueOSSPayout(orgDB, "beta", "beta/bob", 4000, "usd", "txn_dup", false)
	AccrueOSSPayout(orgDB, "beta", "beta/bob", 4000, "usd", "txn_dup", false)

	accruals := make([]*ossaccrual.OSSAccrual, 0, 8)
	if _, err := ossaccrual.Query(sysDB).
		Ancestor(sysDB.NewKey("synckey", "", 1, nil)).
		Filter("SourceTxnID=", "txn_dup").
		GetAll(&accruals); err != nil {
		t.Fatalf("query accruals: %v", err)
	}

	if len(accruals) != 1 {
		t.Fatalf("idempotency broken: expected 1 accrual after 2 fires, got %d", len(accruals))
	}
	// 25% of 4000 = 1000, one direct package gets the whole pool.
	if accruals[0].Amount != 1000 {
		t.Errorf("accrued %d, want 1000 (whole 25%% pool to the one package)", accruals[0].Amount)
	}
}

// TestAccrueOSSPayout_NoSBOM is a no-op when no SBOMs are ingested (nothing to
// attribute) — never errors, never invents accruals.
func TestAccrueOSSPayout_NoSBOM(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()

	orgDB := datastore.New(c)
	orgDB.SetNamespace("gamma")
	AccrueOSSPayout(orgDB, "gamma", "gamma/carol", 5000, "usd", "txn_nosbom", false)

	sysDB := datastore.New(c)
	sysDB.SetNamespace("system")
	accruals := make([]*ossaccrual.OSSAccrual, 0, 8)
	if _, err := ossaccrual.Query(sysDB).
		Ancestor(sysDB.NewKey("synckey", "", 1, nil)).
		Filter("SourceTxnID=", "txn_nosbom").
		GetAll(&accruals); err != nil {
		t.Fatalf("query accruals: %v", err)
	}
	if len(accruals) != 0 {
		t.Fatalf("expected 0 accruals with no SBOM, got %d", len(accruals))
	}
}
