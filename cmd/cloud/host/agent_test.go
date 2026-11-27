package host

// The agent's door, asked of the host composed the way run() composes it: what an
// agent reads at /llms.txt and /skill.md, on which hosts, and that every endpoint
// the page names is one the published document carries.

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/sdk/brand"
	"sigs.k8s.io/yaml"

	"github.com/zap-proto/zip"
)

// knock sends one request to the host as a caller addressing host would.
func knock(t *testing.T, app *zip.App, method, host, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+host+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatalf("%s %s%s: %v", method, host, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

// marker is the sentence every page the door writes carries, so a test can tell the
// door's answer from whatever else answers the address.
const marker = "Read the full instructions first: "

// links is every absolute URL a page names.
var links = regexp.MustCompile(`https://[^\s)>"]+`)

// TestTheDoorNamesOnlyTheBrandItIsAskedOn: each brand host answers with its own
// name, API, console and manifest, and with no URL of another brand. Only the brand
// whose IAM opens an account for a first-seen wallet lists the wallet steps.
func TestTheDoorNamesOnlyTheBrandItIsAskedOn(t *testing.T) {
	app := host(t)
	for _, tc := range []struct {
		host, id, title, manifest string
		wallet                    bool
	}{
		{"api.hanzo.ai", "hanzo", "# Hanzo\n", "https://hanzoskills.com/hanzo.md", true},
		{"console.hanzo.ai", "hanzo", "# Hanzo\n", "https://hanzoskills.com/hanzo.md", true},
		{"api.lux.network", "lux", "# Lux\n", "https://lux.network/llms.txt", false},
		{"console.lux.cloud", "lux", "# Lux\n", "https://lux.network/llms.txt", false},
		{"api.zoo.ngo", "zoo", "# Zoo\n", "https://zoo.ngo/llms.txt", false},
	} {
		resp, body := knock(t, app, http.MethodGet, tc.host, openapi.LlmsPath)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, tc.title) {
			t.Fatalf("GET %s%s = %d %.80q, want the %s page", tc.host, openapi.LlmsPath, resp.StatusCode, body, tc.id)
		}
		if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
			t.Errorf("GET %s%s Content-Type = %q", tc.host, openapi.LlmsPath, got)
		}
		if !strings.Contains(body, marker+tc.manifest+"\n") {
			t.Errorf("GET %s%s does not send an agent to %s:\n%s", tc.host, openapi.LlmsPath, tc.manifest, body)
		}
		if strings.Contains(body, "/v1/iam/web3/") != tc.wallet {
			t.Errorf("GET %s%s lists the wallet steps = %v, want %v", tc.host, openapi.LlmsPath, !tc.wallet, tc.wallet)
		}
		for _, l := range links.FindAllString(body, -1) {
			u, err := url.Parse(l)
			if err != nil {
				t.Errorf("GET %s%s names %q, which is not a URL", tc.host, openapi.LlmsPath, l)
				continue
			}
			if other, ok := brand.ForHostOK(u.Hostname()); ok && other != tc.id {
				t.Errorf("GET %s%s names %s, a %s address, on a %s host", tc.host, openapi.LlmsPath, l, other, tc.id)
			}
		}

		resp, skill := knock(t, app, http.MethodGet, tc.host, openapi.SkillPath)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/markdown; charset=utf-8" {
			t.Errorf("GET %s%s = %d %q", tc.host, openapi.SkillPath, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if !strings.HasPrefix(skill, "---\nname: "+tc.id+"\ndescription: ") || !strings.HasSuffix(skill, "---\n\n"+body) {
			t.Errorf("GET %s%s is not the page under a skill's frontmatter:\n%s", tc.host, openapi.SkillPath, skill)
		}

		resp, head := knock(t, app, http.MethodHead, tc.host, openapi.LlmsPath)
		if resp.StatusCode != http.StatusOK || head != "" {
			t.Errorf("HEAD %s%s = %d with %d bytes", tc.host, openapi.LlmsPath, resp.StatusCode, len(head))
		}
	}
}

// TestTheDoorAnswersNobodyElse: a host no brand claims, a customer's site, a brand
// that publishes no manifest, another method and another path are all answered as
// they were before the door existed.
func TestTheDoorAnswersNobodyElse(t *testing.T) {
	app := host(t)
	for _, tc := range []struct{ method, host, path string }{
		{http.MethodGet, "cloud", openapi.LlmsPath},
		{http.MethodGet, "karma.style", openapi.LlmsPath},
		{http.MethodGet, "acme.hanzo.app", openapi.LlmsPath},
		{http.MethodGet, "acme.hanzo.app", openapi.SkillPath},
		{http.MethodGet, "api.pars.network", openapi.LlmsPath},
		{http.MethodPost, "api.hanzo.ai", openapi.LlmsPath},
		{http.MethodGet, "api.hanzo.ai", "/llms.txt/"},
		{http.MethodGet, "api.hanzo.ai", "/docs/llms.txt"},
	} {
		if _, body := knock(t, app, tc.method, tc.host, tc.path); strings.Contains(body, marker) {
			t.Errorf("%s %s%s was answered by the door", tc.method, tc.host, tc.path)
		}
	}
}

// TestTheDoorReadsTheHostTheEdgeWrote: behind the ingress the host trusts
// X-Forwarded-Host, which for a static site's /llms.txt is the site's own name,
// while the edge has rewritten Host to the API host of the brand that speaks for
// the site. The page is that brand's: the edge decided, and the door reads its
// decision rather than the client's name, whatever the forwarded header says.
func TestTheDoorReadsTheHostTheEdgeWrote(t *testing.T) {
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true,
		TrustProxy: true, TrustedProxies: []string{"0.0.0.0/0", "::/0"}, ProxyHeader: "X-Forwarded-For"})
	useAgent(app, nil)
	for _, tc := range []struct{ host, forwarded, title string }{
		{"api.lux.network", "lux.exchange", "# Lux\n"},
		{"api.hanzo.ai", "hanzo.chat", "# Hanzo\n"},
		{"api.zoo.ngo", "api.hanzo.ai", "# Zoo\n"},
	} {
		req, err := http.NewRequest(http.MethodGet, "http://"+tc.host+openapi.LlmsPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Forwarded-Host", tc.forwarded)
		resp, err := app.Test(req, deadline)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(b), tc.title) {
			t.Errorf("GET %s%s forwarded for %s = %d %.40q, want the page that begins %q",
				tc.host, openapi.LlmsPath, tc.forwarded, resp.StatusCode, b, tc.title)
		}
	}
}

// step is one numbered line of the wallet steps: the method and the address.
var step = regexp.MustCompile(`(?m)^\d\. (GET|POST) (https://[^\s?,]+)`)

// TestEveryStepIsAPublishedOperation: the steps are endpoint names, and each one is
// an operation the published document carries at that method. A renamed route
// fails here rather than in an agent's first call.
func TestEveryStepIsAPublishedOperation(t *testing.T) {
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v", golden, err)
	}
	var d struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := yaml.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	_, page := knock(t, host(t), http.MethodGet, "api.hanzo.ai", openapi.LlmsPath)
	steps := step.FindAllStringSubmatch(page, -1)
	if len(steps) != 8 { // nine lines, one of which is a signature and calls nothing
		t.Fatalf("the wallet steps name %d endpoints, want 8:\n%s", len(steps), page)
	}
	for _, s := range steps {
		u, err := url.Parse(s[2])
		if err != nil {
			t.Fatal(err)
		}
		if d.Paths[u.Path][strings.ToLower(s[1])] == nil {
			t.Errorf("step %s %s names no published operation", s[1], s[2])
		}
	}
}

// TestTheDoorSitsBetweenTheSiteEdgeAndTheConsole reads run() itself: after the
// site edge, so a customer's site answers its own /llms.txt, and before the
// console, whose catch-all would answer it with a shell.
func TestTheDoorSitsBetweenTheSiteEdgeAndTheConsole(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	run := body[strings.Index(body, "func run("):]
	sites, agent, console := strings.Index(run, "mountSites(app)"), strings.Index(run, "useAgent(app,"), strings.Index(run, "webui.Use(app")
	if sites < 0 || agent < 0 || console < 0 || !(sites < agent && agent < console) {
		t.Fatalf("run() composes the site edge at %d, the door at %d and the console at %d; want them in that order", sites, agent, console)
	}
}

// TestEveryAddressTheDoorNamesAnswers: on the host composed the way run()
// composes it, every address the sitemap lists, every address the API catalog
// names on the API host and every page the home page links there answers 200
// without a credential — the host's own documents from the host, each app's
// from that app. The MCP endpoint is linked for POST and is not asked by GET.
func TestEveryAddressTheDoorNamesAnswers(t *testing.T) {
	app := host(t)
	const api = "https://api.hanzo.ai"
	names := map[string]string{}
	resp, sitemap := knock(t, app, http.MethodGet, "api.hanzo.ai", openapi.SitemapPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", openapi.SitemapPath, resp.StatusCode)
	}
	for _, m := range regexp.MustCompile(`<loc>([^<]+)</loc>`).FindAllStringSubmatch(sitemap, -1) {
		names[m[1]] = "the sitemap"
	}
	if len(names) < 50 {
		t.Fatalf("the sitemap lists %d addresses; the fleet publishes over a hundred capabilities", len(names))
	}
	_, catalog := knock(t, app, http.MethodGet, "api.hanzo.ai", openapi.CatalogPath)
	for _, m := range regexp.MustCompile(`"href": "(`+api+`[^"]*)"`).FindAllStringSubmatch(catalog, -1) {
		names[m[1]] = "the API catalog"
	}
	req, _ := http.NewRequest(http.MethodGet, "http://api.hanzo.ai/", nil)
	req.Header.Set("Accept", "text/markdown")
	r, err := app.Test(req, deadline)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	for _, m := range regexp.MustCompile(`\]\((`+api+`[^)]*)\)`).FindAllStringSubmatch(string(home), -1) {
		if m[1] != api+manifest.MCPPath {
			names[m[1]] = "the home page"
		}
	}
	for u, by := range names {
		p, ok := strings.CutPrefix(u, api)
		if !ok {
			t.Errorf("%s names %s, which is not on the API host", by, u)
			continue
		}
		if resp, body := knock(t, app, http.MethodGet, "api.hanzo.ai", p); resp.StatusCode != http.StatusOK ||
			strings.Contains(body, "Hanzo Cloud Console") {
			t.Errorf("%s names %s, which answers %d %.60q", by, u, resp.StatusCode, body)
		}
	}
	t.Logf("%d addresses the door names, every one answering", len(names))
}

// TestTheHomePageIsTheAPIHostsAlone: the console keeps / on the hosts it is
// served at, and a host no brand claims gets neither.
func TestTheHomePageIsTheAPIHostsAlone(t *testing.T) {
	app := host(t)
	if resp, body := knock(t, app, http.MethodGet, "api.hanzo.ai", "/"); resp.StatusCode != http.StatusOK ||
		!strings.Contains(body, "<title>Hanzo API</title>") {
		t.Errorf("GET api.hanzo.ai/ = %d %.80q, want the API's home page", resp.StatusCode, body)
	}
	for _, h := range []string{"console.hanzo.ai", "cloud.hanzo.ai", "localhost"} {
		if _, body := knock(t, app, http.MethodGet, h, "/"); !strings.Contains(body, "Hanzo Cloud Console") {
			t.Errorf("GET %s/ = %.80q, want the console", h, body)
		}
	}
	for _, h := range []string{"acme.hanzo.app", "karma.style"} {
		if _, body := knock(t, app, http.MethodGet, h, "/"); strings.Contains(body, "Hanzo API") {
			t.Errorf("GET %s/ answered the API's home page", h)
		}
	}
}
