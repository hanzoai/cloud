package answer

// stream.go — the streamed envelope and its two sinks. The event shapes match the
// @hanzo/ai SearchEvent union EXACTLY (type-discriminated), so a client that
// consumes the SDK's search()/deepResearch() stream consumes this unchanged:
// status → sources → status → text… → follow_ups → done.
//
// ONE loop, two deliveries: Run() drives a Sink; sseSink writes SSE frames for a
// streaming client, bufferSink accumulates the final answer/sources/follow-ups for
// a single JSON reply. Neither leaks into the loop — Run() only calls Sink methods.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zap-proto/zip"
)

// maxAnswerChunk caps a single streamed text delta (runes) when the AI plane
// cannot token-stream, so a long answer is still delivered progressively at word
// boundaries rather than one giant frame.
const maxAnswerChunk = 200

// Sink receives the loop's envelope events — one method per SearchEvent variant,
// which keeps the loop declarative. Implemented by sseSink (streaming) and
// bufferSink (JSON).
//
// There is no fail(): the loop never hard-fails. A down model degrades to an
// honest answer + a done frame (and is not billed), a down crawl degrades to
// snippets — so the client ALWAYS gets a terminal frame. The union's `error`
// variant is the CLIENT's (a transport failure it observes), never the server's.
type Sink interface {
	status(stage, detail string)
	sources(s []Source)
	text(delta string)
	followUps(qs []string)
	done(answer string, s []Source)
}

// ── SSE sink ─────────────────────────────────────────────────────────────────

// sseSink writes each event as an SSE frame (`data: <json>\n\n`) and flushes, so
// the browser/SDK renders sources, progress, and the answer as they arrive. The
// JSON self-describes via `type`, so a data-only SSE reader needs no `event:` line.
type sseSink struct{ w *bufio.Writer }

func (s *sseSink) frame(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return
	}
	_ = s.w.Flush()
}

func (s *sseSink) status(stage, detail string) {
	e := map[string]any{"type": "status", "stage": stage}
	if detail != "" {
		e["detail"] = detail
	}
	s.frame(e)
}
func (s *sseSink) sources(src []Source) {
	s.frame(map[string]any{"type": "sources", "sources": nonNilSrc(src)})
}
func (s *sseSink) text(delta string) { s.frame(map[string]any{"type": "text", "delta": delta}) }
func (s *sseSink) followUps(qs []string) {
	s.frame(map[string]any{"type": "follow_ups", "questions": nonNilStr(qs)})
}
func (s *sseSink) done(answer string, src []Source) {
	s.frame(map[string]any{"type": "done", "answer": answer, "sources": nonNilSrc(src)})
	// A terminal [DONE] sentinel mirrors the OpenAI SSE convention so a generic
	// reader knows the stream is complete even if it ignores the typed done frame.
	_, _ = s.w.WriteString("data: [DONE]\n\n")
	_ = s.w.Flush()
}

// ── buffer sink ──────────────────────────────────────────────────────────────

// bufferSink accumulates the terminal result for the non-stream JSON reply. It
// keeps the last sources and the final answer (from done) and the follow-ups —
// status/text deltas are progress-only and not retained.
type bufferSink struct {
	answer string
	srcs   []Source
	follow []string
}

func (b *bufferSink) status(string, string) {}
func (b *bufferSink) sources(s []Source)    { b.srcs = s }
func (b *bufferSink) text(string)           {}
func (b *bufferSink) followUps(qs []string) { b.follow = qs }
func (b *bufferSink) done(answer string, s []Source) {
	b.answer = answer
	if s != nil {
		b.srcs = s
	}
}

// ── stream negotiation ───────────────────────────────────────────────────────

// wantsStream reports whether to stream SSE. Explicit `stream` in the body wins;
// otherwise an `Accept: text/event-stream` (the SDK) or `?stream=1` opts in. Default
// is a single JSON reply — friendlier for curl and simple clients.
func wantsStream(c *zip.Ctx, req Request) bool {
	if req.Stream != nil {
		return *req.Stream
	}
	if strings.Contains(c.Header("Accept"), "text/event-stream") {
		return true
	}
	return strings.TrimSpace(c.Query("stream")) == "1"
}

// setStreamHeaders writes the SSE response headers (never buffer at a proxy).
func setStreamHeaders(c *zip.Ctx) {
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("Connection", "keep-alive")
	c.SetHeader("X-Accel-Buffering", "no")
}

// ── text helpers ─────────────────────────────────────────────────────────────

// chunkText splits s into pieces of at most size runes, breaking at the last
// whitespace before the limit so words and markdown links stay intact. A short
// string yields one chunk; empty yields none.
func chunkText(s string, size int) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	r := []rune(s)
	if size <= 0 || len(r) <= size {
		return []string{s}
	}
	var out []string
	for len(r) > 0 {
		if len(r) <= size {
			out = append(out, string(r))
			break
		}
		cut := size
		for cut > 0 && !isSpace(r[cut]) {
			cut--
		}
		if cut == 0 { // no space in the window — hard split
			cut = size
		}
		out = append(out, string(r[:cut]))
		// skip the boundary space so it is not duplicated at the next chunk's head
		for cut < len(r) && isSpace(r[cut]) {
			cut++
		}
		r = r[cut:]
	}
	return out
}

func isSpace(r rune) bool { return r == ' ' || r == '\n' || r == '\t' || r == '\r' }

// sourcesBlock renders the numbered grounding context the model synthesizes over.
func sourcesBlock(src []Source) string {
	if len(src) == 0 {
		return "(no web sources were found — answer from general knowledge and say so)"
	}
	var b strings.Builder
	for i, s := range src {
		fmt.Fprintf(&b, "[%d] %s\n%s\n%s\n\n", i+1, s.Title, s.URL, s.Snippet)
	}
	return strings.TrimRight(b.String(), "\n")
}

func nonNilSrc(s []Source) []Source {
	if s == nil {
		return []Source{}
	}
	return s
}
func nonNilStr(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
