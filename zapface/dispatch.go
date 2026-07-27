package zapface

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"

	"github.com/zap-proto/fiber/v3/middleware/adaptor"
	zaprpc "github.com/zap-proto/go/rpc"

	fiber "github.com/zap-proto/fiber/v3"
)

// dispatcher replays a decoded ZAP call as an in-process HTTP request against
// the cloud Fiber app's existing /v1 routes. It owns NO business logic; it is a
// pure transport bridge ZAP <-> the /v1 surface.
type dispatcher struct {
	// fiberHandler runs a synthesized *http.Request through the WHOLE Fiber app
	// (all routes + middleware + auth filters). It is adaptor.FiberApp(rawFiber).
	fiberHandler http.HandlerFunc
}

func newDispatcher(rawFiber *fiber.App) *dispatcher {
	return &dispatcher{fiberHandler: adaptor.FiberApp(rawFiber)}
}

// envelope is the uniform response shape every /v1 handler
// returns. We translate it into a ZapReply.
type envelope struct {
	Status string          `json:"status"` // "ok" | "error"
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
	Data2  json.RawMessage `json:"data2"`
}

// dispatch maps an rpc.Call to a ZapReply by replaying it as a /v1 HTTP request.
// cookieHeader is the raw Cookie header captured from the WS upgrade, replayed
// on every call so the /v1 session filter authenticates exactly as it does
// for the REST client (credentials: 'include').
func (d *dispatcher) dispatch(call zaprpc.Call, cookieHeader, authHeader, acceptLang string) zapReply {
	req, err := parseZapRequest(call.Payload)
	if err != nil {
		return errReply(http.StatusBadRequest, "INVALID_BODY", "malformed ZAP payload: "+err.Error())
	}
	if req.method == "" {
		return errReply(http.StatusBadRequest, "INVALID_BODY", "empty method")
	}

	httpReq, err := buildHTTPRequest(req)
	if err != nil {
		return errReply(http.StatusBadRequest, "INVALID_BODY", err.Error())
	}
	// Replay the browser's auth context so the same /v1 session/JWT filter runs.
	if cookieHeader != "" {
		httpReq.Header.Set("Cookie", cookieHeader)
	}
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}
	if acceptLang != "" {
		httpReq.Header.Set("Accept-Language", acceptLang)
	}

	rec := httptest.NewRecorder()
	d.fiberHandler(rec, httpReq)
	res := rec.Result()
	bodyBytes, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	// AuthGate parity: surface 401/403 as wire status the client maps to its
	// existing not-authorized handling.
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return errReply(uint32(res.StatusCode), "UNAUTHORIZED", "Not authorized")
	}

	var env envelope
	if err := json.Unmarshal(bodyBytes, &env); err != nil {
		return errReply(uint32(orStatus(res.StatusCode, http.StatusBadGateway)),
			"INVALID_RESPONSE", fmt.Sprintf("non-envelope response (HTTP %d)", res.StatusCode))
	}

	if env.Status != "ok" {
		// the /v1 handler reports the failure in `msg`; preserve it for the client.
		ej, _ := json.Marshal(map[string]string{"code": "ERROR", "msg": env.Msg, "message": env.Msg})
		return zapReply{ok: false, status: uint32(orStatus(res.StatusCode, http.StatusBadRequest)), errorJSON: string(ej)}
	}

	// Success. The REST `getList` path reads data2 (total) alongside data; the
	// ZAP `list` twin only consumes `data` (Provider[]), so result == data.
	// SuperJSON-wrap so console's SuperJSON.parse(reply.result) yields `data`.
	return zapReply{ok: true, status: http.StatusOK, result: superJSONWrap(env.Data)}
}

// buildHTTPRequest maps (method, SuperJSON input) onto a /v1 request.
//
// The method is an HTTP request line, "<VERB> <path>" (see splitMethod):
//   - a read verb (GET/HEAD/DELETE) -> no body; scalar input fields become the query.
//   - a write verb (POST/PUT/PATCH) -> scalar input fields lifted to the
//     query (identity hints like id/owner) and the single nested
//     object/array field as the JSON body (the resource); if there is no
//     nested field, the whole input is the body.
//
// This single rule covers every twin shape without per-endpoint coupling:
//
//	GET ai/providers/acme/openai   {}            -> GET    /v1/ai/providers/acme/openai
//	GET ai/providers               {owner,store} -> GET    /v1/ai/providers?owner=...&store=...
//	PATCH ai/providers/acme/openai {provider}    -> PATCH  … body=provider
//	POST ai/providers              <provider>    -> POST   /v1/ai/providers body=<provider>
//	DELETE ai/providers/acme/openai {}           -> DELETE /v1/ai/providers/acme/openai
func buildHTTPRequest(req zapRequest) (*http.Request, error) {
	verb, path, err := splitMethod(req.method)
	if err != nil {
		return nil, err
	}
	inputJSON := superJSONUnwrap(req.payload)

	scalars, nested := splitInput(inputJSON)
	q := url.Values{}
	for k, v := range scalars {
		q.Set(k, v)
	}

	target := path
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}

	if verb == http.MethodGet || verb == http.MethodHead || verb == http.MethodDelete {
		r := httptest.NewRequest(verb, target, nil)
		r.Header.Set("Accept", "application/json")
		return r, nil
	}

	// Mutation with a body: choose it.
	var body []byte
	switch {
	case nested != nil:
		body = nested // the single resource object/array
	case len(inputJSON) > 0:
		body = inputJSON // whole input is the resource
	default:
		body = []byte("{}")
	}
	r := httptest.NewRequest(verb, target, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.ContentLength = int64(len(body))
	return r, nil
}

// zapVerbs are the methods a ZAP call may name. Anything else is refused rather
// than coerced — a caller that cannot say what it wants to do does not get a guess.
var zapVerbs = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
	http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

// splitMethod reads a ZAP method as an HTTP request line: "<VERB> <path>", e.g.
// "GET rag/stores" or "PATCH ai/providers/acme/openai". The path is rooted at /v1.
//
// The verb is CARRIED, not inferred. It used to be guessed from the method name —
// a "get-" prefix meant GET and everything else meant POST — which worked only
// because the route surface encoded its verb in every route name (/v1/get-store
// vs /v1/update-store). Once the surface became RESTful that signal disappeared:
// one path answers GET, PATCH and DELETE, and no prefix distinguishes them. A
// heuristic there would silently send a delete as a POST.
//
// A method with no verb is an ERROR, not a default. Defaulting would turn a
// caller's omission into a wrong-but-plausible request — for a surface where the
// verb decides between reading a resource and destroying it, that is the one
// behaviour worth refusing outright.
func splitMethod(method string) (verb, path string, err error) {
	m := strings.TrimSpace(method)
	head, rest, ok := strings.Cut(m, " ")
	if !ok {
		return "", "", fmt.Errorf("method %q must be %q — the HTTP verb is required, not inferred", method, "<VERB> <path>")
	}
	verb = strings.ToUpper(strings.TrimSpace(head))
	if !zapVerbs[verb] {
		return "", "", fmt.Errorf("method %q names an unsupported verb %q", method, verb)
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", "", fmt.Errorf("method %q names no path", method)
	}
	return verb, "/v1/" + strings.TrimPrefix(rest, "/"), nil
}

// splitInput separates a JSON object into its scalar fields (rendered as query
// strings) and at most one nested object/array field (the resource body). A
// non-object input yields no scalars and the whole value as the nested body.
func splitInput(inputJSON []byte) (scalars map[string]string, nested []byte) {
	scalars = map[string]string{}
	if len(inputJSON) == 0 {
		return scalars, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(inputJSON, &obj); err != nil {
		// Not an object (array/primitive): the whole value is the body.
		return scalars, inputJSON
	}
	var nestedFields [][]byte
	for k, raw := range obj {
		if s, ok := scalarString(raw); ok {
			scalars[k] = s
			continue
		}
		nestedFields = append(nestedFields, raw)
	}
	// Exactly one nested object/array is the resource; ambiguous (>1) falls
	// back to sending the whole input as the body.
	if len(nestedFields) == 1 {
		return scalars, nestedFields[0]
	}
	if len(nestedFields) > 1 {
		return map[string]string{}, inputJSON
	}
	return scalars, nil
}

// scalarString renders a JSON scalar (string/number/bool) as a query value.
// Strings are unquoted; objects/arrays/null are not scalars.
func scalarString(raw json.RawMessage) (string, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 {
		return "", false
	}
	switch t[0] {
	case '"':
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return "", false
		}
		return s, true
	case '{', '[':
		return "", false
	}
	if string(t) == "null" {
		return "", false
	}
	if string(t) == "true" || string(t) == "false" {
		return string(t), true
	}
	// number
	if _, err := strconv.ParseFloat(string(t), 64); err == nil {
		return string(t), true
	}
	return "", false
}

func errReply(status uint32, code, msg string) zapReply {
	ej, _ := json.Marshal(map[string]string{"code": code, "msg": msg, "message": msg})
	return zapReply{ok: false, status: status, errorJSON: string(ej)}
}

func orStatus(got, fallback int) int {
	if got == 0 {
		return fallback
	}
	return got
}
