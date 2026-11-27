package infer

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// planeStream is a plane whose ai answers a streamed completion with handler,
// framed onto one end of a pipe and read from the other, as ai_chat_stream and
// planeTrip do across a unix socket. The trailer carries price, or none when
// price is negative.
type planeStream struct {
	handler http.HandlerFunc
	price   int64
	ended   chan struct{}
}

func (p *planeStream) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	if !IsStream(body) {
		return nil, errors.New("the plane was asked for a buffered completion")
	}
	near, far := net.Pipe()
	go func() {
		defer close(p.ended)
		defer func() { _ = far.Close() }()
		sw := NewStreamWriter(far)
		p.handler(sw, r)
		_ = sw.End(p.price, p.price >= 0)
	}()
	return StreamResponse(near, r)
}

func sse(w http.ResponseWriter, content string) {
	_, _ = io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"`+content+`"},"finish_reason":null}]}`+"\n\n")
}

func planeClient(t *testing.T, p *planeStream) StreamCompleter {
	t.Helper()
	c, ok := Via(HTTPAt("http://ai.plane", "", FixedModel("m")), p)
	if !ok {
		t.Fatal("no plane client")
	}
	return c.(StreamCompleter)
}

// Deltas cross the plane as the model writes them, not once it has finished, and
// the price told at the end reaches the caller's Bill once.
func TestAStreamCrossesThePlaneADeltaAtATimeAndIsPricedOnce(t *testing.T) {
	second := make(chan struct{})
	p := &planeStream{price: 1234, ended: make(chan struct{}), handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sse(w, "Hel")
		<-second // the caller must have the first delta before the model writes on
		sse(w, "lo")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}}
	ctx, bill := WithBill(context.Background())
	var said []string
	resp, err := planeClient(t, p).ChatStream(ctx, &ChatRequest{Prompt: "hi"}, func(d string) error {
		said = append(said, d)
		if len(said) == 1 {
			close(second)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello" || strings.Join(said, "|") != "Hel|lo" {
		t.Errorf("content %q in deltas %v, want Hello as Hel|lo", resp.Content, said)
	}
	if n, told := bill.Nanos(); !told || n != 1234 {
		t.Errorf("the bill was told %d (%v), want the price once", n, told)
	}
}

// A caller that stops reading after its first delta still learns what the call
// cost: Close reads on to the trailer.
func TestAStreamLeftEarlyIsStillPriced(t *testing.T) {
	p := &planeStream{price: 900, ended: make(chan struct{}), handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 5 {
			sse(w, "tok ")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}}
	ctx, bill := WithBill(context.Background())
	resp, err := planeClient(t, p).ChatStream(ctx, &ChatRequest{Prompt: "hi"}, func(string) error {
		return errors.New("the client hung up")
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(resp.Content, "tok") != 1 {
		t.Errorf("content %q, want what arrived before the hang-up", resp.Content)
	}
	<-p.ended
	if n, told := bill.Nanos(); !told || n != 900 {
		t.Errorf("the bill was told %d (%v), want the price of the whole call", n, told)
	}
}

// A refusal crosses as the status and body the model API wrote, which the caller
// reads as the error it would over HTTP, and nothing is priced.
func TestARefusedStreamReadsAsItsStatusAndIsNotPriced(t *testing.T) {
	p := &planeStream{price: -1, ended: make(chan struct{}), handler: func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"insufficient balance","type":"insufficient_balance"}}`)
	}}
	ctx, bill := WithBill(context.Background())
	_, err := planeClient(t, p).ChatStream(ctx, &ChatRequest{Prompt: "hi"}, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "402") {
		t.Errorf("a refused stream answered %v, want the 402", err)
	}
	if _, told := bill.Nanos(); told {
		t.Error("a refusal was priced")
	}
}

// A stream whose socket closes before its trailer is an error, never a quiet end.
func TestAStreamCutBeforeItsTrailerIsAnError(t *testing.T) {
	near, far := net.Pipe()
	var wg sync.WaitGroup
	wg.Go(func() {
		sw := NewStreamWriter(far)
		sw.Header().Set("Content-Type", "text/event-stream")
		_, _ = sw.Write([]byte("data: x\n\n"))
		_ = far.Close()
	})
	r, _ := http.NewRequest(http.MethodPost, "http://ai.plane/v1/chat/completions", nil)
	resp, err := StreamResponse(near, r)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	wg.Wait()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a cut stream read %v, want ErrUnexpectedEOF", err)
	}
}
