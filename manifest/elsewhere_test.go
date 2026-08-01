package manifest

import (
	"slices"
	"testing"
)

// ai's row is /v1 and it is LAST, so everything an earlier app claims is
// elsewhere for it — which is what keeps its door from publishing the fifteen of
// its own registrations the fleet delivers to a sibling.
func TestElsewhereIsWhatAnEarlierAppClaimed(t *testing.T) {
	got := Elsewhere("ai")
	if len(got) == 0 {
		t.Fatal("ai claims the /v1 remainder and every other app is in front of it")
	}
	for _, want := range []string{"/v1/admin", "/v1/metrics", "/v1/search", "/v1/crawl"} {
		if !slices.Contains(got, want) {
			t.Errorf("%q missing — ai registers routes under it and the fleet delivers them elsewhere", want)
		}
	}
	if slices.Contains(got, "/v1") {
		t.Error("ai's own prefix is not elsewhere for ai")
	}
}

// The FIRST app claims the remainder of nothing: an app at the head of the list
// wins every prefix it names, so there is no earlier row to yield to.
func TestElsewhereIsEmptyForTheFirstApp(t *testing.T) {
	if got := Elsewhere(Apps[0].Name); len(got) != 0 {
		t.Errorf("Elsewhere(%q) = %v, want none", Apps[0].Name, got)
	}
	if got := Elsewhere("no-such-app"); got != nil {
		t.Errorf("Elsewhere of an unknown name = %v, want nil", got)
	}
}
