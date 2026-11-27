package cli

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// TestFnValidate — payload bounds: script required, flag-injection via
// requirements refused, timeout defaulted and capped.
func TestFnValidate(t *testing.T) {
	if _, err := fnValidate(json.RawMessage(`{}`)); err == nil {
		t.Error("empty script must be refused")
	}
	if _, err := fnValidate(json.RawMessage(`{"script":"print(1)","requirements":["--index-url=evil"]}`)); err == nil {
		t.Error("flag-shaped requirement must be refused")
	}
	in, err := fnValidate(json.RawMessage(`{"script":"print(1)"}`))
	if err != nil || in.TimeoutSeconds != 3600 {
		t.Errorf("default timeout: got %d, err %v", in.TimeoutSeconds, err)
	}
	in, _ = fnValidate(json.RawMessage(`{"script":"print(1)","timeoutSeconds":999999}`))
	if in.TimeoutSeconds != 21600 {
		t.Errorf("timeout cap: got %d, want 21600", in.TimeoutSeconds)
	}
}

// TestTailBuffer — a chatty training loop keeps only the LAST bytes, flagged.
func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{cap: 8}
	tb.Write([]byte("0123456789"))
	if string(tb.buf) != "23456789" || !tb.truncated {
		t.Errorf("tail = %q truncated=%v", tb.buf, tb.truncated)
	}
	tb2 := &tailBuffer{cap: 8}
	tb2.Write([]byte("abc"))
	if string(tb2.buf) != "abc" || tb2.truncated {
		t.Errorf("small write mangled: %q %v", tb2.buf, tb2.truncated)
	}
}

// TestHasNonRenderLane — echo and studio.render alone keep the render-only
// poison guard; a registered fn.run lane lifts it.
func TestHasNonRenderLane(t *testing.T) {
	w := &worker{handlers: map[string]jobHandler{"echo": echoHandler, studioCap: nil}}
	if w.hasNonRenderLane() {
		t.Error("render-only worker must report no extra lane")
	}
	w.handlers[fnCap] = fnRunHandler
	if !w.hasNonRenderLane() {
		t.Error("fn.run lane must lift the guard")
	}
}

// TestFnRunSmoke — the real handler end-to-end against the local uv: a script
// that prints and exits 0. Skipped where uv is absent.
func TestFnRunSmoke(t *testing.T) {
	if !uvPresent() {
		t.Skip("uv not installed")
	}
	res, err := fnRunHandler(t.Context(), json.RawMessage(`{"script":"print(2+2)","timeoutSeconds":120}`))
	if err != nil {
		t.Fatalf("fn.run: %v", err)
	}
	m := res.(map[string]any)
	if !strings.Contains(m["output"].(string), "4") {
		t.Errorf("output = %q, want it to contain 4", m["output"])
	}
}

func uvPresent() bool {
	_, err := exec.LookPath("uv")
	return err == nil
}
