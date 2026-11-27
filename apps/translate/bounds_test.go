package translate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// These pin what bounds one translate call — its size, its time, its caller, its
// model calls — and what a failure may say and bill.

// TestTooMuchTextIs413: a batch past maxTotal is refused before any model is
// asked, and the refusal says to split it.
func TestTooMuchTextIs413(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{render: upper}
		app := mount(t, m)
		batch := make([]string, maxTotal/maxChars+1)
		for i := range batch {
			batch[i] = strings.Repeat("x", maxChars)
		}
		code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", Brief{Batch: batch, Target: "es"})
		if code != http.StatusRequestEntityTooLarge || !strings.Contains(string(raw), "split") {
			t.Fatalf("an oversize batch = %d %s, want 413 telling the caller to split it", code, raw)
		}
		// 512 strings of 8 KiB: every limit but the total holds.
		many := make([]string, maxBatch)
		for i := range many {
			many[i] = strings.Repeat("x", chunkBudget-stringCost)
		}
		if code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", Brief{Batch: many, Target: "es"}); code != http.StatusRequestEntityTooLarge {
			t.Fatalf("512 strings of 8 KiB = %d %s, want 413", code, raw)
		}
		if m.calls != 0 {
			t.Fatalf("an oversize batch reached the model %d times", m.calls)
		}
	})
}

// TestTheDeadlineEndsTheWork: a model that never answers is given up on at the
// call's deadline, and the call answers 502 rather than hanging.
func TestTheDeadlineEndsTheWork(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		saved := deadline
		deadline = 200 * time.Millisecond
		t.Cleanup(func() { deadline = saved })
		m := &model{render: upper, hung: make(chan struct{})}
		app := mount(t, m)
		start := time.Now()
		code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", Brief{Text: "Hello", Target: "es"})
		if code != http.StatusBadGateway || !strings.Contains(string(raw), `"translate_failed"`) {
			t.Fatalf("a call past its deadline = %d %s, want 502 translate_failed", code, raw)
		}
		if took := time.Since(start); took > 10*time.Second {
			t.Fatalf("the call took %s past a 200ms deadline", took)
		}
		select {
		case <-m.hung:
		default:
			t.Fatal("the model call was never cancelled")
		}
	})
}

// TestALeavingCallerEndsTheWork: over a real socket, a caller that sends its
// request and hangs up cancels the model call in flight — fasthttp would not, so
// the connection is asked.
func TestALeavingCallerEndsTheWork(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{render: upper, hung: make(chan struct{})}
		conn := dial(t, mount(t, m))
		ask(conn, `{"text":"Hello","target":"es"}`)
		time.Sleep(300 * time.Millisecond) // the request is in the handler, the model is asked
		_ = conn.Close()
		select {
		case <-m.hung:
		case <-time.After(3 * time.Second):
			t.Fatal("the model was still asked 3s after its caller hung up")
		}
	})
}

// strings4K is n strings of which every two fill a part exactly.
func strings4K(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strings.Repeat(string(rune('a'+i%26)), chunkBudget/2-stringCost)
	}
	return out
}

// TestTwoFailingPartsStopTheCall: a reply that never fits stops the call after two
// parts in a row return nothing; the parts after them are not asked for, and their
// strings say why.
func TestTwoFailingPartsStopTheCall(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{reply: `{"source":"en","translations":[]}`}
		app := mount(t, m)
		code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", Brief{Batch: strings4K(10), Target: "es"})
		if code != http.StatusBadGateway {
			t.Fatalf("a call nothing came back from = %d %s, want 502", code, raw)
		}
		// Two parts, each asked whole and then one string at a time: 2 × (1 + 2).
		if m.calls != 6 {
			t.Fatalf("model calls = %d, want 6: the call goes on past two failing parts", m.calls)
		}
	})
}

// TestTheCallBudgetBoundsTheAsks: a request makes at most two model calls per
// part, the one-string retries included; what the budget does not reach fails
// with a reason, and what it did reach is kept.
func TestTheCallBudgetBoundsTheAsks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{render: upper, merge: true}
		app := mount(t, m)
		out := translate(t, app, "acme", Brief{Batch: []string{"one", "two", "three", "four", "five"}, Target: "es"})
		if m.calls != 2 {
			t.Fatalf("model calls = %d, want 2 for one part", m.calls)
		}
		if out.Translations[0].Text != "ONE" {
			t.Fatalf("the string the budget reached = %+v", out.Translations[0])
		}
		for _, tr := range out.Translations[1:] {
			if tr.Error != whyBudget {
				t.Fatalf("a string past the budget = %+v, want %q", tr, whyBudget)
			}
		}
	})
}

// TestAnUnusablePartIsAskedOneStringAtATime: a model that merges two strings into
// one fails the whole part's reply; asked again one string at a time, every
// string lands, and the other part is untouched.
func TestAnUnusablePartIsAskedOneStringAtATime(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{render: upper, merge: true}
		app := mount(t, m)
		batch := append(strings4K(2), "c")
		out := translate(t, app, "acme", Brief{Batch: batch, Target: "es"})
		if out.Usage.Translated != 3 || out.Usage.Failed != 0 {
			t.Fatalf("usage = %+v, want all three translated", out.Usage)
		}
		if got := sizes(m.inputs); len(got) != 4 || got[0] != 2 || got[1] != 1 || got[2] != 1 || got[3] != 1 {
			t.Fatalf("asks of sizes %v, want [2 1 1 1]", got)
		}
	})
}

// TestAFailureEchoesNoText: when no string comes back the 502 is a fixed sentence
// under a stable code, and a string's own error names the class of what it lost —
// neither repeats the caller's text, and no span does either.
func TestAFailureEchoesNoText(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sr := record(t)
		m := &model{render: func(string) string { return "x" }}
		app := mount(t, m)
		secret := "Reset at https://acme.example/reset?token=SECRET123"
		code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", Brief{Text: secret, Target: "es"})
		if code != http.StatusBadGateway || !strings.Contains(string(raw), `"translate_failed"`) {
			t.Fatalf("all failed = %d %s, want 502 translate_failed", code, raw)
		}
		if strings.Contains(string(raw), "SECRET123") || strings.Contains(string(raw), "acme.example") {
			t.Fatalf("the 502 repeats the caller's text: %s", raw)
		}

		out := translate(t, app, "acme", Brief{Batch: []string{secret, "Hello"}, Target: "es"})
		if got := out.Translations[0].Error; got != "the translation lost a URL" {
			t.Fatalf("a string's error = %q, want the class it lost", got)
		}
		for _, s := range sr.Ended() {
			if strings.Contains(s.Status().Description, "SECRET123") {
				t.Errorf("span %s status carries the caller's text", s.Name())
			}
			for _, kv := range s.Attributes() {
				if strings.Contains(kv.Value.Emit(), "SECRET123") {
					t.Errorf("span %s carries the caller's text in %s", s.Name(), kv.Key)
				}
			}
			for _, ev := range s.Events() {
				for _, kv := range ev.Attributes {
					if strings.Contains(kv.Value.Emit(), "SECRET123") {
						t.Errorf("span %s event carries the caller's text", s.Name())
					}
				}
			}
		}
	})
}

// TestICUBranchesAreStoredAndBulkBillsWhatCameBack: a plural whose branch words
// are translated is a translation, kept and stored; on bulk, a string that failed
// is neither counted nor billed, and a call where every string failed is billed
// nothing at all.
func TestICUBranchesAreStoredAndBulkBillsWhatCameBack(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		plural := "{count, plural, one {file} other {files}}"
		m := &model{render: func(s string) string {
			return strings.NewReplacer("{file}", "{archivo}", "{files}", "{archivos}").Replace(s)
		}}
		app := mount(t, m)
		if got := translate(t, app, "acme", Brief{Text: plural, Target: "es"}).Translations[0]; got.Error != "" || got.Text == "" {
			t.Fatalf("a translated plural = %+v", got)
		}
		if again := translate(t, app, "acme", Brief{Text: plural, Target: "es"}); !again.Translations[0].Cached || m.calls != 1 {
			t.Fatalf("a translated plural was not stored: %+v after %d calls", again.Translations[0], m.calls)
		}

		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var in bulkRequest
			_ = json.NewDecoder(r.Body).Decode(&in)
			out := make([]string, len(in.Texts))
			for i, s := range in.Texts {
				out[i] = strings.ReplaceAll(s, "{name}", "") // MADLAD drops the placeholder
			}
			_ = json.NewEncoder(w).Encode(bulkReply{Translations: out})
		}))
		defer backend.Close()
		t.Setenv("TRANSLATE_BULK_URL", backend.URL)
		priced := 0
		saved := bulkRate
		bulkRate = func(context.Context) int64 { priced++; return 20 }
		t.Cleanup(func() { bulkRate = saved })
		bulkApp := mount(t, &model{render: upper})

		out := translate(t, bulkApp, "acme", Brief{Batch: []string{"Hi {name}", "Bye"}, Target: "es", Tier: TierBulk})
		if out.Usage.Characters != len("Bye") || out.Usage.Failed != 1 {
			t.Fatalf("bulk usage = %+v, want only the string that came back counted", out.Usage)
		}
		priced = 0
		code, raw := call(t, bulkApp, http.MethodPost, "/v1/translate", "acme", Brief{Text: "Hello {name}", Target: "es", Tier: TierBulk})
		if code != http.StatusBadGateway {
			t.Fatalf("bulk with every string failed = %d %s, want 502", code, raw)
		}
		if priced != 1 {
			t.Fatalf("the price was read %d times, want 1 (the gate): a call that returned nothing was billed", priced)
		}
	})
}

// TestOldFormatsKeepTheirReviews: an approved row made before the key carried the
// format answers a text, an html and a markdown call alike, still approved — and a
// memory larger than one migration batch moves whole.
func TestOldFormatsKeepTheirReviews(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		db, err := cloud.OrgDB(t.TempDir(), principal.MustOrgNamespace("acme", ""), "translate")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`
CREATE TABLE entry (key TEXT PRIMARY KEY, source TEXT NOT NULL, target TEXT NOT NULL, glossary TEXT NOT NULL DEFAULT '',
 tier TEXT NOT NULL, text TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'machine', actor TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		insert := `INSERT INTO entry VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`
		if _, err := db.Exec(insert, oldKey("Pay <b>now</b>", "es", "", TierQuality),
			"Pay <b>now</b>", "es", "", "quality", "Pagar <b>ya</b>", "approved", "u-legal", 5); err != nil {
			t.Fatal(err)
		}
		const more = 2*rekeyBatch + 1
		for i := range more {
			src := fmt.Sprintf("string %d", i)
			if _, err := db.Exec(insert, oldKey(src, "es", "", TierQuality), src, "es", "", "quality", "x", "machine", "", 1); err != nil {
				t.Fatal(err)
			}
		}
		mem, err := open(db)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for _, f := range formats {
			got, found, err := mem.recall(ctx, MemoryEntry{Source: "Pay <b>now</b>", Target: "es", Tier: TierQuality, Format: f})
			if err != nil || !found || got.Text != "Pagar <b>ya</b>" || got.State != StateApproved {
				t.Errorf("format %s: %+v found=%v err=%v, want the approved row", f, got, found, err)
			}
		}
		if n := count(t, db); n != 3*(more+1) {
			t.Fatalf("rows = %d, want %d: every old row under three formats", n, 3*(more+1))
		}
	})
}

// dial serves app on a real socket and returns a connection to it.
func dial(t *testing.T, app *zip.App) net.Conn {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	h, err := zip.Serve(app, "http://"+addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	for until := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			t.Cleanup(func() { _ = conn.Close() })
			return conn
		}
		if time.Now().After(until) {
			t.Fatalf("nothing listening at %s", addr)
		}
	}
}

// ask writes one translate request as acme's caller.
func ask(conn net.Conn, body string) {
	fmt.Fprintf(conn, "POST /v1/translate HTTP/1.1\r\nHost: translate\r\nContent-Type: application/json\r\n"+
		"X-Org-Id: acme\r\nX-User-Id: u-acme\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
}

// slow answers after a second and a bit, unless its call is cancelled first.
type slow struct {
	model
	cancelled chan struct{}
}

func (s *slow) ChatCompletion(ctx context.Context, req *infer.ChatRequest) (*infer.ChatResponse, error) {
	select {
	case <-time.After(1200 * time.Millisecond):
	case <-ctx.Done():
		close(s.cancelled)
		return nil, ctx.Err()
	}
	return s.model.ChatCompletion(ctx, req)
}

// TestAStayingCallerIsServed: a caller that waits through a slow model is
// answered, and its call is never cancelled. One that closes its sending side
// after the request reads as gone (hangup.go) and is answered 502.
func TestAStayingCallerIsServed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := &slow{model: model{render: upper}, cancelled: make(chan struct{})}
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		compose(app)
		if err := Use(app, cloud.Deps{AI: s}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = Shutdown() })
		conn := dial(t, app)
		ask(conn, `{"text":"Hello","target":"es"}`)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("a staying caller = %d, want 200", resp.StatusCode)
		}
		select {
		case <-s.cancelled:
			t.Fatal("a staying caller's model call was cancelled")
		default:
		}

		half := dial(t, app)
		ask(half, `{"text":"Bye","target":"es"}`)
		_ = half.(*net.TCPConn).CloseWrite()
		resp, err = http.ReadResponse(bufio.NewReader(half), nil)
		if err != nil {
			t.Fatalf("a half-closed caller got no reply: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("a half-closed caller = %d, want 502", resp.StatusCode)
		}
	})
}

// TestAFullRequestOfBracesIsServed: a request at maxTotal of unmatched braces —
// the input that made a reader search for every brace's match — is answered in a
// call per part, echoed back or not.
func TestAFullRequestOfBracesIsServed(t *testing.T) {
	batch := make([]string, maxTotal/maxChars)
	for i := range batch {
		batch[i] = strings.Repeat("{", maxChars-1) + string(rune('a'+i))
	}
	for name, render := range map[string]func(string) string{
		"echo": func(s string) string { return s },
		"junk": func(string) string { return "x" },
	} {
		// Each reply is its own database: the echo would otherwise be remembered and
		// the junk answered from memory, with no call to the model at all.
		sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
			m := &model{render: render}
			app := mount(t, m)
			start := time.Now()
			code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", Brief{Batch: batch, Target: "es"})
			if code != http.StatusOK && code != http.StatusBadGateway {
				t.Fatalf("%s: %d %s", name, code, raw)
			}
			if took := time.Since(start); took > 20*time.Second {
				t.Errorf("%s: a full request of braces held the handler %s", name, took)
			}
			if m.calls == 0 || m.calls > 2*len(chunks(batch)) {
				t.Errorf("%s: %d model calls for %d parts", name, m.calls, len(chunks(batch)))
			}
		})
	}
}

// TestEveryPartGetsItsOwnAsk: one part whose reply is unusable spends its retries
// out of the asks no later part is owed, so every later part is still asked for
// whole; and a string whose shape fails once and holds the second time costs
// exactly two asks, across a request of maxTotal.
func TestEveryPartGetsItsOwnAsk(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		tiny := make([]string, maxBatch)
		for i := range tiny {
			tiny[i] = "s" + strconv.Itoa(i)
		}
		parts := len(chunks(tiny))
		for name, m := range map[string]*model{
			"unusable everywhere": {reply: `{"source":"en","translations":["only one"]}`},
			"merging":             {render: upper, merge: true},
		} {
			app := mount(t, m)
			call(t, app, http.MethodPost, "/v1/translate", "acme", Brief{Batch: tiny, Target: "es"})
			whole := 0
			for _, r := range m.seen {
				var in []string
				_ = json.Unmarshal([]byte(r.Prompt[strings.LastIndex(r.Prompt, "INPUT: ")+len("INPUT: "):]), &in)
				if len(in) > 1 {
					whole++
				}
			}
			if whole != parts {
				t.Errorf("%s: %d of %d parts were asked for whole", name, whole, parts)
			}
			if m.calls > 2*parts {
				t.Errorf("%s: %d model calls past the budget of %d", name, m.calls, 2*parts)
			}
		}

		n := maxTotal / 4065
		batch := make([]string, n)
		for i := range batch {
			id := strconv.Itoa(i)
			batch[i] = "{x} " + strings.Repeat("y", 4065-4-len(id)) + id
		}
		asked := map[string]int{}
		flaky := &model{render: func(s string) string {
			if asked[s]++; asked[s] == 1 {
				return strings.Replace(s, "{x}", "", 1)
			}
			return s
		}}
		out := translate(t, mount(t, flaky), "acme", Brief{Batch: batch, Target: "es"})
		if out.Usage.Translated != n || flaky.calls != 2*len(chunks(batch)) {
			t.Fatalf("flaky shapes: usage %+v after %d calls, want all %d translated in %d", out.Usage, flaky.calls, n, 2*len(chunks(batch)))
		}
	})
}

// TestEngineErrorsLogTheirClass: an engine's error can quote what it was sent; the
// log line names its class, and neither it nor the reply carries the text.
func TestEngineErrorsLogTheirClass(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var logs bytes.Buffer
		prev := luxlog.Default()
		luxlog.SetDefault(luxlog.NewWriter(&logs))
		t.Cleanup(func() { luxlog.SetDefault(prev) })
		secret := "Reset at https://acme.example/reset?token=SECRET123"
		m := &model{err: errors.New("status code: 400, message: flagged input: " + secret)}
		app := mount(t, m)
		code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", Brief{Text: secret, Target: "es"})
		if code != http.StatusBadGateway || strings.Contains(string(raw), "SECRET123") {
			t.Fatalf("engine error = %d %s", code, raw)
		}
		if !strings.Contains(logs.String(), "translate engine failed") || !strings.Contains(logs.String(), `"class":"engine"`) {
			t.Fatalf("no engine failure logged with its class: %s", logs.String())
		}
		if strings.Contains(logs.String(), "SECRET123") {
			t.Fatalf("the log carries the caller's text: %s", logs.String())
		}
	})
}
