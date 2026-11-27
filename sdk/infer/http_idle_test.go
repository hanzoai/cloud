package infer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// streaming is a model API that writes n tokens, one per tick, and then holds the
// stream open saying nothing until its client hangs up; ended closes then.
func streaming(t *testing.T, n int, tick time.Duration) (url string, ended <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for i := 0; i < n; i++ {
			time.Sleep(tick)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok \"}}]}\n\n")
			f.Flush()
		}
		<-r.Context().Done()
		close(done)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv.URL, done
}

func idleGap(t *testing.T, d time.Duration) {
	t.Helper()
	was := streamIdle
	streamIdle = d
	t.Cleanup(func() { streamIdle = was })
}

// A stream that falls silent is hung up on at the idle gap, so the model upstream
// stops, and the tokens it did send are the answer.
func TestAStalledStreamIsHungUpAtTheIdleGap(t *testing.T) {
	idleGap(t, 200*time.Millisecond)
	url, ended := streaming(t, 3, 10*time.Millisecond)
	ai := HTTPAt(url, "k", FixedModel("m")).(*httpAI)
	got := make(chan string, 1)
	go func() {
		resp, _ := ai.ChatStream(context.Background(), &ChatRequest{Prompt: "q"}, func(string) error { return nil })
		if resp != nil {
			got <- resp.Content
		}
		close(got)
	}()
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("a stream silent for 3s was never hung up on")
	}
	if c := <-got; strings.Count(c, "tok") != 3 {
		t.Fatalf("the tokens sent before the silence are the answer, got %q", c)
	}
}

// A stream that keeps producing tokens is never cut, however long it runs: each
// frame re-arms the gap.
func TestASteadyStreamIsNeverCut(t *testing.T) {
	idleGap(t, 100*time.Millisecond)
	url, _ := streaming(t, 20, 40*time.Millisecond) // 800ms, eight gaps long
	ai := HTTPAt(url, "k", FixedModel("m")).(*httpAI)
	resp, err := ai.ChatStream(context.Background(), &ChatRequest{Prompt: "q"}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(resp.Content, "tok"); n != 20 {
		t.Fatalf("a steady stream was cut at %d of 20 tokens", n)
	}
}

// The gap covers the first frame, so it has to clear a local model's TTFT.
func TestTheIdleGapClearsALocalFirstToken(t *testing.T) {
	if streamIdle < 2*time.Minute {
		t.Errorf("streamIdle is %v; a local model's first token can take two minutes", streamIdle)
	}
}
