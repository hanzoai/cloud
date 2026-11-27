package content

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
)

// TestBoardLimitTolerance pins what `?limit=` means on the typed board op.
//
// Untyped, an unparseable or non-positive limit fell through limitOf to the default —
// a caller's typo about ONE filter was never a reason to refuse the read. Typed,
// zip's URL binder leaves an int field at ZERO for a value it cannot parse, which is
// the same fact reaching the handler by a different road, and the handler still reads
// a non-positive value as "take the default". Either half slipping would be a silent
// wire change: a binder that 400'd refuses a call that used to work, and a handler
// that read 0 as "return nothing" empties a board that used to have rows.
func TestBoardLimitTolerance(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	installMarketing(t, app, org)
	mounted.State.gen = fakeGenerator{}

	// Two real drafts, so "the board has rows" is a fact this test can lose.
	for _, title := range []string{"first", "second"} {
		if code, b := req(t, app, http.MethodPost, "/v1/content/generate", org,
			map[string]any{"doctype": DocTypeCampaign, "title": title, "brief": title}); code != http.StatusCreated {
			t.Fatalf("seed %q: %d (%s)", title, code, b)
		}
	}

	want := boardCount(t, app, org, "/v1/content/board")
	if want == 0 {
		t.Fatal("seeded board is empty — the tolerance assertions below would prove nothing")
	}

	// Every one of these is "no usable limit given" and must answer the default page.
	for _, q := range []string{"?limit=abc", "?limit=", "?limit=0", "?limit=-1", "?limit=1e3"} {
		if got := boardCount(t, app, org, "/v1/content/board"+q); got != want {
			t.Fatalf("limit%q: want the default page (%d rows), got %d", q, want, got)
		}
	}

	// A real limit still narrows the page, so the tolerance above is the default
	// applying rather than the limit being ignored outright.
	if got := boardCount(t, app, org, "/v1/content/board?limit=1"); got != 1 {
		t.Fatalf("limit=1 want 1 row, got %d", got)
	}
}

// boardCount reads the board and returns its row count, failing on any non-200.
func boardCount(t *testing.T, app *zip.App, org, path string) int {
	t.Helper()
	code, b := req(t, app, http.MethodGet, path, org, nil)
	if code != http.StatusOK {
		t.Fatalf("GET %s want 200, got %d (%s)", path, code, b)
	}
	var page boardPage
	if err := json.Unmarshal(b, &page); err != nil {
		t.Fatalf("GET %s json: %v (%s)", path, err, b)
	}
	return page.Count
}
