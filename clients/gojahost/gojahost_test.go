package gojahost

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

const echoBundle = `
(function(){
  globalThis.handle = function(req){
    if (req.route === 'boom') throw new Error('kaboom');
    if (req.route === 'notfound') return { status: 404, body: { error: 'nope' } };
    return { status: 200, body: { route: req.route, tenant: req.tenant, params: req.params, data: globalThis.__X__ } };
  };
  globalThis.dbl = function(n){ return n * 2; };
})();
`

func newTestHost(t *testing.T) *Host {
	t.Helper()
	h, err := New(Config{
		Name:    "test",
		Bundle:  []byte(echoBundle),
		Globals: map[string]any{"__X__": map[string]any{"k": "v"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func TestDispatch_OK(t *testing.T) {
	h := newTestHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), Request{Route: "ping", Tenant: "acme", Params: map[string]string{"id": "7"}})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Status != 200 {
		t.Fatalf("status = %d, want 200", resp.Status)
	}
	var body struct {
		Route  string            `json:"route"`
		Tenant string            `json:"tenant"`
		Params map[string]string `json:"params"`
		Data   map[string]string `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal %q: %v", resp.Body, err)
	}
	if body.Route != "ping" || body.Tenant != "acme" || body.Params["id"] != "7" || body.Data["k"] != "v" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestDispatch_Status404(t *testing.T) {
	h := newTestHost(t)
	defer h.Close()
	resp, err := h.Dispatch(context.Background(), Request{Route: "notfound"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Status != 404 {
		t.Fatalf("status = %d, want 404", resp.Status)
	}
}

func TestDispatch_JSThrowIsError(t *testing.T) {
	h := newTestHost(t)
	defer h.Close()
	if _, err := h.Dispatch(context.Background(), Request{Route: "boom"}); err == nil {
		t.Fatal("expected error from thrown JS exception")
	}
}

func TestNew_RejectsBundleWithoutHandle(t *testing.T) {
	_, err := New(Config{Name: "x", Bundle: []byte(`globalThis.notHandle = 1;`)})
	if err == nil {
		t.Fatal("expected error when bundle has no globalThis.handle")
	}
}

func TestEval_Helper(t *testing.T) {
	h := newTestHost(t)
	defer h.Close()
	out, err := h.Eval(context.Background(), "dbl", []byte(`21`))
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if string(out) != "42" {
		t.Fatalf("Eval dbl(21) = %s, want 42", out)
	}
}

func TestSetGlobal_TakesEffect(t *testing.T) {
	h := newTestHost(t)
	defer h.Close()
	h.SetGlobal("__X__", map[string]any{"k": "updated"})
	resp, err := h.Dispatch(context.Background(), Request{Route: "ping"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var body struct {
		Data map[string]string `json:"data"`
	}
	_ = json.Unmarshal(resp.Body, &body)
	if body.Data["k"] != "updated" {
		t.Fatalf("global not updated: %+v", body.Data)
	}
}

func TestDispatch_ContextCancel(t *testing.T) {
	h, err := New(Config{
		Name:   "loop",
		Bundle: []byte(`globalThis.handle = function(){ var x=0; while(true){ x=(x+1)|0; Math.sin(x); } };`),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := h.Dispatch(ctx, Request{Route: "x"}); err == nil {
		t.Fatal("expected ctx error from infinite loop")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("interrupt too slow: %v", time.Since(start))
	}
}
