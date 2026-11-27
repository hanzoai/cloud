package billing

import "testing"

// The country is the request's own statement, else the edge's; a pseudo-code or a
// malformed one is not a country.
func TestTheBuyersCountryIsTheirsOrTheEdges(t *testing.T) {
	for stated, want := range map[string]string{"br": "BR", " nl ": "NL", "XX": "", "T1": "", "usa": "", "1a": ""} {
		if got := normalCountry(stated); got != want {
			t.Errorf("%q -> %q, want %q", stated, got, want)
		}
	}
}
