package leaderboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of leaderboard operations that are NOT typed ops,
// each naming the wire mechanism that forbids one. An operation missing from the
// registry is invisible to the SDKs, the MCP tool list and the CLI, so every entry owes
// a reason that cites the code making it true.
var untypedByDesign = map[string]string{
	"GET /v1/leaderboard/feed": "BYTE-STREAM REPLY: Server-Sent Events that outlive the handler, " +
		"`return c.SendStreamWriter(func(w *bufio.Writer) {` (feed.go), while a typed op's only response " +
		"path is c.JSON(out). The media type is DECLARED (openapi.Bytes text/event-stream) and the " +
		"lines are described in its prose.",
	"GET /v1/leaderboard/badge/{handle}": "NON-JSON MEDIA TYPE: returns raw `image/svg+xml` bytes with " +
		"ETag / 304 conditional responses (`return c.Bytes(http.StatusOK, svg)` in badge.go), while a typed " +
		"op's only response path is c.JSON(out). The media type is DECLARED (spec.Bytes image/svg+xml) and " +
		"the options are described in its prose.",
}

// TestTheLedgerNamesWhatIsServed fails when a ledger entry names an address the app no
// longer serves, which is what keeps its reason from outliving its truth.
func TestTheLedgerNamesWhatIsServed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		installFakeDS(t, nil)
		app := mountApp(t)
		t.Cleanup(stopHub)
		for route := range untypedByDesign {
			method, path, _ := strings.Cut(route, " ")
			if strings.Contains(path, "{handle}") {
				// An invalid option answers 400 before handle lookup, proving the route
				// is mounted without needing a seeded listed org.
				path = strings.ReplaceAll(path, "{handle}", "probe.svg") + "?theme=invalid"
			}
			req := httptest.NewRequest(method, path, nil)
			resp, err := app.Test(req, zip.TestConfig{Timeout: 300 * time.Millisecond, FailOnTimeout: false})
			if err != nil {
				t.Fatalf("%s: %v", route, err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
				t.Errorf("untypedByDesign names %q, which leaderboard no longer serves (%d)", route, resp.StatusCode)
			}
		}
	})
}

// TestBackfillForceIsLiteralTrue pins the ONE judgement call in backfillQuery: Force
// is carried as a STRING, not as a bool.
//
// The guard has always been `c.Query("force") == "true"` — a literal compare. zip's
// URL binder fills a BOOL field with strconv.ParseBool, which also accepts "1", "t",
// "T", "TRUE" and (as a bare `?force`) an empty value. Typing the field as a bool
// would therefore have silently WIDENED the guard, and what it guards is a rollup
// that ACCUMULATES: a run that should have been refused doubles every day it
// re-reads. So the field is a string and the handler still compares it literally.
//
// Every near-miss spelling must still reach the already-seeded 409, exactly as it did
// untyped; only the literal "true" forces.
func TestBackfillForceIsLiteralTrue(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		installFakeDS(t, func(sql string, _ []any) []map[string]any {
			if strings.Contains(sql, "SELECT count() AS n FROM "+rollupTable) {
				return []map[string]any{{"n": uint64(42)}}
			}
			return nil
		})
		app := mountApp(t)
		super := withHeader(principalHeaders("admin", "root"), "X-User-IsAdmin", "true")

		for _, force := range []string{"", "1", "t", "T", "TRUE", "yes", "on"} {
			path := "/v1/admin/leaderboard/rollup"
			if force != "" {
				path += "?force=" + force
			}
			if code, body := doJSON(t, app, "POST", path, super, nil); code != 409 {
				t.Fatalf("force=%q must NOT force (want 409, got %d: %s) — a widened guard double-counts the rollup",
					force, code, body)
			}
		}

		// The one spelling that forces still does — so the loop above is the guard
		// holding, not the route being inert.
		if code, body := doJSON(t, app, "POST", "/v1/admin/leaderboard/rollup?force=true", super, nil); code != 200 {
			t.Fatalf("force=true must force (want 200, got %d: %s)", code, body)
		}
	})
}

// TestBackfillBeforeStillRejectsGarbage pins that `before` is parsed by the HANDLER,
// not by the binder: it is a string field, so an unparseable value reaches
// time.Parse and answers the same 400 it always did — rather than being coerced to a
// zero time and silently seeding the whole history.
func TestBackfillBeforeStillRejectsGarbage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		installFakeDS(t, nil)
		app := mountApp(t)
		super := withHeader(principalHeaders("admin", "root"), "X-User-IsAdmin", "true")

		if code, body := doJSON(t, app, "POST", "/v1/admin/leaderboard/rollup?before=lastweek", super, nil); code != 400 {
			t.Fatalf("unparseable before want 400, got %d (%s)", code, body)
		}
	})
}
