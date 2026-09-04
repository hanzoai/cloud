package ask

// git_test.go pins the ROLLUP: what the forge's repository inventory becomes
// when a founder asks about their source. Every figure here is stated verbatim
// by the advisor above, so a wrong one is not a rendering bug — it is the
// advisor telling somebody a false number about their own company.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
)

// twelveRepos is an inventory of 12 repositories totalling 48 MiB, which is what
// the wired-domain test asserts the advisor states.
func twelveRepos() []forge.Repo {
	out := make([]forge.Repo, 0, 12)
	for i := 0; i < 12; i++ {
		out = append(out, forge.Repo{Name: "r" + string(rune('a'+i)), Size: 4096, UpdatedAt: time.Now()})
	}
	return out
}

// figuresOf reads the rollup for one org against a fixed inventory.
func figuresOf(t *testing.T, repos []forge.Repo) map[string]client.Figure {
	t.Helper()
	stubInventory(t, byRepo{"acme": repos})
	out, err := gitFigures(cloud.For(context.Background(), "acme"), &client.FiguresIn{})
	if err != nil {
		t.Fatalf("gitFigures: %v", err)
	}
	by := map[string]client.Figure{}
	for _, f := range out.Figures {
		by[f.Label] = f
	}
	return by
}

// TestGitFiguresRollup is the arithmetic, and each number is a different
// question a founder asks in words: how much have we got, how big is it, how
// much of it moved this month, what did we touch last.
func TestGitFiguresRollup(t *testing.T) {
	now := time.Now()
	got := figuresOf(t, []forge.Repo{
		{Name: "cloud", Size: 51200, UpdatedAt: now.Add(-2 * time.Hour)},         // 50 MiB, today
		{Name: "universe", Size: 1024, UpdatedAt: now.Add(-10 * 24 * time.Hour)}, // 1 MiB, this month
		{Name: "attic", Size: 512, UpdatedAt: now.Add(-400 * 24 * time.Hour)},    // half a MiB, long ago
	})

	for label, want := range map[string]string{
		"Repositories":          "3",
		"Code stored":           "51.5 MB",
		"Repositories updated":  "2", // the 400-day-old one is outside the window
		"Most recently updated": "cloud",
	} {
		if got[label].Value != want {
			t.Errorf("%s = %q, want %q", label, got[label].Value, want)
		}
	}
	if p := got["Repositories updated"].Period; p != "last 30 days" {
		t.Errorf("the window is stated as %q", p)
	}
	if p := got["Most recently updated"].Period; p != now.Add(-2*time.Hour).UTC().Format("2006-01-02") {
		t.Errorf("the newest repo's date = %q", p)
	}
}

// TestGitFiguresEmptyOrgIsAnAnswer proves an org with no repositories reports
// zero rather than failing. "You have no repositories" is true and useful; only
// a failure to find out is an error, and the advisor tells the two apart.
func TestGitFiguresEmptyOrgIsAnAnswer(t *testing.T) {
	got := figuresOf(t, nil)
	if got["Repositories"].Value != "0" {
		t.Fatalf("Repositories = %q, want 0", got["Repositories"].Value)
	}
	// And NOT a figure labelled "Most recently updated" with nothing after it —
	// the advisor states figures verbatim and would narrate the blank.
	if _, ok := got["Most recently updated"]; ok {
		t.Fatalf("an empty org was handed a most-recently-updated figure")
	}
}

// TestGitFiguresAnonymousIsRefused is the fail-closed spine of the domain: with
// no caller there is no org, and an org-less rollup would be somebody's figures
// answered to nobody.
func TestGitFiguresAnonymousIsRefused(t *testing.T) {
	prev := inventory
	reached := false
	inventory = func(context.Context) ([]forge.Repo, error) { reached = true; return nil, nil }
	t.Cleanup(func() { inventory = prev })

	if _, err := forgeRepos(context.Background()); err == nil {
		t.Fatal("an anonymous rollup was answered")
	}
	if reached {
		t.Fatal("an anonymous caller reached the forge")
	}
}

// TestForgeReposRefusesAnUnmappedTenant pins the first of the two tenancy
// controls. An IAM org reaches a forge namespace ONLY through forge.Owner's
// closed table — never by being spelled like one — and it is refused before a
// credential is spent on it. The second control is the sudo actor, which
// forge/tree_test.go pins at the wire.
func TestForgeReposRefusesAnUnmappedTenant(t *testing.T) {
	for _, org := range []string{"hanzoai", "acme", "admin"} {
		_, err := forgeRepos(cloud.For(context.Background(), org))
		if err == nil {
			t.Fatalf("org %q reached the forge", org)
		}
		if !errors.Is(err, forge.ErrNoOwner) {
			t.Fatalf("org %q was refused as %v, want ErrNoOwner", org, err)
		}
	}
}

// TestBytesReadsLikeAPerson fixes the one formatting rule this domain owns. The
// value crosses the wire already formatted (client.Figure), so a second spelling
// downstream is how one number starts disagreeing with the page it came from.
func TestBytesReadsLikeAPerson(t *testing.T) {
	for n, want := range map[int64]string{
		0:             "0 B",
		512:           "512 B",
		1024:          "1.0 KB",
		1536:          "1.5 KB",
		48 << 20:      "48.0 MB",
		3 * (1 << 30): "3.0 GB",
	} {
		if got := bytes(n); got != want {
			t.Errorf("bytes(%d) = %q, want %q", n, got, want)
		}
	}
}
