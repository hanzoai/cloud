package provider

// codebase_test.go — where a GitHub repository lands in the org's code
// workspace when it is brought in, said on every row Sync lists.

import "testing"

func TestARepositoryRowNamesTheCodebaseItLandsAs(t *testing.T) {
	r := githubRepo{FullName: "Webby-AI/Intel-Hub", Name: "Intel-Hub"}
	r.Owner.Login = "Webby-AI"
	if got := item(r).Codebase; got != "webby-ai_intel-hub" {
		t.Fatalf("codebase = %q", got)
	}
}

func TestAnImportNamesTheCodebasesItLandsAs(t *testing.T) {
	items := []githubImportItem{{Name: "intel-hub", Owner: "webby-ai"}, {Name: "Site", Owner: "Acme"}}
	if got := landing(items); len(got) != 2 || got[0] != "webby-ai_intel-hub" || got[1] != "acme_site" {
		t.Fatalf("codebases = %v", got)
	}
}
