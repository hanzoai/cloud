package billing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/principal"
)

// A sale for an org the caller's token does not list — one created after the token
// was minted — is refused before anything is charged, never bought for the org the
// boundary fell back to; a sale in an org the token opened goes on.
func TestASaleForAnOrgTheTokenDoesNotListIsRefused(t *testing.T) {
	for _, tc := range []struct {
		unopened string
		want     int
	}{{"acme-new", http.StatusConflict}, {"", http.StatusNoContent}} {
		app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
		app.Raw(http.MethodPost, "/sale", func(c *zip.Ctx) error {
			if tc.unopened != "" {
				principal.Unopen(c, tc.unopened)
			}
			if err := opened(c); err != nil {
				return err
			}
			return c.NoContent(http.StatusNoContent)
		})
		resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/sale", nil), zip.TestConfig{Timeout: 10 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want || (tc.unopened != "" && !strings.Contains(string(body), "acme-new")) {
			t.Fatalf("unopened %q: %d %s, want %d naming the org", tc.unopened, resp.StatusCode, body, tc.want)
		}
	}
}
