package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// ── the fence lives in another repository, so the write path VERIFIES it ────

// An unreadable, unparseable or fence-less template is a REFUSAL, never a pass.
// "The rule could not be read" and "the rule agrees" are opposite facts, and the
// refusal covers every directory — including a reserved one, because a fence
// that cannot be evaluated cannot be confirmed for anybody.
//
// This is the property the first cut of checkFence did NOT have. It refused one
// known-bad substring, so every OTHER unsafe template passed it: red found six,
// and they are the table below. A denylist of one is not a check.
func TestFenceRefusesEveryTemplateItCannotConfirm(t *testing.T) {
	const stalePrefixRule = "spec:\n  template:\n    spec:\n      project: '{{ if hasPrefix \"tenant-\" .path.basename }}{{ .path.basename }}{{ else }}hanzo-platform{{ end }}'\n"
	for _, tc := range []struct{ name, body string }{
		{"absent", ""},
		{"empty file", " "},
		{"garbage", "%%% not yaml : : :"},
		{"no project field", "apiVersion: apps.hanzo.ai/v1\nkind: ApplicationSet\nspec:\n  template:\n    spec: {}\n"},
		{"unconditional platform fence", "spec:\n  template:\n    spec:\n      project: hanzo-platform\n"},
		{"the stale prefix rule", stalePrefixRule},
		{"template that does not parse", "spec:\n  template:\n    spec:\n      project: '{{ if }}'\n"},
		{"fence chosen by the file being fenced", "spec:\n  template:\n    spec:\n      project: '{{ .cd.project }}'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.body != "" {
				if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(filepath.Dir(fleetSet))), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(fleetSet)), []byte(tc.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkFence(root, "acme"); err == nil {
				t.Fatal("a commit to main was allowed against a template that cannot fence \"acme\" correctly")
			}
		})
	}
}

// And the positive: a template that DOES derive the reported fence is admitted,
// for a customer org and for a reserved one alike.
func TestFenceAdmitsATemplateThatDerivesTheReportedFence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(filepath.Dir(fleetSet))), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(fleetSet)), []byte(fleetSetFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, org := range []string{"acme", "widgets-co", "hanzo", "admin"} {
		if err := checkFence(root, org); err != nil {
			t.Errorf("checkFence(%q): %v", org, err)
		}
		got, err := fenceOf(root, org)
		if err != nil {
			t.Fatalf("fenceOf(%q): %v", org, err)
		}
		if got != declareProject(org) {
			t.Errorf("the template fences %q under %q, this API reports %q", org, got, declareProject(org))
		}
	}
}
