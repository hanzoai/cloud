package commerce_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/commerce"
	"github.com/hanzoai/cloud/client"
)

// A plan is bought where the catalog is on sale. On a brand's host that sells
// none, the sale is refused as a plan that is not there — before commerce is
// asked to charge anything, so no card, credit or balance moves.
func TestASaleOnAnotherBrandsHostIsNotThere(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{User: "z", Org: "hanzo"})
	for _, host := range []string{"pay.lux.cloud", "pay.lux.tel", "pay.zoo.cloud", "pay.pars.network"} {
		_, err := commerce.PlaneSubscribeForTest(ctx, &client.SaleIn{
			Host:     host,
			SourceID: "credits",
			PlanID:   "team",
			Quantity: 1,
			Subject:  "hanzo",
		})
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusNotFound {
			t.Errorf("a sale on %s answered %v, want 404 plan not found", host, err)
		}
	}
}
