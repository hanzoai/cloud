package console

import "testing"

func TestSlugifyOrg(t *testing.T) {
	cases := map[string]string{
		"Acme Rockets":     "acme-rockets",
		"  Hello, World!  ": "hello-world",
		"UPPER_case-123":   "upper-case-123",
		"多 bytes 混":        "bytes", // non-ASCII runes act as separators (slug is ASCII)
		"---":              "",
		"":                 "",
		"a.b.c":            "a-b-c",
	}
	for in, want := range cases {
		if got := slugifyOrg(in); got != want {
			t.Errorf("slugifyOrg(%q) = %q, want %q", in, got, want)
		}
	}
	// A name longer than the cap is truncated and re-trimmed (no trailing dash).
	long := "this-is-a-very-long-organization-name-that-exceeds-the-sixty-character-cap-easily"
	got := slugifyOrg(long)
	if len(got) > maxOrgSlug {
		t.Errorf("slugifyOrg over-cap: len=%d > %d (%q)", len(got), maxOrgSlug, got)
	}
	if got != "" && got[len(got)-1] == '-' {
		t.Errorf("slugifyOrg left a trailing dash after slice: %q", got)
	}
}

func TestValidateOrgName(t *testing.T) {
	if v := validateOrgName("a"); v.ok {
		t.Errorf("single char should be rejected (min %d): %+v", minOrgSlug, v)
	}
	if v := validateOrgName("!!"); v.ok {
		t.Errorf("no-usable-chars should be rejected: %+v", v)
	}
	for _, reserved := range []string{"admin", "Hanzo", "built-in", "lux", "zoo", "pars", "app"} {
		if v := validateOrgName(reserved); v.ok {
			t.Errorf("reserved %q should be rejected: %+v", reserved, v)
		}
	}
	v := validateOrgName("Cool Team")
	if !v.ok || v.slug != "cool-team" {
		t.Errorf("valid name: want ok cool-team, got %+v", v)
	}
}

func TestPersonalOrgSlug(t *testing.T) {
	cases := map[string]string{
		"dave@example.com": "dave",
		"Alice.Smith":      "alice-smith",
		"bob":              "bob",
		"@leading":         "-leading"[1:], // slugified: "leading"
	}
	for in, want := range cases {
		if got := personalOrgSlug(in); got != want {
			t.Errorf("personalOrgSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"dave.smith@x.com": "Dave Smith",
		"alice":            "Alice",
		"bob_jones":        "Bob Jones",
		"@":                "Personal",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsReservedOrg(t *testing.T) {
	if !isReservedOrg("admin") || !isReservedOrg("hanzo") {
		t.Fatal("admin/hanzo must be reserved")
	}
	if isReservedOrg("acme") {
		t.Fatal("acme must not be reserved")
	}
}
