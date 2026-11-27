package provider

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/zap-proto/zip"
)

// A LINK IS SPENT ONCE, AND ONE THE DEPLOYMENT CANNOT CHECK IS A 503. It used to
// read as "link already used", which sends the user off to start over for a fault
// that clears in seconds. A deployment of one pod is the process, so with its KV
// down a link is still spent once, counted here.
func TestALinkIsSpentOnceAndAnUncheckableOneIs503(t *testing.T) {
	coordtest.Default(t, coord.Alone())
	set := newSeenSet("test:link", time.Minute)
	if fresh, err := set.redeem("n1"); !fresh || err != nil {
		t.Fatalf("first use: fresh=%v err=%v", fresh, err)
	}
	if fresh, err := set.redeem("n1"); fresh || err != nil {
		t.Fatalf("second use: fresh=%v err=%v", fresh, err)
	}
	down := func(declared int) *coord.Coord {
		return coordtest.Down(coord.Config{Self: coord.Member{ID: "cloud-0"}, Declared: declared})
	}
	coordtest.Default(t, down(0))
	if fresh, err := set.redeem("n2"); !fresh || err != nil {
		t.Fatalf("one pod, KV down, first use: fresh=%v err=%v", fresh, err)
	}
	if fresh, err := set.redeem("n2"); fresh || err != nil {
		t.Fatalf("one pod, KV down, second use: fresh=%v err=%v", fresh, err)
	}
	coordtest.Default(t, down(2))
	_, err := set.redeem("n3")
	var ze *zip.HTTPError
	if !errors.As(err, &ze) || ze.Status != http.StatusServiceUnavailable {
		t.Fatalf("several pods, a link the KV cannot check: %v, want a 503", err)
	}
}
