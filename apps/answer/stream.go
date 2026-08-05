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
// alive reports whether the client is still receiving. It is the loop's only
// window onto the socket: one research answer costs five minutes, three dozen page
// fetches and up to eight completions, and without this a browser tab closed a
// second in buys all of it. A sink with no socket (the JSON reply, a test) is
// always alive.
type Sink interface {
	status(stage, detail string)
	sources(s []Source)
	text(delta string)
	followUps(qs []string)
	done(answer string, s []Source)
	alive() bool
}

// ── SSE sink ─────────────────────────────────────────────────────────────────

// sseSink writes each event as an SSE frame (`data: <json>\n\n`) and flushes, so
// the browser/SDK renders sources, progress, and the answer as they arrive. The
// JSON self-describes via `type`, so a data-only SSE reader needs no `event:` line.
type sseSink struct {
	w *bufio.Writer
	// gone latches on the first failed write: the reader hung up, and every frame
	// after it is work done for nobody. A stream cannot un-disconnect, so once set
	// it stays set.
	gone bool
}

func (s *sseSink) frame(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		s.gone = true
		return
	}
	if err := s.w.Flush(); err != nil {
		s.gone = true
	}
}

func (s *sseSink) alive() bool { return !s.gone }

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
func (b *bufferSink) alive() bool           { return true }
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

// joinWindow caps how long a joiner will hold text waiting for a link to close.
// A model that emits a lone `[` and then prose must not stall the stream, so the
// buffer is released unconditionally at this many runes.
const joinWindow = 512

// joiner keeps a markdown link whole across streamed deltas. A citation arrives
// as `[Rich`, ` Hickey](https://`, `clojure.org)` — rendered as they land, the
// reader watches raw brackets and a half URL appear and then rewrite themselves.
// From the first `[` the text is held until the closing `)` (or joinWindow) and
// released in one piece.
//
// Holding a link whole is also what lets the stream apply the SAME citation check
// the finished answer gets (cite): a link split across two frames could not be
// checked at all, and the streamed text would keep a citation the `done` frame
// drops. allow is the gathered source set; a nil allow flattens every link.
//
// It is a DELIVERY property only: every consumer accumulates answer+delta, so the
// finished text is identical either way. It wraps the synthesis emit and nothing
// else — status, sources and follow-ups are not prose and must never be held.
type joiner struct {
	buf   strings.Builder
	emit  func(string)
	allow map[string]bool
}

func (j *joiner) write(delta string) {
	j.buf.WriteString(delta)
	for {
		s := j.buf.String()
		if s == "" {
			j.buf.Reset()
			return
		}
		i := strings.IndexByte(s, '[')
		switch {
		case i < 0: // nothing open — everything is safe to release
			j.release(s, "")
			return
		case i > 0: // release what precedes the link, keep the link open
			j.release(s[:i], s[i:])
		default: // the buffer starts at '['
			k := linkEnd(s)
			if k < 0 {
				if len([]rune(s)) >= joinWindow {
					j.release(s, "")
				}
				return
			}
			j.release(s[:k], s[k:])
		}
	}
}

// linkEnd returns the index just past the markdown link at s[0]=='[', or -1 while
// the link is still arriving.
//
// Parentheses inside the target are BALANCED: `[Clojure](…/Clojure_(programming_
// language))` is one link, and stopping at the first `)` would split the very
// citations a research answer leans on hardest. A `[` that turns out not to open a
// link is released as soon as that is known, so ordinary prose is never held.
func linkEnd(s string) int {
	close := strings.IndexByte(s, ']')
	if close < 0 {
		return -1 // still inside the link text
	}
	if close+1 >= len(s) {
		return -1 // cannot yet tell whether a target follows
	}
	if s[close+1] != '(' {
		return close + 1 // `[not a link]` — release it
	}
	depth := 0
	for i := close + 1; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// release emits out — with its citations checked — and leaves keep buffered.
func (j *joiner) release(out, keep string) {
	j.buf.Reset()
	j.buf.WriteString(keep)
	if out = cite(out, j.allow); out != "" {
		j.emit(out)
	}
}

// flush releases whatever is still held — the answer ended mid-link, or ended
// inside a bracket that was never a link at all. It goes out through release, so
// the last frame is checked exactly like every frame before it.
func (j *joiner) flush() {
	if s := j.buf.String(); s != "" {
		j.release(s, "")
	}
}

// reset drops the buffer without emitting: the completion it belonged to was
// discarded, so its half-frame must not leak into the next model's stream.
func (j *joiner) reset() { j.buf.Reset() }

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
