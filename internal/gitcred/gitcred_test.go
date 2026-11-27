package gitcred

import "testing"

// A host that differs only by case, padding or a character a variable name cannot
// hold is one credential slot, or a deployment would set one name and a client
// read another.
func TestEnvNamesTheHost(t *testing.T) {
	for host, want := range map[string]string{
		"github.com":       "GIT_MIRROR_TOKEN_GITHUB_COM",
		"GitHub.com":       "GIT_MIRROR_TOKEN_GITHUB_COM",
		" github.com ":     "GIT_MIRROR_TOKEN_GITHUB_COM",
		"git.hanzo.ai":     "GIT_MIRROR_TOKEN_GIT_HANZO_AI",
		"git-host.example": "GIT_MIRROR_TOKEN_GIT_HOST_EXAMPLE",
		"127.0.0.1":        "GIT_MIRROR_TOKEN_127_0_0_1",
		"host:8443":        "GIT_MIRROR_TOKEN_HOST_8443",
	} {
		if got := Env(host); got != want {
			t.Errorf("Env(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestForReadsOnlyTheNamedHost(t *testing.T) {
	t.Setenv("GIT_MIRROR_TOKEN_GITHUB_COM", " gh-token ")
	t.Setenv("GIT_MIRROR_TOKEN_GIT_HANZO_AI", "")
	if got := For("github.com"); got != "gh-token" {
		t.Fatalf("For(github.com) = %q", got)
	}
	if got := For("git.hanzo.ai"); got != "" {
		t.Fatalf("a host holding nothing answered %q", got)
	}
	if got := For("gitlab.com"); got != "" {
		t.Fatalf("an unnamed host answered %q", got)
	}
	if got := For(""); got != "" {
		t.Fatalf("an empty host answered %q", got)
	}
}
