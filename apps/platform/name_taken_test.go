package platform

// name_taken_test.go — a deploy whose name someone else holds in the one public
// namespace takes a name of the org's own instead of failing (#769), and the
// same deploy asked again lands on the same app.

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// namespacePeer is the project app's half of the one public namespace: who holds
// each name, as org/holder, and every name it was asked for.
type namespacePeer struct {
	mu    sync.Mutex
	held  map[string]string
	asked []string
}

func (p *namespacePeer) holder(name string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held[name]
}

// serveNamespace stands the project app's claim and release up beside the
// provider peer newDeployHarness serves, in the same runtime directory.
func serveNamespace(t *testing.T, held map[string]string) *namespacePeer {
	t.Helper()
	p := &namespacePeer{held: held}
	project := zip.New(zip.Config{AppName: projectpeer.App, DisableStartupMessage: true})
	project.Post("/sites/claim", func(ctx context.Context, in *client.NameIn) (*client.NameOut, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.asked = append(p.asked, in.Name)
		who := zip.CallerOf(ctx).Org + "/" + in.Holder
		if h, ok := p.held[in.Name]; ok && h != who {
			return &client.NameOut{Taken: true}, nil
		}
		p.held[in.Name] = who
		return &client.NameOut{}, nil
	}, zip.WithOperationID(client.SitesClaim))
	go func() { _ = project.Listen(zip.SocketPath(projectpeer.App)) }()
	t.Cleanup(func() { _ = project.Shutdown() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if up, err := plane.Listening(zip.SocketPath(projectpeer.App)); err == nil && up {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the project peer never came up")
	return nil
}

// TestATakenNameBecomesOneOfTheOrgsOwn is #769: "Add to project" on a repository
// named `web`, when web.hanzo.app is another org's, used to answer 409. It now
// declares the app as web-acme, says so, and a second deploy of the same
// repository (the dialog's "Ship to main" sends the same name again) lands on
// web-acme rather than minting a third name.
func TestATakenNameBecomesOneOfTheOrgsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		ns := serveNamespace(t, map[string]string{"web": "globex/" + holder("web")})

		out := h.deploy(t, true, map[string]any{"repo": "acme-gh/web", "partOf": "web", "mode": "branch"})
		if out.App.Name != "web-acme" {
			t.Fatalf("a taken name must become the org's own: app %q", out.App.Name)
		}
		if len(out.App.Hosts) != 1 || out.App.Hosts[0] != "web-acme.hanzo.app" {
			t.Fatalf("the app answers at its own name: %v", out.App.Hosts)
		}
		if out.App.PartOf != "web" {
			t.Fatalf("the project it joins is unchanged: %q", out.App.PartOf)
		}
		if !strings.Contains(out.Notice, "web.hanzo.app is taken") || !strings.Contains(out.Notice, "web-acme.hanzo.app") {
			t.Fatalf("the rename must be said: %q", out.Notice)
		}
		if got := ns.holder("web-acme"); got != "acme/"+holder("web-acme") {
			t.Fatalf("web-acme must be held by acme's app, held by %q", got)
		}
		if got := ns.holder("web"); got != "globex/"+holder("web") {
			t.Fatalf("the taken name stays its holder's: %q", got)
		}

		again := h.deploy(t, true, map[string]any{"repo": "acme-gh/web", "name": "web", "partOf": "web", "mode": "branch"})
		if again.App.Name != "web-acme" {
			t.Fatalf("the same deploy asked again must land on the same app, got %q", again.App.Name)
		}

		// An address the caller NAMED is theirs to have or not: still 409.
		code, body := h.post(t, true, map[string]any{"repo": "acme-gh/web", "name": "web", "host": "web.hanzo.app", "mode": "branch"})
		if code != http.StatusConflict {
			t.Fatalf("a named host that is taken want 409, got %d %s", code, body)
		}
	})
}

// TestTheOrgsOwnNameIsNumberedWhenThatIsTakenToo: web and web-acme both held
// elsewhere, so the app is web-acme-2.
func TestTheOrgsOwnNameIsNumberedWhenThatIsTakenToo(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newDeployHarness(t)
		serveNamespace(t, map[string]string{
			"web":      "globex/" + holder("web"),
			"web-acme": "initech/" + holder("web-acme"),
		})
		out := h.deploy(t, true, map[string]any{"repo": "acme-gh/web", "mode": "branch"})
		if out.App.Name != "web-acme-2" {
			t.Fatalf("want web-acme-2, got %q", out.App.Name)
		}
	})
}

// TestTheOrgsOwnNameFitsALabel: the name stays a DNS label of at most 40
// characters however long the org's name is.
func TestTheOrgsOwnNameFitsALabel(t *testing.T) {
	for _, tc := range []struct{ name, org, want string }{
		{"web", "acme", "web-acme"},
		{"site", "jamessburnss1313", "site-jamessburnss1313"},
		{"a-very-long-application-name-x", "an-org-with-a-long-name", "a-very-long-application-name-x-an-org-wi"},
		// The cut lands on a dash, which a label may not end with.
		{"app", "abcdefghij-abcdefghij-abcdefghij-ab-cd", "app-abcdefghij-abcdefghij-abcdefghij-ab"},
	} {
		got := ownNames(tc.name, tc.org)
		if len(got) < 3 || got[0] != tc.name || got[1] != tc.want {
			t.Errorf("ownNames(%q, %q) = %v, want %q second", tc.name, tc.org, got, tc.want)
		}
		for _, n := range got {
			if !slugRE.MatchString(n) {
				t.Errorf("ownNames(%q, %q) offered %q, not a DNS label of at most 40", tc.name, tc.org, n)
			}
		}
	}
}
