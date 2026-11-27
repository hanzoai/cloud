package forge

import (
	"strings"
	"testing"
)

// A git transfer goes to the cluster origin when one is named, keeps the public
// origin otherwise, and never sends the credential in clear off the cluster.
func TestTransfersReachTheForgeFromInsideItsCluster(t *testing.T) {
	c, err := New("git.hanzo.ai", "tok")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ env, want, refuse string }{
		{"", "https://git.hanzo.ai/hanzoai/hanzoai_team.git", ""},
		{"http://hanzo-git.hanzo.svc", "http://hanzo-git.hanzo.svc/hanzoai/hanzoai_team.git", ""},
		{"http://hanzo-git.hanzo.svc.cluster.local:80/", "http://hanzo-git.hanzo.svc.cluster.local:80/hanzoai/hanzoai_team.git", ""},
		{"https://forge.internal", "https://forge.internal/hanzoai/hanzoai_team.git", ""},
		{"http://git.hanzo.ai", "", "in clear"},
		{"http://10.43.0.9", "", "in clear"},
		{"http://hanzo-git.hanzo.svc/v1", "", "not an origin"},
	}
	for _, k := range cases {
		t.Setenv(TransferEnv, k.env)
		r, err := c.Remote("hanzoai", "hanzoai_team")
		if k.refuse != "" {
			if err == nil || !strings.Contains(err.Error(), k.refuse) {
				t.Errorf("%s=%q: err %v, want one saying %q", TransferEnv, k.env, err, k.refuse)
			}
			continue
		}
		if err != nil || r.URL != k.want {
			t.Errorf("%s=%q: %q, %v; want %q", TransferEnv, k.env, r.URL, err, k.want)
		}
	}
}

func TestLocalIsLoopbackOrAClusterService(t *testing.T) {
	for h, want := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "::1": true,
		"hanzo-git.hanzo.svc": true, "hanzo-git.hanzo.svc.cluster.local.": true,
		"git.hanzo.ai": false, "10.43.0.9": false, "svc": false, "evil.svc.example.com": false,
	} {
		if Local(h) != want {
			t.Errorf("Local(%q) = %v, want %v", h, !want, want)
		}
	}
}
