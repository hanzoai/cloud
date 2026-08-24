package plugin

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// changesSomething is the CLOSED list of this surface's operations that CHANGE
// something. All three run fleet-wide, and reload LOADS AN ARTIFACT THE CALLER NAMES —
// a url and a digest — into every host. Each asks core.Change, which is the operator
// admission plus the estate's anti-forgery control.
//
// This family answers under /v1/admin and is driven from the same browser session as
// the rest of that board, so it is the same ambient cookie: an operator reading a page
// elsewhere is enough for that page to act as them. It took core.Admit — the READ gate
// — where every family inside apps/admin takes core.Change.
var changesSomething = map[string]string{
	"POST /v1/admin/plugins/:name/reload":  "loads a caller-named artifact into every host",
	"POST /v1/admin/plugins/:name/enable":  "brings a subsystem up fleet-wide",
	"POST /v1/admin/plugins/:name/disable": "takes a subsystem down fleet-wide",
}

// TestEveryChangeIsClassified makes forgetting structurally impossible: the ledger and
// what this package registers must sum, so an operation added without deciding whether
// it changes anything goes red here rather than shipping uncontrolled.
func TestEveryChangeIsClassified(t *testing.T) {
	z := zip.New(zip.Config{Logger: luxlog.New("test")})
	Routes(z, &ops{z: z})
	seen := map[string]bool{}
	for _, r := range z.Routes() {
		if !strings.HasPrefix(r.Pattern, "/v1/admin/plugins") {
			continue
		}
		addr := strings.ToUpper(r.Method) + " " + r.Pattern
		seen[addr] = true
		_, named := changesSomething[addr]
		if consumes(r.Method) && !named {
			t.Errorf("%s is registered and %s changes state — say what it does in "+
				"changesSomething and give it core.Change", addr, r.Method)
		}
	}
	for addr := range changesSomething {
		if !seen[addr] {
			t.Errorf("changesSomething names %s, which this package does not register", addr)
		}
	}
}

// TestEveryChangeAsksTheControl drives each change TWICE: once as a browser carrying
// only an ambient session cookie and no echoed token, which must be refused BY THE
// CONTROL; and once as a caller who PRESENTED a credential, which must not be.
//
// The pair is what makes each row mean something. A refusal on its own would pass
// against a surface that refuses everything — "SuperAdmin required" is what an
// unattested caller gets whether or not the control ever ran.
func TestEveryChangeAsksTheControl(t *testing.T) {
	z := zip.New(zip.Config{Logger: luxlog.New("test")})
	z.Use(cloud.Bridge())
	Routes(z, &ops{z: z})
	for addr, what := range changesSomething {
		t.Run(addr, func(t *testing.T) {
			path := strings.ReplaceAll(addr[strings.IndexByte(addr, ' ')+1:], ":name", "billing")
			forged := drive(t, z, path, map[string]string{"Cookie": "session=v"})
			if !strings.Contains(forged, "CSRF") {
				t.Errorf("an ambient cross-origin request answered %q — this operation %s and "+
					"must ask the control", clip(forged), what)
			}
			presented := drive(t, z, path, map[string]string{
				"Cookie": "session=v", "Authorization": "Bearer sk-test",
			})
			if strings.Contains(presented, "CSRF") {
				t.Errorf("a caller who presented a credential was refused by the control (%q) — "+
					"they cannot be forged into, and the row above measured nothing", clip(presented))
			}
		})
	}
}

// TestTheControlCoversEverySeam drives the reload by NAME, at a path that is not its
// own, which is how MCP and the call plane address it — no route middleware is reached
// there, so only a control inside the operation answers. With no body at all, which
// both seams take as happily as a full one, and which is the shape a cross-origin page
// can send under a simple content type with no preflight to consult.
func TestTheControlCoversEverySeam(t *testing.T) {
	z := zip.New(zip.Config{Logger: luxlog.New("test")})
	z.Use(cloud.Bridge())
	Routes(z, &ops{z: z})
	ambient := map[string]string{"Cookie": "session=v"}

	if got := body(t, z, zip.CallPath+"adminReloadPlugin", "", ambient); !strings.Contains(got, "CSRF") {
		t.Errorf("call plane adminReloadPlugin with an ambient cookie answered %q — the control did not run", clip(got))
	}
	frame := `{"kind":"request","id":1,"method":"tools/call","params":{"name":"adminReloadPlugin","arguments":{}}}`
	if got := body(t, z, "/mcp", frame, ambient); !strings.Contains(got, "CSRF") {
		t.Errorf("MCP tools/call adminReloadPlugin with an ambient cookie answered %q — the control did not run", clip(got))
	}
}

func consumes(m string) bool {
	switch strings.ToUpper(m) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func drive(t *testing.T, z *zip.App, path string, headers map[string]string) string {
	t.Helper()
	return body(t, z, path, "{}", headers)
}

func body(t *testing.T, z *zip.App, path, payload string, headers map[string]string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(payload)))
	req.Header.Set("Content-Type", "application/json")
	// A VALIDATED principal, so the ambient path means something: the identity boundary
	// sets these only from a credential it verified.
	req.Header.Set("X-User-Id", "u-1")
	req.Header.Set("X-Org-Id", "admin")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := z.Test(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
