package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

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

// TestEveryRouteIsTyped fails when a notify operation carries no typed registry
// entry. The whole surface is typed — the send routes were the last holdouts, and
// typing them is what lets a sibling process (IAM's OTP sender) reach them as a
// typed zip.Call instead of hand-rolling HTTP against an undeclared shape.
func TestEveryRouteIsTyped(t *testing.T) {
	served, typed, _ := notifyOps(t)

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Post/... on the group).", strings.Join(untyped, ", "))
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

// sendApp is the send-route harness: the same routes() the binary serves, behind
// the cloud.Bridge the composer installs in production (a subsystem never
// installs its own), with the delivery function replaced so nothing touches a
// real provider.
func sendApp(t *testing.T, send func(ctx context.Context, org, channel, provider string, to []string, subject, body string) (string, error)) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	routes(app, &service{log: luxlog.New("test"), send: send})
	return app
}

// post drives one send. user simulates the SanitizeIdentity-minted X-User-Id
// (present ONLY for a validated bearer); org simulates the minted X-Org-Id.
func post(t *testing.T, app *zip.App, path, org, user string, body map[string]any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	rq := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	rq.Header.Set("Content-Type", "application/json")
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		rq.Header.Set("X-User-Id", user)
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("send %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// TestSendKeepsBothShapes is the conversion's parity proof: the typed op answers
// the exact bytes the raw handler always did. One recipient returns the bare
// {message_id,status} outcome; two return the {items:[…]} envelope. This is the
// pair the two-shape refusal said no typed op could carry — notifyDelivery's
// MarshalJSON carries it, so the routes are typed AND the bytes are unchanged.
func TestSendKeepsBothShapes(t *testing.T) {
	app := sendApp(t, func(context.Context, string, string, string, []string, string, string) (string, error) {
		return "fake", nil
	})

	code, raw := post(t, app, "/v1/notify/send?sync=true", "acme", "u_acme",
		map[string]any{"to": []string{"+15550001"}, "channel": "sms", "body": "hi"})
	if code != http.StatusOK {
		t.Fatalf("one recipient: %d (%s)", code, raw)
	}
	var one map[string]any
	if err := json.Unmarshal(raw, &one); err != nil {
		t.Fatalf("one-recipient decode: %v", err)
	}
	if _, hasItems := one["items"]; hasItems {
		t.Fatal("one recipient returned the items envelope — the bare-outcome fold is broken")
	}
	if one["status"] != "sent" || one["message_id"] == "" {
		t.Fatalf("one recipient body = %v, want a bare {message_id,status} outcome", one)
	}

	code, raw = post(t, app, "/v1/notify/send?sync=true", "acme", "u_acme",
		map[string]any{"to": []string{"+15550001", "+15550002"}, "channel": "sms", "body": "hi"})
	if code != http.StatusOK {
		t.Fatalf("two recipients: %d (%s)", code, raw)
	}
	var many map[string]any
	if err := json.Unmarshal(raw, &many); err != nil {
		t.Fatalf("two-recipient decode: %v", err)
	}
	items, ok := many["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("two recipients body = %v, want {items:[…2]}", many)
	}
}

// TestSendRefusalsKeepTheirStatuses pins every refusal the raw handler answered,
// status for status, on the typed route.
func TestSendRefusalsKeepTheirStatuses(t *testing.T) {
	app := sendApp(t, func(context.Context, string, string, string, []string, string, string) (string, error) {
		return "fake", nil
	})
	for _, tc := range []struct {
		name, path, org, user string
		body                  map[string]any
		want                  int
	}{
		{"no principal", "/v1/notify/send?sync=true", "acme", "", map[string]any{"to": []string{"x"}, "channel": "sms", "body": "hi"}, http.StatusUnauthorized},
		{"no org", "/v1/notify/send?sync=true", "", "u_acme", map[string]any{"to": []string{"x"}, "channel": "sms", "body": "hi"}, http.StatusUnauthorized},
		{"no recipients", "/v1/notify/send?sync=true", "acme", "u_acme", map[string]any{"channel": "sms", "body": "hi"}, http.StatusBadRequest},
		{"no channel", "/v1/notify/send?sync=true", "acme", "u_acme", map[string]any{"to": []string{"x"}, "body": "hi"}, http.StatusBadRequest},
		{"no body or template", "/v1/notify/send?sync=true", "acme", "u_acme", map[string]any{"to": []string{"x"}, "channel": "sms"}, http.StatusBadRequest},
		{"async refused", "/v1/notify/send", "acme", "u_acme", map[string]any{"to": []string{"x"}, "channel": "sms", "body": "hi"}, http.StatusServiceUnavailable},
	} {
		if code, raw := post(t, app, tc.path, tc.org, tc.user, tc.body); code != tc.want {
			t.Errorf("%s: %d (%s), want %d", tc.name, code, raw, tc.want)
		}
	}
}

// TestChannelPinnedRoutesOverrideTheBody proves /send/sms and /send/email fix the
// channel whatever the body names — the contract the per-channel routes exist for.
func TestChannelPinnedRoutesOverrideTheBody(t *testing.T) {
	for path, want := range map[string]string{
		"/v1/notify/send/sms?sync=true":   "sms",
		"/v1/notify/send/email?sync=true": "email",
	} {
		var got string
		app := sendApp(t, func(_ context.Context, _, channel, _ string, _ []string, _, _ string) (string, error) {
			got = channel
			return "fake", nil
		})
		body := map[string]any{"to": []string{"x"}, "channel": "voice", "body": "hi"}
		if want == "sms" {
			body["channel"] = "email"
		} else {
			body["channel"] = "sms"
		}
		if code, raw := post(t, app, path, "acme", "u_acme", body); code != http.StatusOK {
			t.Fatalf("%s: %d (%s)", path, code, raw)
		}
		if got != want {
			t.Errorf("%s delivered on channel %q, want %q", path, got, want)
		}
	}
}

// TestTerminalFailureIsA200WithStatusFailed pins the notifyd contract IAM
// decodes: a provider failure is a 200 whose status is failed with the reason in
// error, never a transport error.
func TestTerminalFailureIsA200WithStatusFailed(t *testing.T) {
	app := sendApp(t, func(context.Context, string, string, string, []string, string, string) (string, error) {
		return "twilio", errors.New("number unreachable")
	})
	code, raw := post(t, app, "/v1/notify/send?sync=true", "acme", "u_acme",
		map[string]any{"to": []string{"+15550001"}, "channel": "sms", "body": "hi"})
	if code != http.StatusOK {
		t.Fatalf("terminal failure: %d (%s), want 200", code, raw)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "failed" || got["error"] != "number unreachable" {
		t.Fatalf("terminal failure body = %v, want status failed with the provider's reason", got)
	}
}

// TestSendIsCallableByName is the projection this conversion exists for: IAM's
// OTP sender reaches these ops as a typed call BY NAME over the socket plane.
// The call encoding computes the input's whole layout before reading a byte of
// payload, so a map field anywhere on the input failed every such call — this
// drives the real socket with template_vars populated to prove the raw-object
// field crosses, and the answer is the one declared shape (no JSON fold on
// this plane).
func TestSendIsCallableByName(t *testing.T) {
	// A unix socket address is capped near a hundred bytes and t.TempDir embeds
	// the test name, so an anonymous short-named dir keeps the address legal.
	sockDir, err := os.MkdirTemp("", "zip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	t.Setenv("ZIP_RUNTIME_DIR", sockDir)

	app := sendApp(t, func(context.Context, string, string, string, []string, string, string) (string, error) {
		return "fake", nil
	})
	go func() { _ = app.Listen(zip.SocketPath("notify")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for i := 0; ; i++ {
		if c, derr := net.Dial("unix", zip.SocketPath("notify")); derr == nil {
			_ = c.Close()
			break
		}
		if i == 200 {
			t.Fatal("notify socket never began listening")
		}
		time.Sleep(10 * time.Millisecond)
	}

	conn, err := zip.Dial(zip.SocketPath("notify"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// A background caller states who it acts for; the callee's identity chain
	// parks it exactly as it parks a gateway assertion.
	ctx := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "u_acme"})
	out, err := zip.Call[notifySend, notifyDelivery](ctx, conn, "post_v1_notify_send", &notifySend{
		To: []string{"+15550001"}, Channel: "sms", Body: "hi",
		TemplateVars: json.RawMessage(`{"code":"123456"}`), Sync: "true",
	})
	if err != nil {
		t.Fatalf("call by name: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].Status != "sent" {
		t.Fatalf("delivery = %+v, want one sent outcome", out)
	}
}
