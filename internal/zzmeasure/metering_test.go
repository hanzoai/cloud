package zzmeasure

// The split-deploy shape: CLOUD_COMMERCE_HTTP_URL unset (config.go:367 default "")
// and cfg.Enabled("commerce") false in every process but commerce (build.go:196),
// so buildMeteringClient hands every app a Client with an empty BaseURL.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/money"
)

func TestSplitDeployMeteringClient(t *testing.T) {
	c, err := metering.New(metering.Config{}) // exactly build.go's split-deploy client
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Enabled()                 = %v", c.Enabled())

	// the PRE-request gate on a caller with zero balance
	err = c.Authorize(context.Background(), metering.AuthInput{
		User: "u_broke", Org: "acme", Currency: "usd", Amount: money.FromCents(500),
	})
	t.Logf("Authorize(broke, $5.00)   = %v   (nil == allowed)", err)

	v, verr := c.AuthorizeVerdict(context.Background(), metering.AuthInput{
		User: "u_broke", Org: "acme", Currency: "usd", Amount: money.FromCents(500),
	})
	t.Logf("AuthorizeVerdict          = allow=%v reason=%q err=%v", v.Allow, v.Reason, verr)

	// the DEBIT for work already done
	res, rerr := c.Record(context.Background(), metering.Usage{
		User: "u_broke", Org: "acme", Currency: "usd", Amount: money.FromCents(500),
		Model: "zen", Provider: "hanzo", Service: "ai",
	})
	t.Logf("Record($5.00)             = res=%v err=%v   (nil,nil == silently dropped)", res, rerr)
}
