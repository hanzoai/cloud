package answer

// hangup_test.go — a streamed answer's model call ends with its reader, and runs
// for as long as its tokens come rather than to a wall clock. Both are proved over
// a real socket through Serve, because the fault being guarded is exactly what a
// direct call cannot show: a request whose context never ends when its client
// leaves.

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/infer"
)

// tickingAI streams the synthesis one token per tick, n tokens or forever when n
// is 0, until its context ends. ended closes when the model was hung up on.
type tickingAI struct {
	loopAI
	tick  time.Duration
	n     int
	once  sync.Once
	ended chan struct{}
}

func (f *tickingAI) ChatStream(ctx context.Context, _ *infer.ChatRequest, emit func(string) error) (*infer.ChatResponse, error) {
	var b strings.Builder
	for i := 0; f.n == 0 || i < f.n; i++ {
		select {
		case <-ctx.Done():
			f.once.Do(func() { close(f.ended) })
			return &infer.ChatResponse{Content: b.String()}, nil
		case <-time.After(f.tick):
		}
		b.WriteString("tok ")
		if err := emit("tok "); err != nil {
			return &infer.ChatResponse{Content: b.String()}, nil
		}
	}
	return &infer.ChatResponse{Content: b.String(), CompletionTokens: f.n}, nil
}

// serveAsk serves e's streamed search answer on a real socket and returns its URL.
func serveAsk(t *testing.T, e Engine) string {
	t.Helper()
	noNetworkSearch(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	stream, follow := true, false
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Raw(http.MethodPost, "/v1/ask", func(c *zip.Ctx) error {
		return e.Serve(c, Request{Mode: "search", Stream: &stream, FollowUps: &follow}, "who created clojure")
	})
	h, err := zip.Serve(app, "http://"+addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	for deadline := time.Now().Add(5 * time.Second); ; {
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing listening at %s", addr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "http://" + addr + "/v1/ask"
}

// ask opens the stream as an org's caller.
func askStream(t *testing.T, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "u-acme")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A reader who hangs up mid-answer hangs up the model: its call is cancelled in
// seconds, not left generating to the mode's deadline for nobody.
func TestAReaderWhoLeavesStopsTheModel(t *testing.T) {
	ai := &tickingAI{tick: 10 * time.Millisecond, ended: make(chan struct{})}
	resp := askStream(t, serveAsk(t, newEngine(ai)))
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && !strings.Contains(sc.Text(), `"type":"text"`) {
	}
	_ = resp.Body.Close() // the reader leaves mid-answer
	select {
	case <-ai.ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the model was still generating 3s after its reader left")
	}
}

// A streamed answer still producing tokens is not cut at the mode's deadline: it
// runs as long as its tokens come.
func TestASteadyStreamOutlivesTheDeadline(t *testing.T) {
	m := modes["search"]
	short := m
	short.deadline = 300 * time.Millisecond
	modes["search"] = short
	t.Cleanup(func() { modes["search"] = m })

	ai := &tickingAI{tick: 20 * time.Millisecond, n: 40, ended: make(chan struct{})} // 800ms of tokens
	resp := askStream(t, serveAsk(t, newEngine(ai)))
	defer resp.Body.Close()
	var answer string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		payload, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var ev struct{ Type, Answer string }
		if json.Unmarshal([]byte(payload), &ev) == nil && ev.Type == "done" {
			answer = ev.Answer
		}
	}
	if got := strings.Count(answer, "tok"); got != 40 {
		t.Fatalf("the answer was cut at %d of 40 tokens: a steady stream met a wall clock", got)
	}
}
