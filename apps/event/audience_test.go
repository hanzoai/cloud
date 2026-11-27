package event

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

const chrome = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"

// awsSample is the shape of ip-ranges.json, cut to two prefixes a family.
const awsSample = `{"syncToken":"1","prefixes":[
 {"ip_prefix":"3.5.140.0/22","region":"ap-northeast-2","service":"AMAZON"},
 {"ip_prefix":"52.94.76.0/22","region":"us-west-2","service":"AMAZON"}],
 "ipv6_prefixes":[{"ipv6_prefix":"2600:1f14::/35","region":"us-west-2","service":"AMAZON"}]}`

func TestRobot(t *testing.T) {
	for _, ua := range []string{
		"",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/129.0.0.0 Safari/537.36",
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
		"facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)",
		"Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)",
		"Mozilla/5.0 (compatible; Baiduspider/2.0)",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2)",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome-Lighthouse",
		"Playwright/1.48.0", "Puppeteer", "selenium/4.25.0 (java linux)", "PhantomJS/2.1.1",
		"curl/8.5.0", "Wget/1.21.4", "python-requests/2.32.3", "Python-urllib/3.12",
		"Go-http-client/1.1", "Go-http-client/2.0", "node", "undici", "axios/1.7.7",
		"Java/21.0.4", "Apache-HttpClient/4.5.14 (Java/17)", "PostmanRuntime/7.42.0",
		"UptimeRobot/2.0", "kube-probe/1.31", "ELB-HealthChecker/2.0",
		"Mozilla/5.0 (compatible; Google-InspectionTool/1.0)",
	} {
		if !robot(ua) {
			t.Errorf("robot(%q) = false, want a program", ua)
		}
	}
	for _, ua := range []string{
		chrome,
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 Edg/129.0.0.0",
		// Cubot makes phones: its model names are the one browser agent that says "bot".
		"Mozilla/5.0 (Linux; Android 11; CUBOT X50) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36",
	} {
		if robot(ua) {
			t.Errorf("robot(%q) = true, want a person's browser", ua)
		}
	}
}

func TestPublishedRanges(t *testing.T) {
	aws, err := parseAWS([]byte(awsSample))
	if err != nil || len(aws) != 3 {
		t.Fatalf("parseAWS = %v, %v", aws, err)
	}
	google, _ := parseGoogle([]byte(`{"prefixes":[{"ipv4Prefix":"34.1.208.0/20"},{"ipv6Prefix":"2600:1900:4000::/44"}]}`))
	oracle, _ := parseOracle([]byte(`{"regions":[{"region":"x","cidrs":[{"cidr":"40.233.0.0/19","tags":["OCI"]}]}]}`))
	feed, _ := parseGeofeed([]byte("# a comment\n5.101.96.0/21,NL,NL-NH,Amsterdam,1098 XH\n2600:3c00::/32,US,US-TX,Richardson,\n"))
	azure, _ := parseAzure([]byte(`{"values":[{"name":"AzureCloud","properties":{"addressPrefixes":["4.144.0.0/12"]}},{"name":"Other","properties":{"addressPrefixes":["9.9.9.0/24"]}}]}`))
	var all []netip.Prefix
	for _, ps := range [][]netip.Prefix{aws, google, oracle, feed, azure} {
		all = append(all, ps...)
	}
	set := merge(all)
	for _, in := range []string{"3.5.140.9", "3.5.143.255", "52.94.77.1", "2600:1f14::1", "34.1.210.3", "40.233.10.1", "5.101.97.2", "2600:3c00:1::5", "4.150.0.1", "::ffff:3.5.140.9"} {
		if !set.has(netip.MustParseAddr(in)) {
			t.Errorf("%s is inside a published range, set says no", in)
		}
	}
	for _, out := range []string{"3.5.144.0", "203.0.113.7", "9.9.9.9", "2600:1f14:2000::1", "1.1.1.1"} {
		if set.has(netip.MustParseAddr(out)) {
			t.Errorf("%s is in no published range, set says yes", out)
		}
	}
	var none *ranges
	if none.has(netip.MustParseAddr("3.5.140.9")) {
		t.Error("a set never loaded holds nothing")
	}
}

// audienceOf asks Audience about one request through a real route, the headers
// standing in for what the identity boundary mints (TestAudienceThroughTheBoundary,
// in the root package, drives the boundary itself).
func audienceOf(t *testing.T, org string, networks []string, h map[string]string) string {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	var got string
	app.Raw(http.MethodGet, "/who", func(c *zip.Ctx) error {
		got = Audience(c, org, networks)
		return c.NoContent(http.StatusNoContent)
	})
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/who", nil)
	for k, v := range h {
		r.Header.Set(k, v)
	}
	if _, err := app.Fiber().Test(r, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAudience(t *testing.T) {
	aws, _ := parseAWS([]byte(awsSample))
	prev := clouds.Load()
	clouds.Store(merge(aws))
	t.Cleanup(func() { clouds.Store(prev) })

	home := "198.51.100.23" // an address no provider publishes
	member := map[string]string{"User-Agent": chrome, "X-Forwarded-For": home, "X-User-Id": "u-1", "X-User-Orgs": "acme,globex", "X-Org-Id": "acme"}
	for _, tc := range []struct {
		name, org, want string
		networks        []string
		h               map[string]string
	}{
		{"claude code", "acme", Agent, nil, map[string]string{"User-Agent": "claude-cli/2.1.310 (external, cli)", "X-Forwarded-For": home}},
		{"an agent from a cloud address is still an agent", "acme", Agent, nil, map[string]string{"User-Agent": "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; ChatGPT-User/1.0; +https://openai.com/bot", "X-Forwarded-For": "3.5.140.77"}},
		{"a training crawler is a bot", "acme", Bot, nil, map[string]string{"User-Agent": "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2)", "X-Forwarded-For": home}},
		{"a search crawler is a bot", "acme", Bot, nil, map[string]string{"User-Agent": "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", "X-Forwarded-For": home}},
		{"curl", "acme", Bot, nil, map[string]string{"User-Agent": "curl/8.5.0", "X-Forwarded-For": home}},
		{"headless chrome", "acme", Bot, nil, map[string]string{"User-Agent": "Mozilla/5.0 HeadlessChrome/129.0.0.0 Safari/537.36", "X-Forwarded-For": home}},
		{"a browser in AWS", "acme", Bot, nil, map[string]string{"User-Agent": chrome, "X-Forwarded-For": "3.5.140.77"}},
		// Behind Cloudflare the ingress appends the Cloudflare edge (104.22.100.153)
		// and Cloudflare names the visitor in CF-Connecting-IP. The home address is a
		// prefix the visitor forged; the edge is in no provider's range. Either one
		// read as the caller makes this program a person.
		{"a browser in AWS behind Cloudflare", "acme", Bot, nil, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home + ", 3.5.140.77, 104.22.100.153", "CF-Connecting-IP": "3.5.140.77"}},
		{"a home browser behind Cloudflare", "acme", Person, nil, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home + ", 104.22.100.153", "CF-Connecting-IP": home}},
		{"the internal network behind Cloudflare", "acme", Internal, []string{"198.51.100.0/24"}, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home + ", 104.22.100.153", "CF-Connecting-IP": home}},
		{"a program is a program even when a member runs it", "acme", Bot, nil, map[string]string{"User-Agent": "Playwright/1.48", "X-Forwarded-For": home, "X-User-Id": "u-1", "X-User-Orgs": "acme"}},
		{"member of the site's org", "acme", Internal, nil, member},
		{"member of another org", "initech", Person, nil, member},
		{"no bearer", "acme", Person, nil, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home}},
		{"an org the boundary did not validate is nobody's", "acme", Person, nil, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home, "X-User-Orgs": "acme"}},
		{"no site", "", Person, nil, member},
		{"internal network", "acme", Internal, []string{"198.51.100.0/24"}, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home}},
		{"outside the internal network", "acme", Person, []string{"192.0.2.0/24"}, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home}},
		// A brand's own org holds every self-serve account as a plain member.
		{"a customer in the signup org", "hanzo", Person, nil, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home, "X-User-Id": "u-2", "X-User-Orgs": "hanzo", "X-Org-Id": "hanzo"}},
		{"an admin of the signup org", "hanzo", Internal, nil, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home, "X-User-Id": "u-3", "X-User-Orgs": "hanzo", "X-Org-Id": "hanzo", "X-User-IsOrgAdmin": "true"}},
		{"an admin of another org is not the brand's", "hanzo", Person, nil, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home, "X-User-Id": "u-4", "X-User-Orgs": "acme,hanzo", "X-Org-Id": "acme", "X-User-IsOrgAdmin": "true"}},
		{"a SuperAdmin on the brand's site", "hanzo", Internal, nil, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home, "X-User-Id": "u-5", "X-User-Orgs": "admin", "X-Org-Id": "admin", "X-User-IsAdmin": "true"}},
	} {
		if got := audienceOf(t, tc.org, tc.networks, tc.h); got != tc.want {
			t.Errorf("%s: audience = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Ingest stamps the audience on every event a visit carries, so a client that
// predates the classifier is covered and none can clear it; a backend's own
// statement is not a visit and is left alone.
func TestStampTheAudience(t *testing.T) {
	evs := func() []CaptureEvent {
		return []CaptureEvent{
			{Event: "a", URL: "https://acme.example/", Properties: map[string]any{"bot": false, "internal": false, "consent": "analytics"}},
			{Event: "b", URL: "https://acme.example/", vouched: true},
		}
	}
	got, _ := stamp(evs(), visit{ua: chrome, from: caller{bot: true}})
	if got[0].Properties["bot"] != true {
		t.Errorf("a program's visit is stamped bot, over the client's false: %v", got[0].Properties)
	}
	if _, ok := got[1].Properties["bot"]; ok {
		t.Errorf("a backend's statement is not classified: %v", got[1].Properties)
	}
	got, _ = stamp(evs(), visit{ua: chrome, from: caller{member: true}})
	if got[0].Properties["internal"] != true {
		t.Errorf("the team's visit is stamped internal: %v", got[0].Properties)
	}
	ip := netip.MustParseAddr("198.51.100.23")
	in := evs()
	in[0].networks = []string{"198.51.100.0/24"}
	if got, _ = stamp(in, visit{ua: chrome, from: caller{ip: ip}}); got[0].Properties["internal"] != true {
		t.Errorf("a visit from the project's network is stamped internal: %v", got[0].Properties)
	}
	got, _ = stamp(evs(), visit{ua: chrome, from: caller{ip: ip}})
	if got[0].Properties["internal"] != false || got[0].Properties["bot"] != false {
		t.Errorf("a person's visit keeps what the page said: %v", got[0].Properties)
	}
	if !FromPerson(got[0].Properties) || FromPerson(map[string]any{"bot": true}) || FromPerson(map[string]any{"internal": "1"}) {
		t.Error("FromPerson misreads the marks")
	}
}

func TestBridgeCarriesTheBot(t *testing.T) {
	if renamed["bot"] != "$bot" || renamed["internal"] != "$internal" {
		t.Errorf("insights reads $bot beside $internal: %v %v", renamed["bot"], renamed["internal"])
	}
	if !testTraffic(map[string]any{"bot": true}) {
		t.Error("the forwarder must skip a bot's event")
	}
}

func TestAgentFamily(t *testing.T) {
	for ua, want := range map[string]string{
		"claude-cli/2.1.310 (external, cli)": "claude",
		"Claude-User/1.0":                    "claude",
		"Cursor/0.50.1":                      "cursor",
		"codex_cli_rs/0.20.0":                "codex",
		"Mozilla/5.0; compatible; ChatGPT-User/1.0; +https://openai.com/bot": "chatgpt",
		"Grok/1.0":                 "grok",
		"hermes-agent/0.4":         "hermes",
		"gemini-cli/0.1.0":         "gemini",
		"Perplexity-User/1.0":      "perplexity",
		"GitHubCopilotChat/0.22.0": "copilot",
		"OpenClaw/1.2":             "openclaw",
		"hanzo/8.5.753":            "hanzo",
		"hanzo-dev/0.7.3":          "hanzo",
		// Crawlers, libraries, our own probes and browsers name no agent.
		"ClaudeBot/1.0": "",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2)": "",
		"PerplexityBot/1.0": "",
		"Googlebot/2.1":     "",
		"curl/8.5.0":        "",
		"python-httpx/0.27": "",
		"hanzo-support/1":   "",
		"Gatus/1.0":         "",
		chrome:              "",
		"":                  "",
	} {
		if got := AgentFamily(ua); got != want {
			t.Errorf("AgentFamily(%q) = %q, want %q", ua, got, want)
		}
	}
	if FromPerson(map[string]any{Agent: true}) {
		t.Error("an agent's visit must not count as a person's")
	}
}
