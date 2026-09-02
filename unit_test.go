package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// titled is an empty struct WITH a name, the other half of the comparison below.
// It is the only place in this repo that still spells one.
type titled struct{}

// TestAnAliasAnswersNoContent measures BOTH halves of a 204, because they are
// independent and a reader who has only one of them writes an op that answers
// the wrong thing in the half they were not thinking about.
//
// The DOCUMENT half turns on the Out type having no NAME: no name, no schema, so
// the projection publishes 204 with no content. The WIRE half turns on the
// handler returning a NIL *Out. An op that types its Out as Unit and returns
// &Unit{} documents 204 and then sends 200 — which is the shape this pins.
func TestAnAliasAnswersNoContent(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("unit"), DisableStartupMessage: true})
	zip.Delete(app, "/void", func(context.Context, *struct{ ID string }) (*Unit, error) {
		return nil, nil
	}, zip.WithOperationID("voidProbe"))
	zip.Delete(app, "/value", func(context.Context, *struct{ ID string }) (*Unit, error) {
		return &Unit{}, nil
	}, zip.WithOperationID("valueProbe"))
	zip.Delete(app, "/titled", func(context.Context, *struct{ ID string }) (*titled, error) {
		return nil, nil
	}, zip.WithOperationID("titledProbe"))

	status := func(path string) int {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest(http.MethodDelete, path, nil),
			zip.TestConfig{Timeout: 30_000_000_000, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("DELETE %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	// THE WIRE. A nil Out is what sends 204; a value sends 200 whatever its type.
	if got := status("/void"); got != http.StatusNoContent {
		t.Errorf("an op returning a nil Out sent %d, want 204", got)
	}
	if got := status("/value"); got == http.StatusNoContent {
		t.Errorf("an op returning &Unit{} sent 204 — then the wire does not turn on nil " +
			"after all, and every op written to return nil for that reason needs re-reading")
	}

	// THE DOCUMENT. No name, no schema, so 204 with no content is what gets
	// published; a named empty struct publishes a body an SDK will expect.
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	content := func(id string) (code string, hasBody bool, found bool) {
		for _, op := range reg.Ops {
			if op.OperationID != id {
				continue
			}
			responses, ok := op.Responses.(map[string]any)
			if !ok {
				t.Fatalf("%s: responses are %T, not the object a typed op projects", id, op.Responses)
			}
			for c, raw := range responses {
				body, _ := raw.(map[string]any)
				return c, body["content"] != nil, true
			}
		}
		return "", false, false
	}

	if code, body, ok := content("voidProbe"); !ok {
		t.Error("the unnamed op is not in the typed registry")
	} else if code != "204" || body {
		t.Errorf("an unnamed Out published %s content=%t, want 204 with none", code, body)
	}
	if code, body, ok := content("titledProbe"); !ok {
		t.Error("the named op is not in the typed registry")
	} else if code == "204" && !body {
		t.Errorf("a NAMED empty struct published 204 with no content too (%s) — then the "+
			"document does not turn on the name, and Unit need not be an alias", code)
	}
}
