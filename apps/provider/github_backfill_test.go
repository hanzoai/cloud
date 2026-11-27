package provider

import "testing"

func TestBackfillWants(t *testing.T) {
	r := githubRepo{Name: "api", FullName: "acme/api"}
	for _, c := range []struct {
		repo string
		want bool
	}{
		{"", true},
		{"  ", true},
		{"api", true},
		{"API", true},
		{"acme/api", true},
		{"Acme/API", true},
		{"other/api", false},
		{"web", false},
		{"acme/web", false},
	} {
		if got := backfillWants(r, c.repo); got != c.want {
			t.Errorf("backfillWants(%q) = %v, want %v", c.repo, got, c.want)
		}
	}
}
