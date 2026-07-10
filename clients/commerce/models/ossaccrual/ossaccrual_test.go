package ossaccrual

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

func testDB() *datastore.Datastore { return datastore.New(context.Background()) }

func TestKind(t *testing.T) {
	if (&OSSAccrual{}).Kind() != "oss-accrual" {
		t.Fatalf("kind = %q", (&OSSAccrual{}).Kind())
	}
}

func TestDefaults(t *testing.T) {
	a := New(testDB())
	if a.Status != Pending {
		t.Fatalf("default status = %q, want pending", a.Status)
	}
	if a.Currency != "usd" {
		t.Fatalf("default currency = %q, want usd", a.Currency)
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	a := New(testDB())
	a.PURL = "pkg:golang/github.com/gin-gonic/gin@v1.12.0"
	a.Name = "gin"
	a.Scope = Direct
	a.SpendOrg = "acme"
	a.SourceTxnID = "txn1"
	a.Amount = currency.Cents(1000)
	a.FundingTarget = "github_sponsors:gin-gonic"
	a.IdempotencyKey = "ossac_deadbeef"
	a.Metadata = map[string]interface{}{"k": "v"}

	ps, err := a.Save()
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded := New(testDB())
	if err := loaded.Load(ps); err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.PURL != a.PURL || loaded.Amount != 1000 || loaded.FundingTarget != "github_sponsors:gin-gonic" {
		t.Fatalf("round-trip mismatch: %+v", loaded)
	}
	if loaded.Metadata["k"] != "v" {
		t.Fatalf("metadata not round-tripped: %+v", loaded.Metadata)
	}
}
