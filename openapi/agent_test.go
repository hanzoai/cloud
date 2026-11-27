package openapi

// The agent's door: every row is a typed operation of the host's capability, the
// edge answers each one where its scope holds and nowhere else, and every
// address a document names is one the door, the index or the document carries.

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/manifest/a2a"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/mcp"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// doored is a host with the index, the document and the door over [parts],
// composed the way cmd/cloud composes them, and a catch-all behind them so a
// request the door yields is visibly not the door's.
func doored(t *testing.T) *zip.App {
	t.Helper()
	app := newApp()
	subsets := func() ([]Part, error) { return parts(), nil }
	r := Read(subsets)
	UseIndex(app, r)
	UseFleet(app, subsets)
	UseAgent(app, r, []string{"/login/oauth"}, "https://hanzo.id", "hanzo")
	app.Raw(zip.MethodAll, "/*", func(c *zip.Ctx) error { return c.String(http.StatusNotFound, "console") })
	return app
}

// hit sends one request to app as a caller addressing host would.
func hit(t *testing.T, app *zip.App, method, host, path, accept string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+host+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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

// Every row is in the document as a typed operation of the host's capability:
// open, named, summarised, in the media the edge answers in, and at an address
// no app row may claim.
func TestEveryDoorRowIsATypedOperationOfTheHost(t *testing.T) {
	d, err := Fleet(parts())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pages {
		op := d.Paths[p.path]["get"]
		if op == nil {
			t.Errorf("the document does not carry GET %s", p.path)
			continue
		}
		if !op.Tool || op.App != Self {
			t.Errorf("GET %s: x-tool %v, x-app %q — want a typed operation of %s", p.path, op.Tool, op.App, Self)
		}
		if op.Security == nil || len(*op.Security) != 0 {
			t.Errorf("GET %s requires a credential; a caller reads it before it holds one", p.path)
		}
		if op.OperationID != p.id || op.Summary != p.summary {
			t.Errorf("GET %s is %q %q", p.path, op.OperationID, op.Summary)
		}
		raw, _ := json.Marshal(op.Responses)
		var res map[string]struct {
			Content map[string]any `json:"content"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatal(err)
		}
		var media []string
		for m := range res["200"].Content {
			media = append(media, m)
		}
		sort.Strings(media)
		want := slices.Clone(p.media)
		sort.Strings(want)
		if !slices.Equal(media, want) {
			t.Errorf("GET %s answers %v in the document, %v on the edge", p.path, media, want)
		}
		if !Host(p.path) || !Routed(p.path) {
			t.Errorf("%s: Host %v, Routed %v — the door's rows are the host's, and a row claiming one would take it",
				p.path, Host(p.path), Routed(p.path))
		}
	}
}

// The crawlable set is what answers without a credential: the host's own
// documents, the well-known files and each index the root links to, never a
// template, a beta capability or a capability's own root.
func TestTheCrawlableSetIsWhatAnswersOpen(t *testing.T) {
	d, err := Fleet(parts())
	if err != nil {
		t.Fatal(err)
	}
	_, per := Discover(d)
	got := crawlable(d, per)
	if !slices.IsSorted(got) || len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Errorf("the set is not sorted and unique: %v", got)
	}
	for _, want := range []string{HomePath, spec.RootPath, Path, WellKnown, CommandPath, "/v1/kms", "/v1/search",
		LlmsPath, SkillPath, AuthPath, CatalogPath, mcp.Card, mcp.Metadata, mcp.MetadataMCP} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is missing from %v", want, got)
		}
	}
	for _, not := range []string{RobotsPath, SitemapPath, "/v1/labs", "/v1/agent", "/v1/admin", IndexPath, mcp.Path} {
		if slices.Contains(got, not) {
			t.Errorf("%s is in the crawlable set", not)
		}
	}
}

// Each row answers where its scope holds and yields everywhere else: the API
// host's documents on the API host alone, the agent's page on every host a brand
// claims, the resource metadata on every host.
func TestTheDoorAnswersEachRowWhereItsScopeHolds(t *testing.T) {
	app := doored(t)
	for _, p := range pages {
		for host, want := range map[string]bool{
			"api.hanzo.ai":     true,
			"API.Hanzo.AI:443": true,
			"api.lux.network":  true,
			"console.hanzo.ai": p.scope != apiHost,
			"hanzo.ai":         p.scope != apiHost,
			"acme.hanzo.app":   p.scope == everyHost,
			"karma.style":      p.scope == everyHost,
		} {
			resp, body := hit(t, app, http.MethodGet, host, p.path, "")
			if got := resp.StatusCode == http.StatusOK && body != "console"; got != want {
				t.Errorf("GET %s%s = %d %.40q, want the door: %v", host, p.path, resp.StatusCode, body, want)
				continue
			}
			if !want {
				continue
			}
			if got := resp.Header.Get("Content-Type"); got != p.media[0] {
				t.Errorf("GET %s%s Content-Type = %q, want %q", host, p.path, got, p.media[0])
			}
			if resp, body := hit(t, app, http.MethodHead, host, p.path, ""); resp.StatusCode != http.StatusOK || body != "" {
				t.Errorf("HEAD %s%s = %d with %d bytes", host, p.path, resp.StatusCode, len(body))
			}
		}
		if resp, body := hit(t, app, http.MethodPost, "api.hanzo.ai", p.path, ""); body != "console" {
			t.Errorf("POST %s was answered by the door (%d)", p.path, resp.StatusCode)
		}
	}
}

// contract is the Link header the home page carries, as the agent-readiness
// contract states it for Hanzo.
const contract = `</.well-known/api-catalog>; rel="api-catalog", </llms.txt>; rel="describedby"; type="text/plain", ` +
	`<https://api.hanzo.ai/v1/openapi.json>; rel="service-desc"; type="application/json", <https://docs.hanzo.ai>; rel="service-doc"`

// The home page answers in the medium the caller asked for, carries the links
// to everything else on every one of them, and its JSON is the capability index
// GET /v1 answers, byte for byte.
func TestTheHomePageAnswersInTheMediumAsked(t *testing.T) {
	app := doored(t)
	_, index := hit(t, app, http.MethodGet, "api.hanzo.ai", spec.RootPath, "")
	for _, tc := range []struct{ accept, media, starts string }{
		{"", mediaHTML, "<!doctype html>"},
		{"*/*", mediaHTML, "<!doctype html>"},
		{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", mediaHTML, "<!doctype html>"},
		{"text/markdown", mediaMarkdown, "# Hanzo API\n\nHanzo's API: one key for every capability listed at https://api.hanzo.ai/v1."},
		{"text/markdown, text/plain, */*", mediaMarkdown, "# Hanzo API\n"},
		{"text/markdown;q=0.5, text/html", mediaHTML, "<!doctype html>"},
		{"application/json", mediaJSON, index},
	} {
		resp, body := hit(t, app, http.MethodGet, "api.hanzo.ai", HomePath, tc.accept)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != tc.media || !strings.HasPrefix(body, tc.starts) {
			t.Errorf("Accept %q: %d %q %.80q, want %q beginning %.60q", tc.accept, resp.StatusCode,
				resp.Header.Get("Content-Type"), body, tc.media, tc.starts)
		}
		if got := resp.Header.Get("Link"); got != contract {
			t.Errorf("Accept %q: Link = %q, want %q", tc.accept, got, contract)
		}
		if resp.Header.Get("Vary") != "Accept" || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("Accept %q: Vary %q, Cache-Control %q", tc.accept, resp.Header.Get("Vary"), resp.Header.Get("Cache-Control"))
		}
	}
	_, md := hit(t, app, http.MethodGet, "api.hanzo.ai", HomePath, "text/markdown")
	_, page := hit(t, app, http.MethodGet, "api.hanzo.ai", HomePath, "text/html")
	for _, l := range (&door{b: brand.For("hanzo")}).links() {
		if !strings.Contains(md, "("+l.href+")") || !strings.Contains(page, `href="`+l.href+`"`) {
			t.Errorf("%s (%s) is not on both the Markdown and the HTML page", l.label, l.href)
		}
	}
}

// robots.txt names every crawler in the group that carries the content signals,
// allows exactly the sitemap's addresses under /v1/ and keeps a crawler out of
// the rest of /v1/ and the apps' addresses outside it, and ends at the sitemap.
func TestRobotsAllowsExactlyWhatTheSitemapNames(t *testing.T) {
	app := doored(t)
	_, robots := hit(t, app, http.MethodGet, "api.hanzo.ai", RobotsPath, "")
	_, sitemap := hit(t, app, http.MethodGet, "api.hanzo.ai", SitemapPath, "")

	var set struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(sitemap), &set); err != nil || len(set.URLs) == 0 {
		t.Fatalf("the sitemap is not a urlset (%v): %.200s", err, sitemap)
	}
	var under []string
	for _, u := range set.URLs {
		p, ok := strings.CutPrefix(u.Loc, "https://api.hanzo.ai")
		if !ok || !strings.HasPrefix(p, "/") {
			t.Errorf("the sitemap names %q, which is not an absolute address on the API host", u.Loc)
		}
		if strings.HasPrefix(p, spec.RootPath+"/") {
			under = append(under, "Allow: "+p+"$")
		}
	}

	lines := strings.Split(strings.TrimSpace(robots), "\n")
	var allowed []string
	for _, l := range lines {
		if strings.HasPrefix(l, "Allow: "+spec.RootPath+"/") {
			allowed = append(allowed, l)
		}
	}
	if !slices.Equal(allowed, under) {
		t.Errorf("robots.txt allows %v under /v1/; the sitemap names %v", allowed, under)
	}
	want := []string{"User-agent: *", Signal, "Allow: /", "Disallow: /v1/", "Disallow: /login/oauth"}
	for _, c := range Crawlers {
		want = append(want, "User-agent: "+c)
	}
	for _, w := range want {
		if !slices.Contains(lines, w) {
			t.Errorf("robots.txt has no line %q:\n%s", w, robots)
		}
	}
	if last := lines[len(lines)-1]; last != "Sitemap: https://api.hanzo.ai/sitemap.xml" {
		t.Errorf("robots.txt ends %q", last)
	}
	// One group: no blank line until the sitemap, so every crawler named reads the
	// same rules and the same signals.
	if strings.Count(strings.TrimSpace(robots), "\n\n") != 1 {
		t.Errorf("robots.txt is not one group and a sitemap:\n%s", robots)
	}
}

// The card is the server's own: the name and revisions initialize answers with,
// the version the build carries, SEP-2127's shape, and its own medium when a
// client asks for it.
func TestTheCardIsTheServersOwn(t *testing.T) {
	app := doored(t)
	resp, body := hit(t, app, http.MethodGet, "api.hanzo.ai", mcp.Card, "")
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("the card is not readable from any origin")
	}
	var c Card
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	if c.Schema != cardSchema || !regexp.MustCompile(`^[a-zA-Z0-9.-]+/[a-zA-Z0-9._-]+$`).MatchString(c.Name) || c.Name != "ai.hanzo/cloud" {
		t.Errorf("$schema %q, name %q", c.Schema, c.Name)
	}
	if n := len(c.Description); n == 0 || n > 100 {
		t.Errorf("description is %d characters; SEP-2127 allows 1 to 100", n)
	}
	url := "https://api.hanzo.ai" + mcp.Path
	if len(c.Remotes) != 1 || c.Remotes[0].Type != "streamable-http" || c.Remotes[0].URL != url ||
		!slices.Equal(c.Remotes[0].Revisions, mcp.Revisions) {
		t.Errorf("remotes = %+v", c.Remotes)
	}
	if c.ServerInfo != (implementation{mcp.Name, env.Version()}) || c.Version != env.Version() ||
		c.URL != url || c.Protocol != mcp.Protocol || c.Transport["type"] != "streamable-http" || !c.Capabilities["tools"] {
		t.Errorf("card = %+v", c)
	}
	if resp, again := hit(t, app, http.MethodGet, "api.hanzo.ai", mcp.Card, mediaCard); resp.Header.Get("Content-Type") != mediaCard || again != body {
		t.Errorf("Accept %s: %q, the same bytes %v", mediaCard, resp.Header.Get("Content-Type"), again == body)
	}
}

// The A2A card names the A2A server on the API host wherever it is read, in the
// fields A2A 1.0 requires, with the authorization server the host's resource
// metadata names; the pre-0.3 address answers the same bytes (hanzo-inc/cloud#422).
func TestTheAgentCardNamesTheA2AServer(t *testing.T) {
	app := doored(t)
	resp, body := hit(t, app, http.MethodGet, "api.hanzo.ai", a2a.Card, "")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%d, CORS %q, %q", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"), resp.Header.Get("Content-Type"))
	}
	var required map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &required); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"name", "description", "supportedInterfaces", "version", "capabilities",
		"defaultInputModes", "defaultOutputModes", "skills"} {
		if len(required[f]) == 0 {
			t.Errorf("the card has no %s, which A2A 1.0 requires", f)
		}
	}
	var c AgentCard
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatal(err)
	}
	want := agentInterface{URL: "https://api.hanzo.ai" + a2a.Path, Binding: "JSONRPC", Protocol: "1.0"}
	if len(c.Interfaces) != 1 || c.Interfaces[0] != want {
		t.Errorf("supportedInterfaces = %+v, want [%+v]", c.Interfaces, want)
	}
	if c.Name != "Hanzo" || c.Version != env.Version() || c.Provider.URL != "https://hanzo.ai" {
		t.Errorf("name %q, version %q, provider %+v", c.Name, c.Version, c.Provider)
	}
	if c.Capabilities.Streaming || c.Capabilities.PushNotifications || c.Capabilities.ExtendedAgentCard {
		t.Errorf("the card claims a capability the server does not have: %+v", c.Capabilities)
	}
	oidc, _ := c.SecuritySchemes["oauth"]["openIdConnectSecurityScheme"].(map[string]any)
	if oidc["openIdConnectUrl"] != "https://hanzo.id/.well-known/openid-configuration" {
		t.Errorf("the card signs callers in at %v", oidc["openIdConnectUrl"])
	}
	for _, r := range c.SecurityRequirements {
		for name := range r.Schemes {
			if _, ok := c.SecuritySchemes[name]; !ok {
				t.Errorf("a requirement names %q, which no scheme declares", name)
			}
		}
	}
	for _, s := range c.Skills {
		if s.ID == "" || s.Name == "" || s.Description == "" || len(s.Tags) == 0 {
			t.Errorf("skill %+v lacks a field A2A requires", s)
		}
	}
	// hanzoskills.com is a static site the edge routes these two addresses from to
	// here (hanzo-inc/cloud#422): it answers the API's card like every claimed host.
	for _, at := range []struct{ host, path string }{{"api.hanzo.ai", a2a.Legacy}, {"hanzo.ai", a2a.Card}, {"hanzo.ai", a2a.Legacy},
		{"hanzoskills.com", a2a.Card}, {"hanzoskills.com", a2a.Legacy}} {
		if r, again := hit(t, app, http.MethodGet, at.host, at.path, ""); r.StatusCode != http.StatusOK || again != body {
			t.Errorf("%s%s: %d, the same card %v", at.host, at.path, r.StatusCode, again == body)
		}
	}
}

// The catalog is an RFC 9727 linkset, and every address it names on the API host
// answers there.
func TestTheCatalogNamesWhatThisHostServes(t *testing.T) {
	app := doored(t)
	resp, body := hit(t, app, http.MethodGet, "api.hanzo.ai", CatalogPath, "")
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/linkset+json") {
		t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
	var set struct {
		Linkset []map[string]json.RawMessage `json:"linkset"`
	}
	if err := json.Unmarshal([]byte(body), &set); err != nil {
		t.Fatal(err)
	}
	var anchors []string
	for _, e := range set.Linkset {
		var a string
		_ = json.Unmarshal(e["anchor"], &a)
		anchors = append(anchors, a)
		for rel, raw := range e {
			if rel == "anchor" {
				continue
			}
			var ts []target
			if err := json.Unmarshal(raw, &ts); err != nil {
				t.Fatalf("%s %s: %v", a, rel, err)
			}
			for _, x := range ts {
				if p, ok := strings.CutPrefix(x.Href, "https://api.hanzo.ai"); ok {
					if r, body := hit(t, app, http.MethodGet, "api.hanzo.ai", p, ""); r.StatusCode != http.StatusOK || body == "console" {
						t.Errorf("%s %s names %s, which this host does not answer (%d)", a, rel, x.Href, r.StatusCode)
					}
				}
			}
		}
	}
	if want := []string{"https://api.hanzo.ai/v1", "https://api.hanzo.ai/v1/mcp", "https://hanzo.ai/agents"}; !slices.Equal(anchors, want) {
		t.Errorf("anchors = %v, want %v", anchors, want)
	}
}

// A brand's host names that brand and none other: Lux's API host says Lux and
// links nothing of Hanzo's, whatever it carries.
func TestEachBrandIsShownItsOwn(t *testing.T) {
	app := doored(t)
	url := regexp.MustCompile(`https?://[^\s)<>"\\]+`)
	for _, p := range pages {
		for _, accept := range []string{"", "text/markdown"} {
			_, body := hit(t, app, http.MethodGet, "api.lux.network", p.path, accept)
			for _, u := range url.FindAllString(body, -1) {
				host := strings.Split(strings.SplitN(u, "://", 2)[1], "/")[0]
				if id, ok := brand.ForHostOK(host); ok && id != "lux" {
					t.Errorf("GET api.lux.network%s names %s, a %s address", p.path, u, id)
				}
			}
		}
	}
}

// The MCP card answers on every host a brand claims, at both addresses scanners
// probe, and describes the brand's API either way: hanzo.ai, cloud.hanzo.ai and
// hanzo.bot are routed here for it (hanzo-inc/cloud#421). The alias is the card's
// own bytes, and no second operation in the document.
func TestTheCardAnswersOnABrandsHostsAtBothAddresses(t *testing.T) {
	app := doored(t)
	_, want := hit(t, app, http.MethodGet, "api.hanzo.ai", mcp.Card, "")
	for _, host := range []string{"api.hanzo.ai", "hanzo.ai", "cloud.hanzo.ai", "hanzo.bot"} {
		for _, path := range []string{mcp.Card, "/.well-known/mcp.json"} {
			resp, body := hit(t, app, http.MethodGet, host, path, "")
			if resp.StatusCode != http.StatusOK || body != want {
				t.Errorf("%s%s = %d %q, want the API's card", host, path, resp.StatusCode, body)
			}
		}
	}
	// A host no brand claims is not answered as one.
	if resp, body := hit(t, app, http.MethodGet, "example.com", "/.well-known/mcp.json", ""); body != "console" {
		t.Errorf("example.com/.well-known/mcp.json = %d %q, want it to fall through", resp.StatusCode, body)
	}
	d, err := Fleet(parts())
	if err != nil {
		t.Fatal(err)
	}
	if _, in := d.Paths["/.well-known/mcp.json"]; in {
		t.Errorf("the alias is in the document as an operation")
	}
}

// trusting is doored behind a host that trusts every proxy's forwarded headers,
// the way cmd/cloud trusts its ingress: a forwarded header a caller wrote reaches
// these handlers exactly as it would in production.
func trusting(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true, TrustProxy: true,
		TrustedProxies: []string{"0.0.0.0/0", "::/0"}, ProxyHeader: "X-Forwarded-For"})
	subsets := func() ([]Part, error) { return parts(), nil }
	r := Read(subsets)
	UseIndex(app, r)
	UseFleet(app, subsets)
	UseAgent(app, r, []string{"/login/oauth"}, "https://hanzo.id", "hanzo")
	app.Raw(zip.MethodAll, "/*", func(c *zip.Ctx) error { return c.String(http.StatusNotFound, "console") })
	return app
}

// probe is the name a caller writes into every forwarded header it can.
const probe = "red-probe.example"

// No document, and no header on it, names a host a forwarded header wrote: every
// row and alias, in every medium, on a brand's API host and on its site, with
// X-Forwarded-Host, X-Forwarded-Proto and Forwarded all set by the caller. The
// brand, the origin and the resource are the request's own Host's.
func TestNoForwardedHeaderNamesAHost(t *testing.T) {
	app := trusting(t)
	paths := []string{spec.RootPath}
	for _, p := range pages {
		paths = append(paths, p.path)
	}
	for from := range aliases {
		paths = append(paths, from)
	}
	for _, host := range []string{"api.lux.network", "api.hanzo.ai", "lux.network"} {
		for _, path := range paths {
			for _, accept := range []string{"", mediaMarkdown, mediaJSON, mediaCard, mediaHTML} {
				req, err := http.NewRequest(http.MethodGet, "http://"+host+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if accept != "" {
					req.Header.Set("Accept", accept)
				}
				req.Header.Set("X-Forwarded-Host", probe)
				req.Header.Set("X-Forwarded-Proto", "http")
				req.Header.Set("Forwarded", "host="+probe+";proto=http")
				resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if strings.Contains(string(b), "red-probe") {
					t.Errorf("GET %s%s (Accept %q) names the forwarded host in its body", host, path, accept)
				}
				for k, vs := range resp.Header {
					for _, v := range vs {
						if strings.Contains(v, "red-probe") {
							t.Errorf("GET %s%s (Accept %q) names the forwarded host in %s: %s", host, path, accept, k, v)
						}
					}
				}
			}
		}
	}
}

// The resource metadata describes the host the request was sent to, and names
// the identity host of that host's brand: a Lux caller signs in at lux.id, a Zoo
// caller at zoolabs.id, never at Hanzo's; the deployment's own brand, and a host
// no brand claims, get the deployment's issuer. The resource is the URL the
// client asked about (RFC 9728 §3.3): the API's origin at the root address, the
// MCP server's own URL at the path-suffixed one.
func TestTheResourceMetadataIsTheHostsBrands(t *testing.T) {
	app := trusting(t)
	for _, tc := range []struct{ host, resource, issuer, name, docs string }{
		{"api.hanzo.ai", "https://api.hanzo.ai", "https://hanzo.id", "Hanzo API", brand.For("hanzo").Docs},
		{"api.lux.network", "https://api.lux.network", "https://lux.id", "Lux API", brand.For("lux").Docs},
		{"api.zoo.ngo", "https://api.zoo.ngo", "https://zoolabs.id", "Zoo API", brand.For("zoo").Docs},
		{"cloud", "http://cloud", "https://hanzo.id", "Hanzo API", brand.For("hanzo").Docs},
	} {
		for path, resource := range map[string]string{mcp.Metadata: tc.resource, mcp.MetadataMCP: tc.resource + mcp.Path} {
			req, err := http.NewRequest(http.MethodGet, "http://"+tc.host+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Forwarded-Host", probe)
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			var meta Resource
			err = json.NewDecoder(resp.Body).Decode(&meta)
			_ = resp.Body.Close()
			if err != nil || meta.Resource != resource || len(meta.Servers) != 1 || meta.Servers[0] != tc.issuer ||
				meta.Name != tc.name || meta.Documentation != tc.docs {
				t.Errorf("GET %s%s = %+v %v, want resource %s, issuer %s, %s at %s",
					tc.host, path, meta, err, resource, tc.issuer, tc.name, tc.docs)
			}
		}
	}
}
