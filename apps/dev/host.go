package dev

// host.go — what the wasm loop asks its world for: a model, and a cell for anything
// that touches a workspace. The loop knows neither (apps/dev/loop).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	dev "github.com/hanzoai/dev/go"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
)

// host serves one person's turns.
type host struct {
	ai    infer.Client
	org   string
	owner string     // the IAM subject
	who   zip.Caller // the person as the plane states them, payer included (actingAs)
	emit  func(string)
	model string
}

func (h *host) Emit(text string) {
	if h.emit != nil {
		h.emit(text)
	}
}

var tools = []infer.ToolDef{
	{Name: "read", Description: "Read a file in the workspace, by path relative to it.",
		Schema: schema(`"path":{"type":"string"}`, "path")},
	{Name: "write", Description: "Replace a file in the workspace with the text given.",
		Schema: schema(`"path":{"type":"string"},"text":{"type":"string"}`, "path", "text")},
	{Name: "patch", Description: "Apply a unified diff to the one file it edits.",
		Schema: schema(`"path":{"type":"string"},"diff":{"type":"string"}`, "path", "diff")},
	{Name: "exec", Description: "Run a command in the workspace and read its output.",
		Schema: schema(`"argv":{"type":"array","items":{"type":"string"}},"cwd":{"type":"string"}`, "argv")},
	{Name: "git", Description: "Run git in the workspace.",
		Schema: schema(`"argv":{"type":"array","items":{"type":"string"}}`, "argv")},
}

func schema(props string, required ...string) json.RawMessage {
	req := ""
	if len(required) > 0 {
		req = `,"required":["` + strings.Join(required, `","`) + `"]`
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `}` + req + `}`)
}

// Ask puts the conversation to the model through the gateway, as the person.
func (h *host) Ask(ctx context.Context, a dev.Ask) (dev.Reply, error) {
	return h.Stream(ctx, a, nil)
}

// Stream is Ask with the reply's text handed to delta as the model writes it, when
// the gateway streams (deps.AI does, metered as a whole answer is); otherwise the
// whole text once, when there is any. A nil delta asks for the whole answer.
func (h *host) Stream(ctx context.Context, a dev.Ask, delta func(string)) (dev.Reply, error) {
	if h.ai == nil {
		return dev.Reply{}, errors.New("no model gateway is mounted here")
	}
	var msgs []infer.ChatMessage
	if a.Prelude != nil && *a.Prelude != "" {
		msgs = append(msgs, infer.ChatMessage{Role: infer.RoleSystem, Content: *a.Prelude})
	}
	for _, m := range a.Messages {
		switch {
		case m.User != nil:
			msgs = append(msgs, infer.ChatMessage{Role: infer.RoleUser, Content: *m.User})
		case m.Agent != nil:
			msg := infer.ChatMessage{Role: infer.RoleAssistant}
			if m.Agent.Text != nil {
				msg.Content = *m.Agent.Text
			}
			for _, c := range m.Agent.Calls {
				name, args := callOf(c.Call)
				msg.ToolCalls = append(msg.ToolCalls, infer.ToolCall{ID: strconv.FormatUint(c.ID, 10), Name: name, Arguments: args})
			}
			msgs = append(msgs, msg)
		case m.Tool != nil:
			msgs = append(msgs, infer.ChatMessage{Role: infer.RoleTool, ToolCallID: strconv.FormatUint(m.Tool.ID, 10), Content: m.Tool.Text})
		}
	}
	model := h.model
	if a.Model != nil && *a.Model != "" {
		model = *a.Model
	}
	req := &infer.ChatRequest{
		Model: model, Org: h.org, Actor: h.org + "/" + h.owner, Feature: "dev.loop",
		Payer: cloud.PayerOf(actingAs(h.org, h.who)).Wallet, Messages: msgs, Tools: tools,
	}
	var resp *infer.ChatResponse
	var err error
	if sc, ok := h.ai.(infer.StreamCompleter); ok && delta != nil {
		resp, err = sc.ChatStream(ctx, req, func(d string) error { delta(d); return nil })
	} else {
		resp, err = h.ai.ChatCompletion(ctx, req)
		if err == nil && resp != nil && delta != nil && strings.TrimSpace(resp.Content) != "" {
			delta(resp.Content)
		}
	}
	if err != nil || resp == nil {
		if err == nil {
			err = errors.New("the model gave no answer")
		}
		return dev.Reply{}, err
	}
	var reply dev.Reply
	if strings.TrimSpace(resp.Content) != "" {
		t := resp.Content
		reply.Text = &t
	}
	for _, tc := range resp.ToolCalls {
		call, err := parseCall(tc.Name, tc.Arguments)
		if err != nil {
			t := fmt.Sprintf("(the model asked for %s with arguments that could not be read: %v)", tc.Name, err)
			reply.Text = &t
			continue
		}
		reply.Calls = append(reply.Calls, call)
	}
	return reply, nil
}

func callOf(c dev.Call) (string, string) {
	var name string
	var v any
	switch {
	case c.Read != nil:
		name, v = "read", c.Read
	case c.Write != nil:
		name, v = "write", c.Write
	case c.Patch != nil:
		name, v = "patch", c.Patch
	case c.Exec != nil:
		name, v = "exec", c.Exec
	case c.Git != nil:
		name, v = "git", c.Git
	case c.Browse != nil:
		name, v = "browse", c.Browse
	}
	b, _ := json.Marshal(v)
	return name, string(b)
}

func parseCall(name, args string) (dev.Call, error) {
	var c dev.Call
	var err error
	switch name {
	case "read":
		c.Read = new(dev.Read)
		err = json.Unmarshal([]byte(args), c.Read)
	case "write":
		c.Write = new(dev.Write)
		err = json.Unmarshal([]byte(args), c.Write)
	case "patch":
		c.Patch = new(dev.Patch)
		err = json.Unmarshal([]byte(args), c.Patch)
	case "exec":
		c.Exec = new(dev.Exec)
		err = json.Unmarshal([]byte(args), c.Exec)
	case "git":
		c.Git = new(dev.Git)
		err = json.Unmarshal([]byte(args), c.Git)
	default:
		err = fmt.Errorf("no tool named %q", name)
	}
	return c, err
}

// Effect does one thing in the person's cell, leasing it the first time any is needed.
// The cell is the same one a sandbox-runtime session of theirs uses, so a session that
// outgrows chat finds its workspace where the chat left it.
func (h *host) Effect(ctx context.Context, lease string, op dev.Op) (string, bool, string, error) {
	if op.Browse != nil {
		return "a browser is not available in this runtime; this session needs a sandbox for it", true, "", nil
	}
	cctx, cancel := context.WithCancel(actingAs(h.org, h.who))
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	// Every effect resumes the lease it holds, or takes a new one: a cell the sandbox
	// reaped, evicted or expired comes back under another id, and the session carries
	// on in it on the same disk instead of failing on a cell that is gone.
	got, err := sandboxpeer.SandboxLease(cctx, &client.LeaseIn{ID: lease, Class: class, Project: project(h.owner, "")})
	if err != nil {
		return "", false, "", err
	}
	held := ""
	if got.ID != lease {
		lease, held = got.ID, got.ID
	}
	run := func(in client.RunIn) (string, bool, string, error) {
		in.ID = lease
		out, err := sandboxpeer.SandboxRun(cctx, &in)
		if err != nil {
			return "", false, held, err
		}
		text := out.Stdout
		if out.Stderr != "" {
			text += out.Stderr
		}
		return text, out.ExitCode != 0, held, nil
	}
	switch {
	case op.Read != nil:
		b, err := sandboxpeer.SandboxRead(cctx, &client.PathIn{ID: lease, Path: op.Read.Path})
		if err != nil {
			return err.Error(), true, held, nil
		}
		if b.Dir {
			return strings.Join(b.Entries, "\n"), false, held, nil
		}
		return string(b.Data), false, held, nil
	case op.Write != nil:
		if _, err := sandboxpeer.SandboxWrite(cctx, &client.WriteIn{ID: lease, Path: op.Write.Path, Data: []byte(op.Write.Text)}); err != nil {
			return err.Error(), true, held, nil
		}
		return "wrote " + op.Write.Path, false, held, nil
	case op.Patch != nil:
		return run(client.RunIn{Argv: []string{"patch", "-u", op.Patch.Path}, Stdin: op.Patch.Diff})
	case op.Exec != nil:
		in := client.RunIn{Argv: op.Exec.Argv}
		if op.Exec.Cwd != nil {
			in.Dir = *op.Exec.Cwd
		}
		return run(in)
	case op.Git != nil:
		return run(client.RunIn{Argv: append([]string{"git"}, op.Git.Argv...)})
	}
	return "", false, held, errors.New("dev: the loop asked for nothing the host can do")
}

// Interrupt stops every command running in a cell, keeping the cell.
func (h *host) Interrupt(ctx context.Context, lease string) error {
	cctx, cancel := context.WithCancel(acting(h.org, h.owner))
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	_, err := sandboxpeer.SandboxStop(cctx, &client.StopIn{ID: lease})
	if gone(err) {
		return nil
	}
	return err
}
