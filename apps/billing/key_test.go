package billing

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestTheCatalogSurvivesAMissingKey is the inverse of the test that stood here, and
// the reason it stood down.
//
// This app verifies tokens the account process mints, and it used to refuse to MOUNT
// when the two could not share a key. That refusal was credited with protecting the
// money path. It did not: cloud.Intended consults the key in one branch and that
// branch already answers 403, and a process with no key holds a random one, so it
// refuses every token rather than accepting a forged one. What the refusal actually
// did was take down all ~23 reads with the writes — including GET /v1/billing/plans,
// the PUBLIC catalog that answers no token at all and that every pricing surface
// reads. A key question stood between an anonymous caller and a price list.
//
// So the mount stands, the writes still meet the control one request at a time, and
// the catalog answers. Measured against the route table rather than argued, because
// "it mounted" and "the public read is reachable" are different claims.
func TestTheCatalogSurvivesAMissingKey(t *testing.T) {
	luxlog.SetDefault(luxlog.New("test"))
	t.Setenv(account.KeyEnv, "")

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Use(app, cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("billing refused to mount with no shared key: %v\n"+
			"the key gates one branch of one control; it does not gate the surface", err)
	}

	resp, err := app.Test(httptest.NewRequest("GET", "/v1/billing/plans", nil))
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// The catalog route is REGISTERED and REACHED. This binary runs no commerce, so
	// billing answers "this deployment runs no commerce" — its own sentence, from its
	// own handler. That is the whole assertion: a 404 would mean the prefix was never
	// claimed, and under the old check there was no route to reach at all because
	// Mount returned before registering one. Asserting the BODY rather than the code
	// is the difference between "billing answered" and "something answered".
	if resp.StatusCode == 404 {
		t.Errorf("GET /v1/billing/plans = 404 %q — the public catalog route was never registered", string(body))
	}
	if !strings.Contains(string(body), "commerce") {
		t.Errorf("GET /v1/billing/plans = %d %q — that is not billing's own answer, so its handler never ran",
			resp.StatusCode, string(body))
	}
}
