package cloud

// THE CEILING, ON EVERY DOOR THE OPERATION HAS.
//
// defaultServiceRPM caps /v1/audio/* at 60 requests a minute for an org nobody
// configured, and middleware_ratelimit_default_test.go proves it binds. It
// proves it over REST, which is the door where the path a request carries IS
// the operation. It is not that door anywhere else: over MCP the path is /mcp
// and over the call plane it is /.well-known/zip/op/<name>, and
// canonicalService wants a /v1/<service>/... shape, so both resolve to the empty
// service and defaultServiceRPM[""] is zero.
//
// So the same operation, asked for the same way by the same org, is bounded
// through one door and unbounded through the two an agent uses. Measured here
// rather than argued.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type speakIn struct {
	N int `json:"n"`
}

type speakOut struct {
	OK bool `json:"ok"`
}

// ceilingRig is the UNCONFIGURED deployment — no commerce, no gateway store —
// which is where the static floor is the only thing that says stop.
type ceilingRig struct {
	base string
	door string // where this rig's MCP door answers.
	ran  func() int
}

// rest asks over the door whose path is the operation.
func (r *ceilingRig) rest(t *testing.T, org string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.base+"/v1/audio/transcriptions", strings.NewReader(`{"n":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "alice")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("REST: %v", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

// mcp asks over the door an agent uses. A tools/call answers 200 whether the op
// ran or was refused, so the refusal is read out of the frame, not the status.
func (r *ceilingRig) mcp(t *testing.T, org string) bool {
	t.Helper()
	return r.mcpOp(t, org, "audio_transcribe")
}

func (r *ceilingRig) mcpOp(t *testing.T, org, op string) bool {
	t.Helper()
	frame, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": op, "arguments": map[string]any{"n": 1}},
	})
	req, _ := http.NewRequest(http.MethodPost, r.base+r.door, strings.NewReader(string(frame)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "alice")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("MCP: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	var ans struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &ans); err != nil {
		t.Fatalf("MCP: reply is not a frame: %v (%s)", err, body)
	}
	if ans.Error != nil {
		t.Fatalf("MCP: protocol error, so the op never ran: %d %s", ans.Error.Code, ans.Error.Message)
	}
	return ans.Result.IsError
}
