package answer

// stream_test.go — the WIRE CONTRACT proof. The SSE frames this engine emits are
// the @hanzo/ai SearchEvent union, frame for frame: a client that consumes
// search()/deepResearch() today consumes /v1/ask unchanged. This test reads the
// bytes off the wire and asserts the union — every frame's type, its exact key
// set, all four declared status stages (including `reading`, which nothing used
// to emit), the SearchSource shape, and the terminal sentinel.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// searchEventKeys is the SearchEvent union, verbatim from hanzo-js/ai/src/search.ts:
//
//	| { type:"status"; stage:"searching"|"planning"|"reading"|"answering"; detail?:string }
//	| { type:"sources"; sources:SearchSource[] }
//	| { type:"text"; delta:string }
//	| { type:"follow_ups"; questions:string[] }
//	| { type:"done"; answer:string; sources:SearchSource[] }
//	| { type:"error"; error:string }
//
// `error` is the CLIENT's variant (a transport failure it observes); the server's
// loop never hard-fails, so it is never emitted here — see Sink's doc comment.
var searchEventKeys = map[string]map[string]bool{
	"status":     {"type": true, "stage": true, "detail": false},
	"sources":    {"type": true, "sources": true},
	"text":       {"type": true, "delta": true},
	"follow_ups": {"type": true, "questions": true},
	"done":       {"type": true, "answer": true, "sources": true},
}

var searchEventStages = map[string]bool{"searching": true, "planning": true, "reading": true, "answering": true}

// stubSearch serves a minimal Bing result page so websearch.Search returns real,
// parseable sources — the loop then reaches the read stage with something to read.
func stubSearch(t *testing.T, results map[string]string) {
	t.Helper()
	var b strings.Builder
	for url, title := range results {
		fmt.Fprintf(&b, `<li class="b_algo"><h2><a href="%s">%s</a></h2><p>a snippet about %s</p></li>`, url, title, title)
	}
	page := "<html><body><ol>" + b.String() + "</ol></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WEBSEARCH_BING_URL", srv.URL)
	t.Setenv("WEBSEARCH_ENGINES", "bing")
}

// frames runs the loop into a real sseSink and returns the decoded data frames
// plus the raw wire text.
func frames(t *testing.T, e Engine, p Params) ([]map[string]any, string) {
	t.Helper()
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	e.Run(context.Background(), p, &sseSink{w: w})
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	wire := buf.String()

	var out []map[string]any
	for _, line := range strings.Split(wire, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("frame is not JSON: %q (%v)", payload, err)
		}
		out = append(out, ev)
	}
	return out, wire
}

func TestSSEFramesMatchSearchEventUnion(t *testing.T) {
	stubSearch(t, map[string]string{
		"https://clojure.org/about":                 "About Clojure",
		"https://en.wikipedia.org/wiki/Rich_Hickey": "Rich Hickey",
	})
	fakeCrawl(t, map[string]string{"https://clojure.org/about": "# Clojure\n\nCreated by Rich Hickey."})

	e := newEngine(&loopAI{answer: "Clojure was created by [Rich Hickey](https://clojure.org/about)."})
	evs, wire := frames(t, e, baseParams(modes["research"]))

	if len(evs) == 0 {
		t.Fatalf("no frames emitted:\n%s", wire)
	}
	stages := map[string]bool{}
	var sawSources, sawDone bool
	for i, ev := range evs {
		typ, _ := ev["type"].(string)
		want, known := searchEventKeys[typ]
		if !known {
			t.Fatalf("frame %d: type %q is not in the SearchEvent union", i, typ)
		}
		for k := range ev {
			if _, allowed := want[k]; !allowed {
				t.Fatalf("frame %d (%s): key %q is not in the union's shape", i, typ, k)
			}
		}
		for k, required := range want {
			if required {
				if _, present := ev[k]; !present {
					t.Fatalf("frame %d (%s): required key %q missing", i, typ, k)
				}
			}
		}
		switch typ {
		case "status":
			stage, _ := ev["stage"].(string)
			if !searchEventStages[stage] {
				t.Fatalf("frame %d: stage %q is not in the union", i, stage)
			}
			stages[stage] = true
		case "sources":
			sawSources = true
			assertSourceShape(t, i, ev["sources"])
		case "done":
			sawDone = true
			assertSourceShape(t, i, ev["sources"])
			if ev["answer"] == "" {
				t.Fatalf("frame %d: done must carry the answer", i)
			}
		}
	}

	// All FOUR declared stages are real — `reading` in particular, which the union
	// has always declared and nothing used to emit.
	for _, want := range []string{"planning", "searching", "reading", "answering"} {
		if !stages[want] {
			t.Fatalf("stage %q was never emitted; saw %v\n%s", want, stages, wire)
		}
	}
	if !sawSources || !sawDone {
		t.Fatalf("sources=%v done=%v — both are required frames", sawSources, sawDone)
	}
	if evs[len(evs)-1]["type"] != "done" {
		t.Fatalf("done must be the last typed frame, got %v", evs[len(evs)-1]["type"])
	}
	if !strings.HasSuffix(wire, "data: [DONE]\n\n") {
		t.Fatalf("stream must end with the [DONE] sentinel, got:\n%s", wire)
	}
}

// assertSourceShape checks the SearchSource contract: {url,title,snippet,favicon}
// always present, engine optional. The extension renders every one of these.
func assertSourceShape(t *testing.T, frame int, v any) {
	t.Helper()
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("frame %d: sources must be an array, got %T", frame, v)
	}
	for j, raw := range list {
		s, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("frame %d source %d: not an object", frame, j)
		}
		for _, k := range []string{"url", "title", "snippet", "favicon"} {
			if _, present := s[k]; !present {
				t.Fatalf("frame %d source %d: missing %q (%v)", frame, j, k, s)
			}
		}
		for k := range s {
			switch k {
			case "url", "title", "snippet", "favicon", "engine":
			default:
				t.Fatalf("frame %d source %d: key %q is not in SearchSource", frame, j, k)
			}
		}
	}
}

// TestSSEEmptySourcesStillTerminates proves the degraded path keeps the contract:
// with no search results the loop skips reading, still answers, and still emits a
// terminal done + sentinel — a client is never left hanging.
func TestSSEEmptySourcesStillTerminates(t *testing.T) {
	noNetworkSearch(t)
	fakeCrawl(t, nil)
	evs, wire := frames(t, newEngine(&loopAI{answer: "No sources, honest answer."}), baseParams(modes["deep"]))
	for _, ev := range evs {
		if ev["type"] == "status" && ev["stage"] == "reading" {
			t.Fatal("reading must not be claimed when there is nothing to read")
		}
	}
	if evs[len(evs)-1]["type"] != "done" {
		t.Fatalf("done must terminate the stream:\n%s", wire)
	}
}

// TestSSESourcesAndFollowUpsNeverNull proves the arrays marshal as [] not null —
// a JS client destructures them without a guard.
func TestSSESourcesAndFollowUpsNeverNull(t *testing.T) {
	noNetworkSearch(t)
	fakeCrawl(t, nil)
	_, wire := frames(t, newEngine(&loopAI{answer: "A."}), baseParams(modes["search"]))
	if strings.Contains(wire, "null") {
		t.Fatalf("no frame may carry null:\n%s", wire)
	}
}
