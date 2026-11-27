// computer.go is the CLOUD half of driving a linked machine's GUI from a bound
// cloud agent. The address is the existing machine-action verb dispatch:
//
//	POST /v1/compute/machines/:id/computer
//	     {"action":"screenshot|click|type|…","args":{…},"approval":true}
//
// It is a THIN, org-scoped relay, not a second computer tool. The action
// vocabulary, the read/act split and the approval requirement are hanzod's
// (v0.5.2, POST /v1/computer and /v1/computer/{action}); this file reuses them so
// a caller cannot get a different answer by coming through Cloud. hanzod keeps
// the audit record; Cloud adds no payload of its own to any log.
//
// # The transport is NOT here yet
//
// There is no path from a Visor machine to its local hanzod in this repository:
// computeClient reaches Visor only (client.go), and the machine-agent commands
// that exist here are bindings and an agent RUN — neither dials the machine.
// The endpoint this handler calls,
//
//	POST /v1/machines/{owner}/{name}/computer  (on Visor)
//
// must be added in the Visor/compute service; the exact wire contract is in
// LLM.md under "Machine computer relay". Until it lands, a real Visor answers
// 404 and this route says so plainly (503) instead of inventing a tunnel. The
// fake upstream in the tests proves the cloud-side validation and byte/status
// pass-through that WILL apply once Visor serves that address.

package compute

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

const (
	// maxComputerBody bounds the request a caller can hand to a GUI action.
	// Args carry coordinates and text, never an image; a megabyte is ample.
	maxComputerBody = 1 << 20

	// computerApprovalHeader is the evidence hanzod accepts in place of a body
	// flag. Cloud accepts either spelling and forwards both to the machine.
	computerApprovalHeader = "X-Hanzo-Approve"
)

// computerReq is the wire body. Approval is a caller's ATTESTATION that an
// operator/agent policy already said yes — hanzod re-checks it and audits every
// op, so Cloud is not the approval authority and does not mint one.
type computerReq struct {
	// Action is a hanzod computer action, with or without the `computer.`
	// prefix the node plane uses ("computer.click" and "click" are one action).
	Action string `json:"action"`
	// Args is the action's argument object, forwarded verbatim. It may contain
	// typed text; it is never logged, marshalled into an error, or traced here.
	Args json.RawMessage `json:"args,omitempty"`
	// Approval is the caller's approval evidence for an act op.
	Approval bool `json:"approval"`
}

// computerReadActions is hanzod's read vocabulary: an op that observes the
// screen or the machine and changes nothing. These are open. Everything else —
// including an action this version of Cloud does not recognise — is an act and
// needs approval evidence. Failing closed is the point: a new hanzod act op
// must not become reachable the moment it ships and before this list is updated.
var computerReadActions = map[string]bool{
	"info":              true,
	"health":            true,
	"screen_size":       true,
	"get_screens":       true,
	"position":          true,
	"get_pixel":         true,
	"get_active_window": true,
	"list_windows":      true,
	"screenshot":        true,
	"screenshot_region": true,
	"mcp.tools":         true,
}

// computerActActions names the act vocabulary for readers of this file and for
// the classification test. The GATE asks computerReadActions, not this map, so
// an unknown action is an act by construction. These are the hanzod v0.5.2 act
// ops (the `computer.*` commands the node plane also declares) plus `key`, the
// short spelling of press/key_down/key_up/hotkey.
var computerActActions = []string{
	"click", "double_click", "right_click", "middle_click",
	"move", "move_relative", "drag", "drag_relative", "scroll",
	"type", "write", "key", "press", "key_down", "key_up", "hotkey",
	"focus", "focus_window", "minimize_window", "maximize_window",
	"resize_window", "move_window", "close_window", "open_app", "batch",
	// hanzo-mcp's control ops change how later ops run, so they are acts too.
	"sleep", "set_pause", "set_failsafe",
	"mac_calendar", "mac_reminders", "mac_notes", "mac_mail_draft",
	"mac_contacts", "mac_shortcuts", "mcp.call",
}

// computerActionName normalises both spellings to the bare action hanzod's
// /v1/computer/{action} expects. It never lowercases args, text or coordinates.
func computerActionName(raw string) string {
	a := strings.ToLower(strings.TrimSpace(raw))
	if rest, ok := strings.CutPrefix(a, "computer."); ok {
		return strings.TrimSpace(rest)
	}
	return a
}

// computerNeedsApproval reports whether an action may act on the machine. An
// unrecognised action is an act: a newly shipped hanzod op must not silently
// become an unapproved capability.
func computerNeedsApproval(action string) bool { return !computerReadActions[action] }

// approvedByHeader reports whether a caller's X-Hanzo-Approve header carries
// approval. hanzod treats the header's PRESENCE as evidence; a literal false
// spelling is not evidence and is refused here rather than waved through.
func approvedByHeader(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

// computerAction is entered from botAction's verb dispatch, with the org and
// machine id already resolved from the validated principal and the URL.
//
// It gates BEFORE any request reaches the machine: no principal (botScope), no
// action, no machine in the caller's org, no bound agent, and — for every act —
// no approval evidence all end here. Then it relays the upstream status,
// Content-Type and body byte-for-byte. Nothing in this function logs the body,
// args, screen pixels, typed text or credentials.
func computerAction(s *cloud.Service[state], c *zip.Ctx, org, id string) error {
	body := c.Body()
	if len(body) > maxComputerBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "computer: request body too large")
	}
	var in computerReq
	if err := json.Unmarshal(body, &in); err != nil {
		return zip.ErrBadRequest("invalid computer request")
	}
	action := computerActionName(in.Action)
	if action == "" {
		return zip.ErrBadRequest("computer action required")
	}
	args := in.Args
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}

	approved := in.Approval || approvedByHeader(c.Header(computerApprovalHeader))
	if computerNeedsApproval(action) && !approved {
		return &zip.HTTPError{Status: http.StatusForbidden, Code: "approval_required",
			Msg: "this computer action changes the machine; approval is required"}
	}

	// The machine is addressed under the caller's OWN org — the same (org, name)
	// key every machine route uses — so another tenant's id is a 404. A missing
	// machine is never turned into a relay attempt.
	var m visorMachine
	if err := s.State.cl.call(c, http.MethodGet, machine(org, id), "", nil, &m); err != nil {
		if notFound(err) {
			return zip.ErrNotFound("machine not found")
		}
		return err
	}
	if strings.TrimSpace(m.Name) == "" && strings.TrimSpace(m.Id) == "" {
		return zip.ErrNotFound("machine not found")
	}

	// A GUI is driven through the machine's BOUND agent, not through any machine
	// the org happens to own. The binding is Visor's own record, read through the
	// same typed op the machine-agent surface uses — no second store.
	var binding agentBinding
	if err := s.State.cl.op(c, http.MethodGet, machine(org, id)+"/agent", "", nil, &binding); err != nil {
		if notFound(err) {
			return zip.ErrConflict("machine has no bound agent to drive")
		}
		return err
	}
	if !binding.identifies() || strings.TrimSpace(binding.AgentName) == "" {
		return zip.ErrConflict("machine has no bound agent to drive")
	}

	payload := struct {
		Action   string          `json:"action"`
		Args     json.RawMessage `json:"args"`
		Approval bool            `json:"approval"`
	}{Action: action, Args: args, Approval: approved}
	extra := map[string]string{}
	if approved {
		extra[computerApprovalHeader] = "true"
	}

	up, err := s.State.cl.passthrough(c, http.MethodPost, machine(org, id)+"/computer", "", payload, extra)
	if err != nil {
		return err
	}
	// The relay is a Visor endpoint that does not exist yet (see LLM.md). A
	// 404/405 means exactly that — say so, and never pass an upstream HTML error
	// page through as if it were a computer answer.
	if up.status == http.StatusNotFound || up.status == http.StatusMethodNotAllowed {
		return zip.Errorf(http.StatusServiceUnavailable,
			"compute: Visor does not serve POST /v1/machines/{owner}/{name}/computer; the machine computer relay is not deployed")
	}
	if ct := up.contentType; ct != "" {
		c.SetHeader("Content-Type", ct)
	}
	return c.Bytes(up.status, up.body)
}
