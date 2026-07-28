package connectorruntime

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// shimJS is the in-process ActivePieces framework, embedded so the runtime is
// a single self-contained binary — no JS files on disk at run time.
//
//go:embed shim.js
var shimJS string

// defaultTimeout bounds a connector HTTP call when the connector sets none.
const defaultTimeout = 30 * time.Second

// maxResponseBytes caps a connector response so a hostile endpoint cannot
// exhaust memory (fail-secure, mirrors the KB ingest limit).
const maxResponseBytes = 32 << 20

// defaultDoerClient has NO timeout of its own; per-request deadlines come from
// the ctx the doer derives, so a slow endpoint is bounded by the caller's
// context or defaultTimeout, whichever is first.
var defaultDoerClient = &http.Client{}

// defaultHTTPDoer performs a connector HTTP request with Go net/http. It builds
// the final URL (merging queryParams), JSON-encodes an object body, sends, and
// decodes the response as JSON when it looks like JSON (matching axios' default
// responseType:'json' the connectors were written against).
func defaultHTTPDoer(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	timeout := defaultTimeout
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("bad url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return HTTPResponse{}, fmt.Errorf("unsupported url scheme %q", u.Scheme)
	}
	if len(req.QueryParams) > 0 {
		q := u.Query()
		for k, v := range req.QueryParams {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}

	var bodyReader io.Reader
	jsonBody := false
	if req.Body != nil {
		switch b := req.Body.(type) {
		case string:
			bodyReader = strings.NewReader(b)
		case []byte:
			bodyReader = bytes.NewReader(b)
		default:
			raw, e := json.Marshal(b)
			if e != nil {
				return HTTPResponse{}, fmt.Errorf("encode body: %w", e)
			}
			bodyReader = bytes.NewReader(raw)
			jsonBody = true
		}
	}

	r, err := http.NewRequestWithContext(cctx, method, u.String(), bodyReader)
	if err != nil {
		return HTTPResponse{}, err
	}
	for k, v := range req.Headers {
		r.Header.Set(k, v)
	}
	if jsonBody && r.Header.Get("Content-Type") == "" {
		r.Header.Set("Content-Type", "application/json")
	}

	resp, err := defaultDoerClient.Do(r)
	if err != nil {
		return HTTPResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("read response: %w", err)
	}

	return HTTPResponse{
		Status:  resp.StatusCode,
		Headers: firstValueHeaders(resp.Header),
		Body:    decodeBody(resp.Header.Get("Content-Type"), raw),
	}, nil
}

// decodeBody returns parsed JSON when the response is JSON, else the raw string
// — the same shape axios handed connectors.
func decodeBody(contentType string, raw []byte) any {
	looksJSON := strings.Contains(strings.ToLower(contentType), "json")
	if !looksJSON {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			looksJSON = true
		}
	}
	if looksJSON {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			return v
		}
	}
	return string(raw)
}

func firstValueHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[strings.ToLower(k)] = v[0]
		}
	}
	return out
}

// installConsole wires a no-op console so a connector that logs never throws
// ReferenceError (goja has no console by default).
func installConsole(vm *goja.Runtime) {
	if !goja.IsUndefined(vm.Get("console")) {
		return
	}
	noop := func(goja.FunctionCall) goja.Value { return goja.Undefined() }
	console := vm.NewObject()
	for _, m := range []string{"log", "info", "warn", "error", "debug", "trace"} {
		_ = console.Set(m, noop)
	}
	_ = vm.Set("console", console)
}

// toStringMap converts an exported JS object to string values (headers /
// queryParams are string-keyed in HTTP). Numeric values stringify without a
// trailing .0 so `count: 10` becomes "10", not "10.000000".
func toStringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		out[k] = scalarString(val)
	}
	return out
}

func scalarString(val any) string {
	switch x := val.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}

func toInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	default:
		return 0
	}
}

// jsError normalizes a goja call error into a Go error with the JS message.
func jsError(err error) error {
	if err == nil {
		return nil
	}
	var ex *goja.Exception
	if ok := asException(err, &ex); ok {
		return fmt.Errorf("connector: %s", ex.Value().String())
	}
	return err
}

func asException(err error, target **goja.Exception) bool {
	if ex, ok := err.(*goja.Exception); ok {
		*target = ex
		return true
	}
	return false
}

// promiseRejection turns a rejected promise value into a Go error. An Error
// object stringifies to "Error: msg"; other values marshal to JSON.
func promiseRejection(vm *goja.Runtime, v goja.Value) error {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return fmt.Errorf("connector: rejected")
	}
	if obj := v.ToObject(vm); obj != nil {
		if msg := obj.Get("message"); msg != nil && !goja.IsUndefined(msg) {
			return fmt.Errorf("connector: %s", msg.String())
		}
	}
	return fmt.Errorf("connector: %s", v.String())
}
