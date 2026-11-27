// Package authztest is the authorization contract an app's own test suite runs
// against its own routes and tools.
//
// It mounts one plugin alone, puts the real identity boundary in front of it
// (cloud.IdentityMiddleware, trusting a JWKS this package serves), and asks every
// route the app registers as a fixed set of callers with signed tokens. Cases are
// derived from the routes the app actually serves, so a route registered tomorrow
// is covered by the next run with nothing to add.
//
// What it holds, at every route:
//
//   - Forged headers buy nothing. No credential plus the SuperAdmin headers is
//     answered exactly as no credential. A member (or org admin) who adds the
//     SuperAdmin headers is answered exactly as that member: still signed in,
//     never elevated.
//   - Another org's name buys nothing. A member selecting an org it does not
//     belong to is answered as the member acting in its own org.
//   - The route's floor holds. Every route refuses an anonymous caller unless it
//     declared itself open (openapi.Open). /v1/admin is SuperAdmin's: member and
//     org admin are refused, the SuperAdmin is let in. An app states any other
//     floor by prefix (Contract.Floors).
//
// And at every read-only, argument-free MCP tool: a signed-in member's call
// reaches the op with its principal, so the op never answers that it cannot see
// its caller where its own route would have served the same member.
//
// Statuses are classified as the gate's decision only: 401, 403, or in.
// Whatever the handler went on to say is not the gate's.
package authztest

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/authz"
	zapmcp "github.com/zap-proto/mcp"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/spec"
)

// Floor is the least a caller must be for a route to let it in.
type Floor int

const (
	// Open routes need no credential. Declared through openapi.Open, or by Floors.
	Open Floor = iota
	// Signed routes need a validated principal. The default.
	Signed
	// OrgAdmin routes need an admin of the org the request acts in.
	OrgAdmin
	// Super routes need the SuperAdmin (owner == "admin").
	Super
)

func (f Floor) String() string {
	return [...]string{"open", "signed", "orgadmin", "super"}[f]
}

// Contract is one app's authorization policy.
type Contract struct {
	// Plugin is the app, as its plugin/<app>/main.go mounts it.
	Plugin cloud.Plugin
	// Floors raises (or opens) routes by path prefix, longest prefix wins.
	// A prefix is a path, matched on segment boundaries.
	Floors map[string]Floor
	// Relayed names prefixes whose answers come from something behind the app
	// (a cluster, an upstream), so a refusal there is not this process's.
	// Each carries its reason.
	Relayed map[string]string
	// Data prepares the app's data directory (a store it needs to serve) and
	// returns it. Nil is an empty temporary directory.
	Data func(t *testing.T) string
}

// The callers. Each reaches the app through the real boundary.
const (
	anon           = "anon"
	forged         = "forged"
	member         = "member"
	memberForged   = "member+forged"
	orgadmin       = "orgadmin"
	orgadminForged = "orgadmin+forged"
	cross          = "cross-org"
	super          = "super"
)

var order = []string{anon, forged, member, memberForged, orgadmin, orgadminForged, cross, super}

// forgedHeaders is the SuperAdmin identity as headers: what a caller would send
// to be taken for one if anything trusted a header over its token.
var forgedHeaders = map[string]string{
	"X-User-Id":         "admin/z",
	"X-Org-Id":          authz.AdminOrg,
	"X-User-Owner":      authz.AdminOrg,
	"X-User-IsAdmin":    "true",
	"X-User-IsOrgAdmin": "true",
}

const issuer = "https://authztest.iam"

// fixture is the boundary and the callers' headers, built once per process.
type fixture struct {
	jwks    string
	headers map[string]map[string]string
	plane   map[string]map[string]string // callers as the boundary states them, for the plane
}

var fx = func() *fixture {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	jwks, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "authztest", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
	}}})
	if err != nil {
		panic(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jwks) }))
	sign := func(sub string, orgs ...authz.Membership) string {
		c := authz.Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    issuer,
				Subject:   sub,
				Audience:  jwt.ClaimStrings{"hanzo-cloud"},
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
			},
			Owner:             authz.AdminOrg, // the application's org, which grants nothing
			PreferredUsername: sub,
			Email:             sub + "@example.test",
			TokenType:         "access-token",
			Orgs:              orgs,
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		tok.Header["kid"] = "authztest"
		raw, err := tok.SignedString(key)
		if err != nil {
			panic(err)
		}
		return "Bearer " + raw
	}
	mem := sign("dave", authz.Membership{Org: "acme", Role: authz.Member})
	adm := sign("erin", authz.Membership{Org: "acme", Role: authz.Admin})
	sup := sign("z", authz.Membership{Org: authz.AdminOrg, Role: authz.Admin})
	with := func(tok string, extra map[string]string) map[string]string {
		h := map[string]string{}
		for k, v := range extra {
			h[k] = v
		}
		if tok != "" {
			h["Authorization"] = tok
		}
		return h
	}
	return &fixture{
		jwks: srv.URL,
		headers: map[string]map[string]string{
			anon:           {},
			forged:         with("", forgedHeaders),
			member:         with(mem, map[string]string{"X-Org-Id": "acme"}),
			memberForged:   with(mem, forgedHeaders),
			orgadmin:       with(adm, map[string]string{"X-Org-Id": "acme"}),
			orgadminForged: with(adm, forgedHeaders),
			cross:          with(mem, map[string]string{"X-Org-Id": "globex"}),
			super:          with(sup, nil),
		},
		plane: map[string]map[string]string{
			member: {"X-User-Id": "dave", "X-Org-Id": "acme", "X-User-Owner": "acme"},
			super:  {"X-User-Id": "z", "X-Org-Id": authz.AdminOrg, "X-User-Owner": authz.AdminOrg, "X-User-IsAdmin": "true"},
		},
	}
}()

// Run holds the contract over every route and tool the app serves.
func Run(t *testing.T, c Contract) {
	t.Helper()
	dir := t.TempDir()
	if c.Data != nil {
		dir = c.Data(t)
	}
	t.Setenv(env.DataDirKey, dir)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	app := mount(t, c.Plugin, dir)
	e := app
	examples := required(app)
	lines := routes(app)
	if len(lines) == 0 {
		t.Fatalf("%s serves no route; the contract would assert nothing", c.Plugin.Name)
	}
	serving := false
	for _, line := range lines {
		method, pattern, _ := strings.Cut(line, " ")
		if st := answer(e, method, concrete(pattern), examples[line], fx.headers[super]); !strings.HasPrefix(st, "5") {
			serving = true
			break
		}
	}
	if !serving {
		t.Fatalf("%s answered 5xx at every route to every caller: it did not mount a working plane, so the contract would assert nothing", c.Plugin.Name)
	}
	t.Run("routes", func(t *testing.T) {
		for _, line := range lines {
			method, pattern, _ := strings.Cut(line, " ")
			if reason := c.relayed(pattern); reason != "" {
				continue
			}
			t.Run(line, func(t *testing.T) {
				t.Parallel()
				got := map[string]string{}
				for _, who := range order {
					got[who] = answer(e, method, concrete(pattern), examples[line], fx.headers[who])
				}
				check(t, c.floor(method, pattern), got)
			})
		}
	})
	var called int
	t.Run("tools", func(t *testing.T) { called = tools(t, app, e) })
	t.Logf("%s: %d routes held to the contract, %d read-only tools called as a signed-in member", c.Plugin.Name, len(lines), called)
}

// errorer is the part of *testing.T check reports to, so the contract itself is
// tested on tables.
type errorer interface {
	Helper()
	Errorf(format string, args ...any)
}

// check holds one route's answers to the contract.
//
// "Got in" is succeeded: a 2xx or 3xx. A 400 is the validator answering before
// the gate, and a 409 is a refusal of its own (an org the token does not list),
// so neither is a way in, and neither is evidence the gate held either.
func check(t errorer, floor Floor, got map[string]string) {
	t.Helper()
	in := func(who string) bool { return succeeded(got[who]) }
	escalated := func(variant, base, what string) {
		if !in(base) && in(variant) {
			t.Errorf("%s: %s answered %s where %s answered %s (%v)", what, variant, got[variant], base, got[base], got)
		}
	}
	escalated(forged, anon, "forged SuperAdmin headers let an anonymous caller in")
	escalated(memberForged, member, "forged SuperAdmin headers elevated a member")
	escalated(orgadminForged, orgadmin, "forged SuperAdmin headers elevated an org admin")
	escalated(cross, member, "naming another org let a member in")
	if floor >= Signed && in(anon) {
		t.Errorf("floor %s: an anonymous caller got in (%v)", floor, got)
	}
	if floor >= OrgAdmin && in(member) {
		t.Errorf("floor %s: a plain member got in (%v)", floor, got)
	}
	if floor >= Super && in(orgadmin) {
		t.Errorf("floor %s: an org admin got in (%v)", floor, got)
	}
	if floor >= Super && (got[super] == "401" || got[super] == "403") {
		t.Errorf("floor %s: the SuperAdmin was refused (%v)", floor, got)
	}
}

func (c Contract) floor(method, pattern string) Floor {
	best, f := -1, Signed
	for p, fl := range c.Floors {
		if under(pattern, p) && len(p) > best {
			best, f = len(p), fl
		}
	}
	if best >= 0 {
		return f
	}
	if spec.IsOpen(pattern, method) {
		return Open
	}
	if under(pattern, "/v1/admin") {
		return Super
	}
	return Signed
}

func (c Contract) relayed(pattern string) string {
	for p, why := range c.Relayed {
		if under(pattern, p) {
			return why
		}
	}
	return ""
}

func under(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/")
}

// mount composes one plugin alone, as its main does.
func mount(t *testing.T, spec cloud.Plugin, dir string) *zip.App {
	t.Helper()
	cfg, done, err := host.SpecConfig()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	t.Cleanup(done)
	cfg.Enable = []string{spec.Name}
	cfg.IAMIssuer, cfg.DataDir = issuer, dir
	cfg.Discovery, cfg.DiscoveryClient = oidctest.Discovery(issuer), oidctest.Client(issuer, fx.jwks)
	t.Setenv(env.DataDirKey, dir) // SpecConfig stated its own; apps read the environment
	// The one constructor every Hanzo program is built from (identity boundary,
	// bridge, edge gates), then the plugin on it, as cloud.Listen composes a child.
	deps := host.SpecDeps(cfg)
	app := cloud.App(spec.Name, cfg, deps, nil)
	app.Use(cloud.DenyEnvelope())
	if err := cloud.UseAll(app, []cloud.Plugin{spec}, cfg, deps); err != nil {
		t.Fatalf("mount %s: %v", spec.Name, err)
	}
	return app
}

// routes is the app's table, one "METHOD pattern" a line.
func routes(app *zip.App) []string {
	seen := map[string]bool{}
	for _, r := range app.Routes() {
		if r.Method == "HEAD" || r.Method == "OPTIONS" || r.Method == "TRACE" || r.Method == "CONNECT" {
			continue
		}
		p := r.Pattern
		if len(p) > 1 {
			p = strings.TrimSuffix(p, "/")
		}
		seen[r.Method+" "+p] = true
	}
	lines := make([]string, 0, len(seen))
	for l := range seen {
		lines = append(lines, l)
	}
	sort.Strings(lines)
	return lines
}

// concrete fills a pattern's parameters and wildcards with a stand-in.
func concrete(pattern string) string {
	segs := strings.Split(pattern, "/")
	for i, seg := range segs {
		if seg == "*" || seg == "+" || strings.HasPrefix(seg, ":") || strings.HasPrefix(seg, "*") {
			segs[i] = "x"
		}
	}
	return strings.Join(segs, "/")
}

// required is the documented example of each op whose input has a required
// field: zip validates input before the op, so {} would be refused alike for
// every caller and say nothing about the gate.
func required(app *zip.App) map[string]json.RawMessage {
	m := app.Manifest()
	needs := map[string]bool{}
	for _, t := range m.Types {
		for _, f := range t.Fields {
			needs[t.ID] = needs[t.ID] || f.Required
		}
	}
	out := map[string]json.RawMessage{}
	for _, op := range m.Ops {
		if needs[op.In] && len(op.Example) > 0 {
			out[op.Method+" "+op.Path] = op.Example
		}
	}
	return out
}

// answer is the gate's decision for one caller at one address.
func answer(app *zip.App, method, path string, example json.RawMessage, headers map[string]string) string {
	if method == "ALL" || method == "USE" || method == "" {
		method = "GET"
	}
	body := "{}"
	switch {
	case example == nil:
	case method == "GET" || method == "DELETE":
		var fields map[string]any
		if json.Unmarshal(example, &fields) == nil {
			q := url.Values{}
			for k, v := range fields {
				q.Set(k, fmt.Sprint(v))
			}
			path += "?" + q.Encode()
		}
	default:
		body = string(example)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		return "in" // past the gate, waiting on whatever is behind the handler
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		// An older dialect refuses inside a 200: {"status":"error","msg":...}.
		var env struct {
			Status string `json:"status"`
		}
		if b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16)); err == nil &&
			json.Unmarshal(b, &env) == nil && env.Status == "error" {
			return "200-error"
		}
	}
	return strconv.Itoa(resp.StatusCode)
}

// succeeded is an answer past every refusal: a 2xx or 3xx, or "in" (the handler
// was still working when the test stopped waiting).
func succeeded(status string) bool {
	if status == "in" {
		return true
	}
	n, err := strconv.Atoi(status)
	return err == nil && n < 400
}

// blind are the sentences an op answers when it cannot see its caller.
var blind = []string{"an org scope is required", "a validated principal is required", "no org on the call"}

// tools calls every read-only, argument-free MCP tool as a signed-in member,
// through the tool endpoint, where no route middleware runs.
func tools(t *testing.T, app, e *zip.App) int {
	called := 0
	cloud.UseMCP(app)
	route := map[string][2]string{}
	for _, op := range app.Registry() {
		route[op.OperationID] = [2]string{op.Method, op.Path}
	}
	for _, tool := range app.MCPTools() {
		name, ok := readOnlyNoArgs(tool)
		if !ok {
			continue
		}
		called++
		t.Run(name, func(t *testing.T) {
			// As a member and as the SuperAdmin: a privileged tool refuses the
			// member on its route too, so only the SuperAdmin's call would show
			// it blind.
			for _, who := range []string{member, super} {
				text, isErr := call(t, name, fx.plane[who])
				if !isErr {
					continue
				}
				// The op's own route is the judge. A caller the route serves must
				// be served by the tool too: the tool is the same op reached by
				// name, and an error there where the route answered is the op not
				// seeing who called.
				st := ""
				if r, ok := route[name]; ok {
					st = answer(e, r[0], concrete(r[1]), nil, fx.headers[who])
				}
				if succeeded(st) {
					t.Errorf("the route served a signed-in %s (%s) and its tool refused: %s", who, st, firstLine(text))
					continue
				}
				// The route did not serve the caller. Unless it refused them
				// outright, a tool that says it cannot see its caller is blind.
				if st == "401" || st == "403" {
					continue
				}
				for _, b := range blind {
					if strings.Contains(text, b) {
						t.Errorf("the tool cannot see a signed-in %s: %s", who, b)
					}
				}
			}
		})
	}
	return called
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	if len(l) > 160 {
		l = l[:160]
	}
	return l
}

func readOnlyNoArgs(tool map[string]any) (string, bool) {
	name, _ := tool["name"].(string)
	ann, _ := tool["annotations"].(map[string]any)
	if ro, _ := ann["readOnlyHint"].(bool); !ro || name == "" {
		return "", false
	}
	schema, _ := tool["inputSchema"].(map[string]any)
	if req, _ := schema["required"].([]any); len(req) > 0 {
		return "", false
	}
	return name, true
}

// call posts one tools/call frame to the plane's tool endpoint as caller.
func call(t *testing.T, name string, caller map[string]string) (text string, isErr bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]any{}})
	body, err := zapmcp.Marshal(&zapmcp.Frame{Kind: zapmcp.Request, ID: "1", Method: "tools/call", Params: params})
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, manifest.MCPPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", zip.CallContentType)
	for k, v := range caller {
		req.Header.Set(k, v)
	}
	resp, err := cloud.Plane().Test(req, zip.TestConfig{Timeout: 20 * time.Second})
	if err != nil {
		return err.Error(), true
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var f zapmcp.Frame
	if zapmcp.Unmarshal(raw, &f) != nil {
		return string(raw), true
	}
	if f.Err != nil {
		return f.Err.Message, true
	}
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(f.Result, &res)
	for _, c := range res.Content {
		text += c.Text
	}
	return text, res.IsError
}
