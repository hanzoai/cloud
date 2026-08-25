package remote

import "testing"

func TestProviderNamesTheForge(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "",
		"https://github.com/hanzoai/x": "github",
		"git@github.com:hanzoai/x.git": "github",
		"https://gitlab.com/g/x":       "gitlab",
		"https://bitbucket.org/b/x":    "bitbucket",
		"https://git.example.com/x":    "git",
	} {
		if got := Provider(in); got != want {
			t.Errorf("Provider(%q) = %q, want %q", in, got, want)
		}
	}
}
