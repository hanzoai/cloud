package leaderboard

import (
	"strings"
	"testing"
)

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
}

// TestBackfillBeforeStillRejectsGarbage pins that `before` is parsed by the HANDLER,
// not by the binder: it is a string field, so an unparseable value reaches
// time.Parse and answers the same 400 it always did — rather than being coerced to a
// zero time and silently seeding the whole history.
func TestBackfillBeforeStillRejectsGarbage(t *testing.T) {
	installFakeDS(t, nil)
	app := mountApp(t)
	super := withHeader(principalHeaders("admin", "root"), "X-User-IsAdmin", "true")

	if code, body := doJSON(t, app, "POST", "/v1/admin/leaderboard/rollup?before=lastweek", super, nil); code != 400 {
		t.Fatalf("unparseable before want 400, got %d (%s)", code, body)
	}
}
