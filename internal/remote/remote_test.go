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

func TestCleanDropsTheCredential(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "",
		"https://github.com/hanzoai/x.git": "https://github.com/hanzoai/x.git",
		"https://x-access-token:gho_abc123@github.com/hanzoai/x.git": "https://github.com/hanzoai/x.git",
		"https://oauth2:glpat-abc@gitlab.com/g/x":                    "https://gitlab.com/g/x",
		"https://ghp_abc123@github.com/hanzoai/x":                    "https://github.com/hanzoai/x",
		"HTTPS://u:p@github.com/hanzoai/x":                           "HTTPS://github.com/hanzoai/x",
		"http://u:p%40q@git.example.com:8080/x?ref=main#f":           "http://git.example.com:8080/x?ref=main#f",
		"https://u:p@q@github.com/hanzoai/x":                         "https://github.com/hanzoai/x",
		"  https://x-access-token:t@github.com/hanzoai/x  ":          "https://github.com/hanzoai/x",
		"https://github.com/hanzoai/x@v1":                            "https://github.com/hanzoai/x@v1",
		"ssh://git@github.com/hanzoai/x.git":                         "ssh://git@github.com/hanzoai/x.git",
		"ssh://git:secret@github.com/hanzoai/x.git":                  "ssh://git@github.com/hanzoai/x.git",
		"git@github.com:hanzoai/x.git":                               "git@github.com:hanzoai/x.git",
		"x-access-token:gho_abc@github.com:hanzoai/x.git":            "x-access-token@github.com:hanzoai/x.git",
		"hanzoai/x": "hanzoai/x",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOwnerNamesTheAccount(t *testing.T) {
	for in, want := range map[string]string{
		"":                                        "",
		"https://github.com/zooai/ngo":            "zooai",
		"https://github.com/ZooAI/ngo.git":        "zooai",
		"git@github.com:luxfi/ui.git":             "luxfi",
		"ssh://git@github.com/zooai/ngo":          "zooai",
		"https://x-access-token:t@github.com/a/b": "a",
		"https://git.hanzo.ai/hanzoai/hanzo.ai":   "hanzoai",
		"https://github.com/zooai":                "",
		"https://github.com/":                     "",
		"https://github.com":                      "",
		"ngo":                                     "",
	} {
		if got := Owner(in); got != want {
			t.Errorf("Owner(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHostNamesTheForge(t *testing.T) {
	for in, want := range map[string]string{
		"":                                        "",
		"https://github.com/zooai/ngo":            "github.com",
		"https://GitHub.com:443/zooai/ngo":        "github.com",
		"git@github.com:luxfi/ui.git":             "github.com",
		"ssh://git@github.com/zooai/ngo":          "github.com",
		"https://x-access-token:t@github.com/a/b": "github.com",
		"https://evil.example/github.com/a/b":     "evil.example",
		"https://git.hanzo.ai/hanzoai/hanzo.ai":   "git.hanzo.ai",
		"ngo":                                     "",
	} {
		if got := Host(in); got != want {
			t.Errorf("Host(%q) = %q, want %q", in, got, want)
		}
	}
}
