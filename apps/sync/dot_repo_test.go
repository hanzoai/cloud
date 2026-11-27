package sync

import "testing"

func TestDotRepositoryImport(t *testing.T) {
	for _, name := range []string{".github", ".profile"} {
		got, err := newRepo("hanzoai", name)
		if err != nil || got.flat() != "hanzoai_"+name {
			t.Fatalf("%s: %#v %v", name, got, err)
		}
		if _, err := validateGitSource(endpointReq{Provider: "github", Locator: "https://github.com/hanzoai/" + name + ".git"}); err != nil {
			t.Fatalf("source %s: %v", name, err)
		}
	}
	for _, name := range []string{".", "..", "../repo", "a/b", "_repo"} {
		if _, err := newRepo("hanzoai", name); err == nil {
			t.Fatalf("unsafe or ambiguous name accepted: %s", name)
		}
	}
}
