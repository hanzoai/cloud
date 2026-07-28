//go:build cgo

package flags

// The native evaluator — hanzo-flags (native/flags), a stateless Rust staticlib
// with PostHog-compatible semantics (rollout hash, property operators, variants,
// payloads), embedded over FFI. Evaluation is a pure in-memory function of
// (definitions JSON, context JSON): no KV, no network, no state — SQLite-backed
// definitions come from the Go side and the result returns in microseconds.
//
// Build: `make native` (or cargo build --release -p hanzo-flags) produces
// native/flags/target/release/libhanzo_flags.a; the cloud image builds it in a
// dedicated Rust stage. cloud already links C (SQLCipher), so cgo is a given.

/*
#cgo LDFLAGS: ${SRCDIR}/../../native/flags/target/release/libhanzo_flags.a
#cgo linux LDFLAGS: -lm -ldl -lpthread
#include <stdlib.h>

extern char* hanzo_flags_evaluate(const char* defs, const char* ctx);
extern void  hanzo_flags_free(char* s);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"
)

// engineAvailable reports whether the native evaluator is linked in.
const engineAvailable = true

// engineEvaluate runs the native evaluator over one definitions array and one
// evaluation context (both JSON) and returns the PostHog-shaped response.
func engineEvaluate(defsJSON, ctxJSON []byte) (json.RawMessage, error) {
	cDefs := C.CString(string(defsJSON))
	defer C.free(unsafe.Pointer(cDefs))
	cCtx := C.CString(string(ctxJSON))
	defer C.free(unsafe.Pointer(cCtx))

	out := C.hanzo_flags_evaluate(cDefs, cCtx)
	if out == nil {
		return nil, fmt.Errorf("flags: native evaluator returned nil")
	}
	defer C.hanzo_flags_free(out)
	res := []byte(C.GoString(out))

	// The engine reports failures as {"error": "..."} — surface them as errors.
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(res, &probe); err == nil && probe.Error != "" {
		return nil, fmt.Errorf("flags: %s", probe.Error)
	}
	return json.RawMessage(res), nil
}
