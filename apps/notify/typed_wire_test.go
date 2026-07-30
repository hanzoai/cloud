package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of notify operations that are NOT typed ops,
// each with the wire fact that keeps it out. A typed op is a route PLUS a registry
// entry — the one value the OpenAPI operation, the MCP tool, the CLI command and
// the SDK method all come from — so an operation missing from that registry is
// invisible to all four. These three are missing on purpose. Addresses are written
// the way the DOCUMENT writes them, which is the identity every projection keys on.
var untypedByDesign = map[string]string{
	"POST /v1/notify/send": "ONE address, TWO 200 shapes: a send to a SINGLE recipient returns the bare " +
		"SendResponse, a send to several returns the {items:[SendResponse]} envelope (notify.go handleSend). " +
		"An op declares exactly one Out.",
	"POST /v1/notify/send/sms":   "same handler, same two 200 shapes as POST /v1/notify/send.",
	"POST /v1/notify/send/email": "same handler, same two 200 shapes as POST /v1/notify/send.",
}

func wireApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// notifyOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. Reading the router (not the source) is what makes this a gate
// rather than prose.
func notifyOps(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := wireApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "notify", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/notify") }
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}
	return served, typed, reg.Schemas
}

// TestEveryRouteIsTypedOrNamed fails when a notify operation is neither a typed op
// nor one of the three named above.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := notifyOps(t)

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with the reason "+
			"typing it would move the wire.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which notify no longer serves", key)
		}
	}
}

// TestEveryTypedOpIsDescribed holds the prose to the same bar as the schema,
// because that prose IS the product surface: it becomes the OpenAPI description
// AND the MCP tool description a model reads to pick the tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := notifyOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed notify ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/notify/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the op-level
// gate cannot see: typing a route documents its ADDRESS and its SHAPE, never the
// shape's FIELDS.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := notifyOps(t)
	var bare []string
	for name, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, ok := sch["properties"].(map[string]any)
		if !ok {
			continue
		}
		for field, praw := range props {
			p, ok := praw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) with no description: %s", strings.Join(bare, ", "))
	}
}

// TestSendAnswersTwoShapes is the MEASUREMENT behind the refusal above, not an
// assertion about it. One recipient returns a bare SendResponse object; two return
// the {items:[…]} envelope. A typed op declares one Out, so as long as this test
// passes the three send routes cannot be typed without moving the wire — and the
// day zip can declare a polymorphic response, this test is the conversion's spec.
func TestSendAnswersTwoShapes(t *testing.T) {
	// Replace the delivery seam so nothing touches a real provider.
	s := &service{log: luxlog.New("test")}
	s.send = func(ctx context.Context, org, channel, provider string, to []string, subject, body string) (string, error) {
		return "fake", nil
	}
	app2 := zip.New(zip.Config{Logger: luxlog.New("test")})
	g := app2.Group("/v1/notify")
	g.Post("/send", s.handleSend(""))

	post := func(to []string) []byte {
		b, _ := json.Marshal(map[string]any{"to": to, "channel": "sms", "body": "hi"})
		rq := httptest.NewRequest(http.MethodPost, "/v1/notify/send?sync=true", bytes.NewReader(b))
		rq.Header.Set("Content-Type", "application/json")
		rq.Header.Set("X-Org-Id", "acme")
		rq.Header.Set("X-User-Id", "u_acme")
		resp, err := app2.Fiber().Test(rq)
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("send %v: %d (%s)", to, resp.StatusCode, raw)
		}
		raw, _ := io.ReadAll(resp.Body)
		return raw
	}

	var one map[string]any
	if err := json.Unmarshal(post([]string{"+15550001"}), &one); err != nil {
		t.Fatalf("one-recipient decode: %v", err)
	}
	if _, hasItems := one["items"]; hasItems {
		t.Fatal("one recipient returned the items envelope — the two-shape refusal is stale, re-check it")
	}
	if one["status"] != "sent" {
		t.Fatalf("one recipient body = %v, want a bare SendResponse", one)
	}

	var many map[string]any
	if err := json.Unmarshal(post([]string{"+15550001", "+15550002"}), &many); err != nil {
		t.Fatalf("two-recipient decode: %v", err)
	}
	items, ok := many["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("two recipients body = %v, want {items:[…2]} — the two-shape refusal is stale", many)
	}
}
