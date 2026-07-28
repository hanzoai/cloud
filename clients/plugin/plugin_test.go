package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// TestRoutes_LandInTheTypedRegistry is the point of typing these ops at all: the
// registry is what the OpenAPI document, the MCP tool list and the generated CLI
// are projections of, so a route that lands here needs no second definition to
// appear in any of them. A raw handler would serve the same bytes and be
// invisible to all three.
func TestRoutes_LandInTheTypedRegistry(t *testing.T) {
	z := zip.New(zip.Config{AppName: "host", DisableStartupMessage: true})
	Routes(z, &ops{z: z})

	spec := z.OpenAPISpec()
	paths, ok := spec["paths"].(map[string]map[string]any)
	if !ok {
		t.Fatalf("OpenAPI spec has no paths (%T)", spec["paths"])
	}
	want := map[string]struct{ method, opID string }{
		"/v1/admin/plugins":                {"get", "adminPlugins"},
		"/v1/admin/plugins/{name}/reload":  {"post", "adminReloadPlugin"},
		"/v1/admin/plugins/{name}/enable":  {"post", "adminEnablePlugin"},
		"/v1/admin/plugins/{name}/disable": {"post", "adminDisablePlugin"},
	}
	for path, w := range want {
		item, ok := paths[path]
		if !ok {
			t.Errorf("%s missing from the OpenAPI projection", path)
			continue
		}
		op, ok := item[w.method].(map[string]any)
		if !ok {
			t.Errorf("%s has no %s operation", path, w.method)
			continue
		}
		if got := op["operationId"]; got != w.opID {
			t.Errorf("%s %s operationId = %v, want %s", w.method, path, got, w.opID)
		}
	}
}

// TestDrift pins the one judgement this view makes. An unreachable host must not
// be read as a plugin being down, and two digests running at once must read as
// drift — that is the difference between "the rollout is finished" and "the
// rollout is stuck", which is the question the board exists to answer.
func TestDrift(t *testing.T) {
	hosts := []Host{
		{Host: "cloud-0", Plugins: []zip.PluginStatus{
			{Name: "billing", Running: true, Version: "aaa"},
			{Name: "search", Running: true, Version: "ccc"},
		}},
		{Host: "cloud-1", Plugins: []zip.PluginStatus{
			{Name: "billing", Running: true, Version: "bbb"}, // mid-rollout
			{Name: "search", Disabled: true},
		}},
		{Host: "cloud-2", Err: "dial tcp: connection refused", Plugins: nil},
	}

	got := map[string]Drift{}
	for _, d := range drift(hosts) {
		got[d.Name] = d
	}
	if len(got) != 2 {
		t.Fatalf("drift produced %d rows, want 2", len(got))
	}
	if b := got["billing"]; !b.Drifted || len(b.Versions) != 2 || b.Running != 2 {
		t.Errorf("billing = %+v, want drifted with 2 versions across 2 running", b)
	}
	if s := got["search"]; s.Drifted || s.Running != 1 || s.Disabled != 1 || s.Down != 0 {
		t.Errorf("search = %+v, want 1 running + 1 disabled, not drifted", s)
	}
	// The unreachable host contributed nothing at all.
	if got["billing"].Down != 0 {
		t.Errorf("an unreachable host was counted as down: %+v", got["billing"])
	}
}

// TestArtifact_RefusesUnverified proves the control plane cannot be talked into
// installing an unpinned artifact. zip refuses a URL without a Sum; so does the
// route in front of it, so the refusal is reported as a clean 'error' envelope
// rather than surfacing from three layers down.
func TestArtifact_RefusesUnverified(t *testing.T) {
	o := &ops{}
	for _, tc := range []struct {
		name string
		in   ReloadIn
		want string
	}{
		{"url without sum", ReloadIn{URL: "https://s3.hanzo.ai/x"}, "unverified"},
		{"both version and url", ReloadIn{Version: "v1", URL: "https://s3.hanzo.ai/x", Sum: "ab"}, "not both"},
		{"version with no origin", ReloadIn{Version: "v1"}, OriginEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := o.artifact(context.Background(), &tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// A url WITH a sum is the pinned form and must pass through untouched.
	got, err := o.artifact(context.Background(), &ReloadIn{URL: "https://s3.hanzo.ai/x", Sum: "abc"})
	if err != nil || got.URL == "" || got.Sum != "abc" {
		t.Fatalf("artifact(url+sum) = %+v, %v", got, err)
	}
	// Neither means "restart what is loaded", which is a legitimate request.
	got, err = o.artifact(context.Background(), &ReloadIn{})
	if err != nil || got.URL != "" || got.Sum != "" {
		t.Fatalf("artifact(empty) = %+v, %v — want an empty spec meaning restart", got, err)
	}
}

// TestKnown proves the generated manifest is the authority on which apps exist,
// so a typo is refused here rather than three layers down.
func TestKnown(t *testing.T) {
	o := &ops{}
	if len(manifest.Apps) == 0 {
		t.Skip("no generated manifest")
	}
	real := manifest.Apps[0].Name
	if err := o.known(real); err != nil {
		t.Errorf("known(%q) = %v, want nil", real, err)
	}
	if err := o.known("definitely-not-an-app"); err == nil {
		t.Error("known() accepted a name the manifest does not declare")
	}
}

// TestRun_RefusesWithoutAuditStore is the AU-5 refusal: a lifecycle change can
// take production down, so a deployment that cannot durably record it does not
// get to make it. Same discipline as a credit grant refusing to move money it
// cannot account for.
func TestRun_RefusesWithoutAuditStore(t *testing.T) {
	ran := false
	o := &ops{self: "cloud-0"} // no audit recorder
	out, err := o.run(context.Background(), nil, act{
		name: "billing", action: "plugin.reload", scope: scopeHost,
		here: func() error { ran = true; return nil },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.Status != core.Err || !strings.Contains(out.Msg, "audit") {
		t.Fatalf("out = %+v, want an error envelope naming the missing audit store", out)
	}
	if ran {
		t.Fatal("the operation ran anyway — the refusal must come BEFORE the change")
	}
}

// TestVerb pins the route segment an action fans out to. The audit action and
// the route share a word on purpose, so this stays a suffix rather than a table
// that can drift from the routes above.
func TestVerb(t *testing.T) {
	for action, want := range map[string]string{
		"plugin.reload": "reload", "plugin.enable": "enable", "plugin.disable": "disable",
	} {
		if got := verb(action); got != want {
			t.Errorf("verb(%q) = %q, want %q", action, got, want)
		}
	}
}
