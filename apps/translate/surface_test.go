package translate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/spec"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestTheSpecTypesTheTranslateBody: the document carries POST /v1/translate's
// request body, with target required, and its success response, with the
// translations — what every SDK generated from it needs to call the route at all.
func TestTheSpecTypesTheTranslateBody(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mount(t, &model{render: upper})
		doc, err := spec.Of(app, spec.Info{Title: "translate", Version: "v1"})
		if err != nil {
			t.Fatalf("spec: %v", err)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		type media map[string]struct {
			Schema struct {
				Ref string `json:"$ref"`
			} `json:"schema"`
		}
		var d struct {
			Paths map[string]map[string]struct {
				RequestBody struct {
					Content media `json:"content"`
				} `json:"requestBody"`
				Responses map[string]struct {
					Content media `json:"content"`
				} `json:"responses"`
			} `json:"paths"`
			Components struct {
				Schemas map[string]struct {
					Required   []string                   `json:"required"`
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"schemas"`
			} `json:"components"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatalf("decode: %v", err)
		}
		op, ok := d.Paths["/v1/translate"]["post"]
		if !ok {
			t.Fatal("no POST /v1/translate in the document")
		}
		named := func(ref string) string { return strings.TrimPrefix(ref, "#/components/schemas/") }

		in := d.Components.Schemas[named(op.RequestBody.Content["application/json"].Schema.Ref)]
		if !slices.Contains(in.Required, "target") {
			t.Errorf("request body required = %v, want target among them", in.Required)
		}
		for _, field := range []string{"text", "batch", "target", "source", "tier", "model", "glossary", "format"} {
			if _, ok := in.Properties[field]; !ok {
				t.Errorf("request body has no %q", field)
			}
		}
		success, ok := op.Responses["2XX"]
		if !ok {
			t.Fatalf("no success response: %v", op.Responses)
		}
		out := d.Components.Schemas[named(success.Content["application/json"].Schema.Ref)]
		for _, field := range []string{"translations", "usage", "tier", "model"} {
			if _, ok := out.Properties[field]; !ok {
				t.Errorf("success response has no %q", field)
			}
		}
		if _, ok := d.Components.Schemas["Translation"].Properties["error"]; !ok {
			t.Error("a translation states no error field")
		}
	})
}

// TestModelReachesThePlane: a named model is the one the model plane is asked for,
// marked as translate's; none named is the default; an upstream family name is the
// default too, as aimodel.ZenModel makes it everywhere.
func TestModelReachesThePlane(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{render: upper}
		app := mount(t, m)
		for n, tc := range []struct{ named, sent string }{
			{"acme/llama-3-70b", "acme/llama-3-70b"},
			{"", aimodel.DefaultModel},
			{"qwen3-32b", aimodel.DefaultModel},
		} {
			out := translate(t, app, "acme", Brief{Text: tc.sent + tc.named, Target: "es", Model: tc.named})
			req := m.seen[n]
			if req.Model != tc.sent || out.Model != tc.sent {
				t.Errorf("model %q: sent %q, reported %q, want %q", tc.named, req.Model, out.Model, tc.sent)
			}
			if req.Feature != feature || req.MaxTokens <= 0 || req.Org != "acme" {
				t.Errorf("model %q: feature %q, max tokens %d, org %q", tc.named, req.Feature, req.MaxTokens, req.Org)
			}
		}
	})
}

// TestModelIsCheckedAndBulkRefusesIt: a model id is bounded in length and
// charset, and naming one on the bulk tier — which runs no model — is refused, on
// the translate call and on the review write alike.
func TestModelIsCheckedAndBulkRefusesIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{render: upper}
		app := mount(t, m)
		for name, b := range map[string]Brief{
			"bulk with a model":   {Text: "x", Target: "es", Tier: TierBulk, Model: "acme-llama"},
			"a space":             {Text: "x", Target: "es", Model: "acme llama"},
			"too long":            {Text: "x", Target: "es", Model: strings.Repeat("m", 129)},
			"a leading separator": {Text: "x", Target: "es", Model: "/etc"},
		} {
			if code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", b); code != http.StatusBadRequest {
				t.Errorf("%s = %d %s, want 400", name, code, raw)
			}
		}
		if code, raw := call(t, app, http.MethodPut, "/v1/translate/memory", "acme", ReviewRequest{
			Source: "x", Target: "es", Tier: TierBulk, Model: "acme-llama", Text: "y", State: StateApproved,
		}); code != http.StatusBadRequest {
			t.Errorf("review on bulk with a model = %d %s, want 400", code, raw)
		}
		if m.calls != 0 {
			t.Fatalf("a refused brief reached the model %d times", m.calls)
		}
	})
}

// record swaps the global provider, which is what tracer() reads at each call.
func record(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev); _ = tp.Shutdown(context.Background()) })
	return sr
}

// spans returns the ended spans named name, and the attributes of each as strings.
func spans(sr *tracetest.SpanRecorder, name string) []map[string]string {
	var out []map[string]string
	for _, s := range sr.Ended() {
		if s.Name() != name {
			continue
		}
		attrs := map[string]string{}
		for _, kv := range s.Attributes() {
			attrs[string(kv.Key)] = kv.Value.Emit()
		}
		out = append(out, attrs)
	}
	return out
}

// TestOneSpanPerCallCarriesTheMarker: every translate call is one span marked
// hanzo.feature=translate, carrying its tags and counts; a call the memory answers
// whole says it stored nothing and asked no engine; and no span carries a string
// the caller sent.
func TestOneSpanPerCallCarriesTheMarker(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sr := record(t)
		app := mount(t, &model{render: upper})
		translate(t, app, "acme", Brief{Text: "secret one", Target: "es"})
		translate(t, app, "acme", Brief{Batch: []string{"secret one", "secret two"}, Target: "es"})
		translate(t, app, "acme", Brief{Batch: []string{"secret one", "secret two"}, Target: "es"})

		got := spans(sr, "translate")
		if len(got) != 3 {
			t.Fatalf("translate spans = %d, want one per call", len(got))
		}
		for name, want := range map[string]string{
			"hanzo.feature": feature, "hanzo.org": "acme", "hanzo.translate.tier": "quality",
			"hanzo.translate.target": "es", "hanzo.translate.model": aimodel.DefaultModel,
			"hanzo.translate.strings": "2", "hanzo.translate.cached": "1", "hanzo.translate.translated": "1",
			"hanzo.translate.failed": "0", "hanzo.translate.characters": "10", "hanzo.translate.chunks": "1",
			"hanzo.translate.retries": "0", "hanzo.translate.stored": "1",
		} {
			if got[1][name] != want {
				t.Errorf("second call %s = %q, want %q", name, got[1][name], want)
			}
		}
		for name, want := range map[string]string{
			"hanzo.translate.cached": "2", "hanzo.translate.stored": "0", "hanzo.translate.chunks": "0",
		} {
			if got[2][name] != want {
				t.Errorf("fully cached call %s = %q, want %q", name, got[2][name], want)
			}
		}
		for _, s := range sr.Ended() {
			for _, kv := range s.Attributes() {
				if strings.Contains(kv.Value.Emit(), "secret") {
					t.Errorf("span %s carries customer text in %s", s.Name(), kv.Key)
				}
			}
		}
	})
}

// TestBulkHasASpanOfItsOwn: the bulk tier asks no model, so its backend call is a
// span of its own, marked, with its counts.
func TestBulkHasASpanOfItsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sr := record(t)
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in bulkRequest
			_ = json.NewDecoder(r.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(bulkReply{Translations: in.Texts})
		}))
		defer backend.Close()
		t.Setenv("TRANSLATE_BULK_URL", backend.URL)
		app := mount(t, &model{render: upper})
		translate(t, app, "acme", Brief{Batch: []string{"secret"}, Target: "es", Tier: TierBulk})

		bulk := spans(sr, "translate.bulk")
		if len(bulk) != 1 || bulk[0]["hanzo.feature"] != feature || bulk[0]["hanzo.translate.strings"] != "1" ||
			bulk[0]["http.response.status_code"] != "200" {
			t.Fatalf("bulk spans = %v", bulk)
		}
		if top := spans(sr, "translate"); len(top) != 1 || top[0]["hanzo.translate.tier"] != "bulk" || top[0]["hanzo.translate.model"] != "" {
			t.Fatalf("translate span on bulk = %v", top)
		}
	})
}
