package provider

// codebase_test.go — where a GitHub repository lands in the org's code
// workspace when it is brought in, said on every row Sync lists.

import "testing"

func TestARepositoryRowNamesTheCodebaseItLandsAs(t *testing.T) {
	r := githubRepo{FullName: "Webby-AI/Intel-Hub", Name: "Intel-Hub"}
	r.Owner.Login = "Webby-AI"
	if got := item("acme", r).Codebase; got != "webby-ai/intel-hub" {
		t.Fatalf("codebase = %q, want webby-ai/intel-hub", got)
	}
	estate := githubRepo{FullName: "hanzo-inc/cloud", Name: "cloud"}
	estate.Owner.Login = "hanzo-inc"
	if got := item("hanzo", estate).Codebase; got != "hanzo-inc/cloud" {
		t.Fatalf("estate codebase = %q, want hanzo-inc/cloud", got)
	}
}

func TestAnImportNamesTheCodebasesItLandsAs(t *testing.T) {
	items := []githubImportItem{{Name: "intel-hub", Owner: "webby-ai"}, {Name: "Site", Owner: "Acme"}}
	if got := landing("acme", items); len(got) != 2 || got[0] != "webby-ai/intel-hub" || got[1] != "acme/site" {
		t.Fatalf("codebases = %v", got)
	}
	estateItems := []githubImportItem{{Name: "Cloud", Owner: "hanzo-inc"}, {Name: "Site", Owner: "Acme"}}
	if got := landing("hanzo", estateItems); len(got) != 2 || got[0] != "hanzo-inc/cloud" || got[1] != "acme/site" {
		t.Fatalf("estate codebases = %v", got)
	}
}
