package integrations

import (
	"strings"
	"testing"
)

// TestOnlyArrivalsAreOffered pins the distinction the handler exists for: an offer
// follows a repo ENTERING the granted set. A departure must not raise one, because
// a todo pointing at a repo the installation can no longer read is work nobody can
// do — and `renamed` is excluded for a different reason: an offer already stands
// under the old name, and a second one would read as two repos.
func TestOnlyArrivalsAreOffered(t *testing.T) {
	for _, a := range []string{"created", "transferred", "unarchived"} {
		if !offersImport(a) {
			t.Errorf("%q puts a repo in the granted set and must be offered", a)
		}
	}
	for _, a := range []string{"deleted", "archived", "privatized", "renamed", "edited", ""} {
		if offersImport(a) {
			t.Errorf("%q is not an arrival and must not raise an offer", a)
		}
	}
}

// TestTheOfferSaysHowToAcceptIt is what makes the todo actionable rather than a
// notification: whoever opens it can see the repo, how it arrived, and the exact
// call that imports it, without leaving the item.
func TestTheOfferSaysHowToAcceptIt(t *testing.T) {
	got := importOffer("acme/widgets", "https://github.com/acme/widgets", true, "created")
	for _, want := range []string{
		"acme/widgets",
		"created",
		"private",
		"https://github.com/acme/widgets",
		"/v1/integrations/github/import",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the offer must name %q so it can be acted on; got:\n%s", want, got)
		}
	}
	// A public repo must not be described as private — the visibility is the whole
	// reason an org treats two otherwise identical repos differently.
	if pub := importOffer("acme/open", "", false, "transferred"); strings.Contains(pub, "private") {
		t.Errorf("a public repo must not read as private; got:\n%s", pub)
	}
}

// TestAnOfferSurvivesAMissingURL keeps the description honest when GitHub omits
// html_url rather than emitting a dangling blank line where a link should be.
func TestAnOfferSurvivesAMissingURL(t *testing.T) {
	got := importOffer("acme/widgets", "", false, "created")
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("no url must not leave an empty gap; got:\n%q", got)
	}
	if !strings.Contains(got, "acme/widgets") {
		t.Error("the repo must still be named")
	}
}
