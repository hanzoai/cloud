package ossfunding

import "testing"

func TestFromPURL(t *testing.T) {
	cases := []struct {
		purl       string
		wantKind   Kind
		wantHandle string
	}{
		{"pkg:golang/github.com/gin-gonic/gin@v1.12.0", KindGitHubSponsors, "gin-gonic"},
		{"pkg:golang/github.com/google/uuid@v1.6.0", KindGitHubSponsors, "google"},
		{"pkg:golang/golang.org/x/sys@v0.20.0", KindGitHubSponsors, "golang"},
		{"pkg:golang/gitlab.com/acme/widget@v2", KindGitHubSponsors, "acme"},
		{"pkg:npm/react@18.3.1", KindUnresolved, ""},
		{"pkg:pypi/fastapi@0.110.0", KindUnresolved, ""},
		{"", KindUnresolved, ""},
	}
	for _, tc := range cases {
		t.Run(tc.purl, func(t *testing.T) {
			got := FromPURL(tc.purl)
			if got.Kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Handle != tc.wantHandle {
				t.Fatalf("handle = %q, want %q", got.Handle, tc.wantHandle)
			}
		})
	}
}

func TestTargetString(t *testing.T) {
	if got := (Target{Kind: KindGitHubSponsors, Handle: "gin-gonic"}).String(); got != "github_sponsors:gin-gonic" {
		t.Fatalf("String() = %q", got)
	}
	if got := Unresolved().String(); got != "unresolved" {
		t.Fatalf("Unresolved String() = %q", got)
	}
}

func TestPURLResolver(t *testing.T) {
	r := PURLResolver{}
	if got := r.Resolve("pkg:golang/github.com/gin-gonic/gin@v1"); got.Handle != "gin-gonic" {
		t.Fatalf("resolver handle = %q", got.Handle)
	}
}
