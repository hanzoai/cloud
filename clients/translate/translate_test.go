package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// testCfg gives a request room to open the encrypted per-org memory on a loaded
// box; fiber's 1s default trips on the first cold open, not on the handler.
var testCfg = fiber.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true}

// ---- harness ----

// model is a fake model plane. It counts calls and answers the exact envelope the
// quality engine requires, so a test can prove how many strings actually REACHED a
// model — which is the whole point of the translation memory.
type model struct {
	mu     sync.Mutex
	calls  int
	texts  int // strings sent to the model across all calls
	render func(string) string
	reply  string // when set, returned verbatim (to exercise a malformed envelope)
	err    error
}

func (m *model) ChatCompletion(_ context.Context, req *cloud.ChatRequest) (*cloud.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if m.reply != "" {
		return &cloud.ChatResponse{Content: m.reply}, nil
	}
	// Recover the INPUT array the prompt carries; the fake renders each string.
	i := strings.LastIndex(req.Prompt, "INPUT: ")
	var in []string
	if err := json.Unmarshal([]byte(req.Prompt[i+len("INPUT: "):]), &in); err != nil {
		return nil, err
	}
	m.texts += len(in)
	out := make([]string, len(in))
	for n, s := range in {
		out[n] = m.render(s)
	}
	env, _ := json.Marshal(map[string]any{"source": "en", "translations": out})
	return &cloud.ChatResponse{Content: string(env)}, nil
}

func (m *model) Embed(context.Context, *cloud.EmbedRequest) ([][]float32, error) { return nil, nil }

func (m *model) count() (calls, texts int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, m.texts
}

// drift makes the model answer differently from here on — the non-determinism the
// translation memory exists to absorb.
func (m *model) drift() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.render = func(string) string { return "DRIFT" }
}

func upper(s string) string { return strings.ToUpper(s) }

// mount brings up the real /v1/translate surface over a temp DataDir and the fake
// model plane — the real router, the real memory, the real handlers.
func mount(t *testing.T, m *model) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), AIDefaultModel: "zen"}
	if m != nil {
		deps.AI = m
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// call issues a request with a validated principal (X-Org-Id + X-User-Id, the pair
// the gateway mints only from a verified credential); org "" exercises the
// no-principal path.
func call(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// translate posts a request and decodes the reply, failing on a non-200.
func translate(t *testing.T, app *zip.App, org string, in Request) Response {
	t.Helper()
	code, raw := call(t, app, http.MethodPost, "/v1/translate", org, in)
	if code != http.StatusOK {
		t.Fatalf("POST /v1/translate = %d, want 200: %s", code, raw)
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode reply: %v (%s)", err, raw)
	}
	return out
}

// ---- the memory is normative ----

// TestMemoryIsIdempotent is the HIP-0516 property that replaces what Crowdin was
// actually providing: an unchanged source string yields a byte-identical
// translation on a rebuild, and never reaches a model twice.
func TestMemoryIsIdempotent(t *testing.T) {
	m := &model{render: upper}
	app := mount(t, m)
	in := Request{Batch: []string{"Hello", "Goodbye"}, Target: "es"}

	first := translate(t, app, "acme", in)
	if got := []string{first.Translations[0].Text, first.Translations[1].Text}; got[0] != "HELLO" || got[1] != "GOODBYE" {
		t.Fatalf("first pass = %v", got)
	}
	if first.Usage != (Usage{Strings: 2, Cached: 0, Translated: 2, Characters: 12}) {
		t.Fatalf("first usage = %+v", first.Usage)
	}
	if first.DetectedSource != "en" {
		t.Fatalf("detected_source = %q, want en", first.DetectedSource)
	}

	// The model now renders differently. A rebuild must NOT see it.
	m.drift()
	second := translate(t, app, "acme", in)
	for i := range second.Translations {
		if second.Translations[i].Text != first.Translations[i].Text {
			t.Fatalf("rebuild rewrote %q: %q -> %q", in.Batch[i], first.Translations[i].Text, second.Translations[i].Text)
		}
		if !second.Translations[i].Cached {
			t.Fatalf("string %d not served from memory", i)
		}
	}
	if second.Usage != (Usage{Strings: 2, Cached: 2, Translated: 0, Characters: 0}) {
		t.Fatalf("rebuild usage = %+v, want fully cached", second.Usage)
	}
	if calls, texts := m.count(); calls != 1 || texts != 2 {
		t.Fatalf("model saw %d calls / %d strings, want 1/2", calls, texts)
	}
}

// TestOnlyChangedStringsReachAModel: adding one string to a locale sends exactly
// that one string to the model.
func TestOnlyChangedStringsReachAModel(t *testing.T) {
	m := &model{render: upper}
	app := mount(t, m)
	translate(t, app, "acme", Request{Batch: []string{"one", "two"}, Target: "fr"})
	out := translate(t, app, "acme", Request{Batch: []string{"one", "two", "three"}, Target: "fr"})

	if out.Usage.Cached != 2 || out.Usage.Translated != 1 || out.Usage.Characters != len("three") {
		t.Fatalf("usage = %+v, want 2 cached / 1 translated / 5 chars", out.Usage)
	}
	if _, texts := m.count(); texts != 3 { // 2 on the first pass, 1 on the second
		t.Fatalf("model saw %d strings, want 3", texts)
	}
	if out.Translations[2].Text != "THREE" || out.Translations[2].Cached {
		t.Fatalf("new string = %+v", out.Translations[2])
	}
}

// TestKeyCoversGlossaryAndTier proves the key is the full HIP-0516 tuple: the same
// source under a different glossary, or a different tier, is a different entry.
func TestKeyCoversGlossaryAndTier(t *testing.T) {
	if key("s", "es", "", TierQuality) == key("s", "es", "v1", TierQuality) {
		t.Fatal("glossary version is not part of the key")
	}
	if key("s", "es", "", TierQuality) == key("s", "es", "", TierBulk) {
		t.Fatal("tier is not part of the key")
	}
	if key("s", "es", "", TierQuality) == key("s", "pt", "", TierQuality) {
		t.Fatal("target is not part of the key")
	}
	// The version is derived from the terms, so an edited glossary self-invalidates
	// without anyone remembering to bump a number.
	if version(map[string]string{"cloud": "nube"}) == version(map[string]string{"cloud": "nubes"}) {
		t.Fatal("glossary version does not track its terms")
	}
	if version(nil) != "" {
		t.Fatal("an absent glossary must version as empty")
	}
}

// TestGlossaryChangeRetranslates: editing the glossary re-keys the affected strings,
// so the new terminology actually lands.
func TestGlossaryChangeRetranslates(t *testing.T) {
	m := &model{render: upper}
	app := mount(t, m)
	translate(t, app, "acme", Request{Text: "cloud", Target: "es", Glossary: map[string]string{"cloud": "nube"}})
	out := translate(t, app, "acme", Request{Text: "cloud", Target: "es", Glossary: map[string]string{"cloud": "nubes"}})
	if out.Usage.Translated != 1 || out.Translations[0].Cached {
		t.Fatalf("an edited glossary must re-translate: %+v", out)
	}
}

// ---- the review lane ----

// TestApprovedIsImmuneToMachineChurn is the trust property: once a human approves a
// string, a rebuild returns it unchanged and no model runs for it.
func TestApprovedIsImmuneToMachineChurn(t *testing.T) {
	m := &model{render: upper}
	app := mount(t, m)
	translate(t, app, "acme", Request{Text: "Hello", Target: "es"})

	code, raw := call(t, app, http.MethodPut, "/v1/translate/memory", "acme", ReviewRequest{
		Source: "Hello", Target: "es", Text: "¡Hola!", State: StateApproved,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT memory = %d: %s", code, raw)
	}

	m.drift()
	out := translate(t, app, "acme", Request{Text: "Hello", Target: "es"})
	got := out.Translations[0]
	if got.Text != "¡Hola!" || got.State != StateApproved || !got.Cached {
		t.Fatalf("rebuild reverted human work: %+v", got)
	}
	if calls, _ := m.count(); calls != 1 {
		t.Fatalf("model ran %d times, want 1 (the approved string must not reach it)", calls)
	}
}

// TestMachineWriteCannotOverwriteHumanWork is the invariant at the store, not just
// at the handler: whatever order writes arrive in, a machine write only ever
// creates a row or refreshes one still at machine.
func TestMachineWriteCannotOverwriteHumanWork(t *testing.T) {
	db, err := cloud.OrgDB(t.TempDir(), "acme", "", "translate")
	if err != nil {
		t.Fatalf("org db: %v", err)
	}
	mem, err := open(db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = mem.Close() }()
	ctx := context.Background()

	base := Entry{Source: "Hello", Target: "es", Tier: TierQuality, Text: "HELLO", State: StateMachine, UpdatedAt: 1}
	if err := mem.put(ctx, base, false); err != nil {
		t.Fatalf("machine put: %v", err)
	}

	for _, human := range []State{StateSuggested, StateApproved, StatePublished} {
		reviewed := base
		reviewed.Text, reviewed.State, reviewed.Actor, reviewed.UpdatedAt = "¡Hola!", human, "reviewer", 2
		if err := mem.put(ctx, reviewed, true); err != nil {
			t.Fatalf("human put: %v", err)
		}
		churn := base
		churn.Text, churn.UpdatedAt = "DRIFT", 3
		if err := mem.put(ctx, churn, false); err != nil {
			t.Fatalf("machine churn: %v", err)
		}
		got, found, err := mem.get(ctx, key(base.Source, base.Target, base.Glossary, base.Tier))
		if err != nil || !found {
			t.Fatalf("get: %v found=%v", err, found)
		}
		if human == StateSuggested {
			// A suggestion is not yet approved work, so the ladder still lets a
			// machine refresh it — but only because the state says so.
			continue
		}
		if got.Text != "¡Hola!" || got.State != human {
			t.Fatalf("machine write overwrote %s work: %+v", human, got)
		}
	}
}

// TestReviewRejectsMachineState: machine is engine-only. A human may not demote a
// string back into the churn.
func TestReviewRejectsMachineState(t *testing.T) {
	app := mount(t, &model{render: upper})
	code, _ := call(t, app, http.MethodPut, "/v1/translate/memory", "acme", ReviewRequest{
		Source: "Hello", Target: "es", Text: "x", State: StateMachine,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT state=machine = %d, want 400", code)
	}
}

// TestMemoryListFilters covers the review-lane read.
func TestMemoryListFilters(t *testing.T) {
	app := mount(t, &model{render: upper})
	translate(t, app, "acme", Request{Batch: []string{"a", "b"}, Target: "es"})
	translate(t, app, "acme", Request{Text: "a", Target: "fr"})
	if code, _ := call(t, app, http.MethodPut, "/v1/translate/memory", "acme", ReviewRequest{
		Source: "a", Target: "es", Text: "A!", State: StateApproved,
	}); code != http.StatusOK {
		t.Fatal("approve failed")
	}

	entries := func(query string) []Entry {
		code, raw := call(t, app, http.MethodGet, "/v1/translate/memory"+query, "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("GET memory%s = %d: %s", query, code, raw)
		}
		var out struct {
			Data []Entry `json:"data"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, raw)
		}
		return out.Data
	}
	if n := len(entries("")); n != 3 {
		t.Fatalf("unfiltered = %d entries, want 3", n)
	}
	if n := len(entries("?target=fr")); n != 1 {
		t.Fatalf("target=fr = %d entries, want 1", n)
	}
	approved := entries("?state=approved")
	if len(approved) != 1 || approved[0].Text != "A!" || approved[0].Actor != "u-acme" {
		t.Fatalf("state=approved = %+v", approved)
	}
}

// ---- tenancy ----

// TestMemoryIsPerOrg: one org's memory is never read by another. Physical
// isolation (HIP-0302) makes it structural, not a WHERE clause.
func TestMemoryIsPerOrg(t *testing.T) {
	m := &model{render: upper}
	app := mount(t, m)
	translate(t, app, "acme", Request{Text: "Hello", Target: "es"})
	if code, _ := call(t, app, http.MethodPut, "/v1/translate/memory", "acme", ReviewRequest{
		Source: "Hello", Target: "es", Text: "acme-secret", State: StateApproved,
	}); code != http.StatusOK {
		t.Fatal("approve failed")
	}

	out := translate(t, app, "other", Request{Text: "Hello", Target: "es"})
	if out.Translations[0].Cached || out.Translations[0].Text == "acme-secret" {
		t.Fatalf("cross-tenant read of a translation memory: %+v", out.Translations[0])
	}
	code, raw := call(t, app, http.MethodGet, "/v1/translate/memory", "other", nil)
	if code != http.StatusOK || strings.Contains(string(raw), "acme-secret") {
		t.Fatalf("cross-tenant list leak: %d %s", code, raw)
	}
}

func TestPrincipalRequired(t *testing.T) {
	app := mount(t, &model{render: upper})
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/translate"},
		{http.MethodGet, "/v1/translate/memory"},
		{http.MethodPut, "/v1/translate/memory"},
	} {
		if code, _ := call(t, app, tc.method, tc.path, "", Request{Text: "x", Target: "es"}); code != http.StatusUnauthorized {
			t.Errorf("%s %s with no principal = %d, want 401", tc.method, tc.path, code)
		}
	}
}

// ---- tier routing ----

// TestBulkWithoutBackendFailsHonestly: a deployment that does not serve MADLAD says
// so. It must never quietly serve — or charge — the request on the quality tier.
func TestBulkWithoutBackendFailsHonestly(t *testing.T) {
	t.Setenv("TRANSLATE_BULK_URL", "")
	m := &model{render: upper}
	app := mount(t, m)
	code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", Request{Text: "Hello", Target: "es", Tier: TierBulk})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("bulk with no backend = %d, want 503: %s", code, raw)
	}
	if calls, _ := m.count(); calls != 0 {
		t.Fatalf("bulk fell back to the model plane (%d calls)", calls)
	}
}

// TestBulkRoutesToTheBackend: with MADLAD served, bulk reaches it — and its results
// land in the memory under the bulk key, so a rebuild is idempotent there too.
func TestBulkRoutesToTheBackend(t *testing.T) {
	var seen bulkRequest
	hits := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewDecoder(r.Body).Decode(&seen)
		out := make([]string, len(seen.Texts))
		for i, s := range seen.Texts {
			out[i] = "madlad:" + s
		}
		_ = json.NewEncoder(w).Encode(bulkReply{Translations: out, DetectedSource: "en"})
	}))
	defer backend.Close()
	t.Setenv("TRANSLATE_BULK_URL", backend.URL)

	m := &model{render: upper}
	app := mount(t, m)
	out := translate(t, app, "acme", Request{Batch: []string{"Hello"}, Target: "pt-BR", Tier: TierBulk})
	if out.Tier != TierBulk || out.Translations[0].Text != "madlad:Hello" || out.DetectedSource != "en" {
		t.Fatalf("bulk reply = %+v", out)
	}
	if seen.Target != "pt-BR" || seen.Format != FormatText {
		t.Fatalf("bulk backend saw %+v", seen)
	}
	if calls, _ := m.count(); calls != 0 {
		t.Fatalf("bulk reached the model plane (%d calls)", calls)
	}
	// Same call again: the memory answers, the backend is not hit twice.
	again := translate(t, app, "acme", Request{Batch: []string{"Hello"}, Target: "pt-BR", Tier: TierBulk})
	if !again.Translations[0].Cached || hits != 1 {
		t.Fatalf("bulk is not memoized: cached=%v hits=%d", again.Translations[0].Cached, hits)
	}
	// The quality tier is a DIFFERENT key, so it re-translates on the model plane.
	q := translate(t, app, "acme", Request{Batch: []string{"Hello"}, Target: "pt-BR"})
	if q.Translations[0].Text != "HELLO" || q.Translations[0].Cached {
		t.Fatalf("quality read the bulk entry: %+v", q.Translations[0])
	}
}

// TestBulkBackendFailureIsNotAFallback: an unreachable or erroring MADLAD is a 502,
// never a silent re-route to the tier the caller did not ask for.
func TestBulkBackendFailureIsNotAFallback(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer backend.Close()
	t.Setenv("TRANSLATE_BULK_URL", backend.URL)

	m := &model{render: upper}
	app := mount(t, m)
	code, _ := call(t, app, http.MethodPost, "/v1/translate", "acme", Request{Text: "Hello", Target: "es", Tier: TierBulk})
	if code != http.StatusBadGateway {
		t.Fatalf("failing bulk backend = %d, want 502", code)
	}
	if calls, _ := m.count(); calls != 0 {
		t.Fatalf("bulk fell back to the model plane (%d calls)", calls)
	}
}

// TestQualityIsTheDefault covers the HIP-0516 default.
func TestQualityIsTheDefault(t *testing.T) {
	m := &model{render: upper}
	app := mount(t, m)
	if out := translate(t, app, "acme", Request{Text: "Hello", Target: "es"}); out.Tier != TierQuality {
		t.Fatalf("tier = %q, want quality", out.Tier)
	}
	if calls, _ := m.count(); calls != 1 {
		t.Fatalf("model calls = %d, want 1", calls)
	}
}

// ---- engine contract ----

// TestQualityRejectsAShortEnvelope: a reply that does not cover every input is an
// error. Storing a mis-aligned translation under a source string would poison the
// memory permanently.
func TestQualityRejectsAShortEnvelope(t *testing.T) {
	m := &model{reply: `{"source":"en","translations":["only one"]}`}
	app := mount(t, m)
	code, _ := call(t, app, http.MethodPost, "/v1/translate", "acme", Request{Batch: []string{"a", "b"}, Target: "es"})
	if code != http.StatusBadGateway {
		t.Fatalf("short envelope = %d, want 502", code)
	}
	// Nothing was written: the next call still reaches the model.
	if out, _, err := func() (Entry, bool, error) {
		mem, err := mounted.stores.For("acme", "")
		if err != nil {
			return Entry{}, false, err
		}
		return mem.get(context.Background(), key("a", "es", "", TierQuality))
	}(); err != nil || out.Text != "" {
		t.Fatalf("a failed call must write nothing: %+v %v", out, err)
	}
}

// TestQualityAcceptsAFencedEnvelope: models wrap JSON in ``` fences.
func TestQualityAcceptsAFencedEnvelope(t *testing.T) {
	m := &model{reply: "```json\n{\"source\":\"de\",\"translations\":[\"hallo\"]}\n```"}
	app := mount(t, m)
	out := translate(t, app, "acme", Request{Text: "hello", Target: "de"})
	if out.Translations[0].Text != "hallo" || out.DetectedSource != "de" {
		t.Fatalf("fenced envelope = %+v", out)
	}
}

// TestDetectedSourceOnlyWhenNotGiven: a caller who declared the source gets no
// detection field back.
func TestDetectedSourceOnlyWhenNotGiven(t *testing.T) {
	app := mount(t, &model{render: upper})
	out := translate(t, app, "acme", Request{Text: "Hello", Target: "es", Source: "en"})
	if out.DetectedSource != "" {
		t.Fatalf("detected_source = %q, want empty when source is given", out.DetectedSource)
	}
}

// TestModelPlaneUnavailable: no AI client wired ⇒ the quality tier says so rather
// than fabricating a translation.
func TestModelPlaneUnavailable(t *testing.T) {
	app := mount(t, nil)
	code, _ := call(t, app, http.MethodPost, "/v1/translate", "acme", Request{Text: "Hello", Target: "es"})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no model plane = %d, want 503", code)
	}
}

// ---- request validation ----

func TestRequestValidation(t *testing.T) {
	app := mount(t, &model{render: upper})
	for name, body := range map[string]Request{
		"no target":     {Text: "x"},
		"bad target":    {Text: "x", Target: "not a tag"},
		"bad source":    {Text: "x", Target: "es", Source: "??"},
		"no text":       {Target: "es"},
		"text or batch": {Text: "x", Batch: []string{"y"}, Target: "es"},
		"bad tier":      {Text: "x", Target: "es", Tier: "cheap"},
		"bad format":    {Text: "x", Target: "es", Format: "docx"},
		"empty batch":   {Batch: []string{}, Target: "es"},
	} {
		if code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", body); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, code, raw)
		}
	}
	big := Request{Batch: make([]string, maxBatch+1), Target: "es"}
	if code, _ := call(t, app, http.MethodPost, "/v1/translate", "acme", big); code != http.StatusBadRequest {
		t.Errorf("oversize batch = %d, want 400", code)
	}
	long := Request{Text: strings.Repeat("x", maxChars+1), Target: "es"}
	if code, _ := call(t, app, http.MethodPost, "/v1/translate", "acme", long); code != http.StatusBadRequest {
		t.Errorf("oversize string = %d, want 400", code)
	}
}

// ---- pricing ----

// TestBulkPricing: the bulk debit is proportional to the characters that actually
// reached the backend, and the operator knob overrides the policy default.
func TestBulkPricing(t *testing.T) {
	if got, want := bulkMicros(1000), defaultBulkPriceUUSDPer1kChars; got != want {
		t.Fatalf("bulkMicros(1000) = %d, want %d", got, want)
	}
	if bulkMicros(0) != 0 {
		t.Fatal("a fully-cached rebuild must cost nothing")
	}
	t.Setenv("TRANSLATE_PRICE_UUSD_PER_1K_CHARS", "500")
	if got := bulkMicros(2000); got != 1000 {
		t.Fatalf("override rate: bulkMicros(2000) = %d, want 1000", got)
	}
	t.Setenv("TRANSLATE_PRICE_UUSD_PER_1K_CHARS", "-1")
	if got := bulkMicros(1000); got != defaultBulkPriceUUSDPer1kChars {
		t.Fatalf("a bad rate must fall through to the default, got %d", got)
	}
	t.Setenv("TRANSLATE_PRICE_UUSD_PER_1K_CHARS", "0")
	if got := bulkMicros(1_000_000); got != 0 {
		t.Fatalf("rate 0 must make the tier free, got %d", got)
	}
}

// TestPromptCarriesTheContract proves the quality prompt states everything the
// decoder then enforces: the target, the format, the glossary and the arity.
func TestPromptCarriesTheContract(t *testing.T) {
	p := prompt(Job{
		Texts: []string{"a", "b"}, Target: "ja", Source: "en", Format: FormatMarkdown,
		Glossary: map[string]string{"cloud": "クラウド"},
	})
	for _, want := range []string{"ja", "from en", string(FormatMarkdown), "cloud = クラウド", "exactly 2 strings", `INPUT: ["a","b"]`} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q:\n%s", want, p)
		}
	}
}

func TestUnfence(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":1}`:                    `{"a":1}`,
		"```json\n{\"a\":1}\n```":    `{"a":1}`,
		"```\n{\"a\":1}\n```":        `{"a":1}`,
		"  ```json\n{\"a\":1}\n``` ": `{"a":1}`,
	} {
		if got := unfence(in); got != want {
			t.Errorf("unfence(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestKeyIsUnambiguous: no two distinct tuples can collide by concatenation.
func TestKeyIsUnambiguous(t *testing.T) {
	seen := map[string]string{}
	for _, tuple := range [][4]string{
		{"a", "bc", "", "quality"},
		{"ab", "c", "", "quality"},
		{"a", "b", "c", "quality"},
	} {
		k := key(tuple[0], tuple[1], tuple[2], Tier(tuple[3]))
		if prev, dup := seen[k]; dup {
			t.Fatalf("key collision: %v and %s", tuple, prev)
		}
		seen[k] = fmt.Sprint(tuple)
	}
}
