package agents

// a2a.go — the A2A (Agent2Agent, a2a-protocol.org 1.0) server: POST /v1/agent/a2a,
// JSON-RPC 2.0, answered as the built-in default assistant (manifest/a2a.Agent).
//
// # A task is an agent run
//
// SendMessage runs the assistant through runAgent, the one run path the HTTP run,
// the scheduler and the chat bridges share: the caller's balance is authorized
// before any inference, the flat run fee is debited only when the run succeeds,
// and the run lands in the run history and the session registry like every other.
// What A2A asks of a task that a run does not record — its state while it works,
// the conversation it belongs to, a cancel — lives in a2a_tasks (a2a_store.go).
//
// # Why the run is detached from the request
//
// A tool loop can take minutes and the edge in front of the API closes a request
// long before that. A run tied to the request would be abandoned with the
// connection, its work lost and the task left working forever. So every task runs
// on a context detached from the request, bounded by the tool loop's own budget,
// and the request only waits for it: a caller that asked to wait gets the finished
// task, one that set configuration.returnImmediately gets the working task at once,
// and either can read it later with GetTask or find it with ListTasks.
//
// # What it does not do
//
// No streaming and no push notifications (the card says so, and those methods
// answer UnsupportedOperation and PushNotificationNotSupported), no extended card,
// and no follow-up message on an existing task: a conversation continues by
// sending the next message with the task's contextId, which starts a new task
// with the conversation so far as its history.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/internal/aimodel"
	a2aproto "github.com/hanzoai/cloud/manifest/a2a"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/mcp"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
	"go.opentelemetry.io/otel/trace"
)

// The task states (A2A §4.1.3), as the wire spells them.
const (
	taskSubmitted     = "TASK_STATE_SUBMITTED"
	taskWorking       = "TASK_STATE_WORKING"
	taskCompleted     = "TASK_STATE_COMPLETED"
	taskFailed        = "TASK_STATE_FAILED"
	taskCanceled      = "TASK_STATE_CANCELED"
	taskRejected      = "TASK_STATE_REJECTED"
	taskInputRequired = "TASK_STATE_INPUT_REQUIRED"
	taskAuthRequired  = "TASK_STATE_AUTH_REQUIRED"
)

var taskStates = map[string]bool{
	taskSubmitted: true, taskWorking: true, taskCompleted: true, taskFailed: true,
	taskCanceled: true, taskRejected: true, taskInputRequired: true, taskAuthRequired: true,
}

// The JSON-RPC error codes A2A defines (§5.4, §9.5), and the standard ones.
const (
	rpcParse          = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternal       = -32603

	a2aTaskNotFound        = -32001
	a2aTaskNotCancelable   = -32002
	a2aPushNotSupported    = -32003
	a2aUnsupported         = -32004
	a2aContentType         = -32005
	a2aVersionNotSupported = -32009
)

// This server's own codes, outside the range A2A assigns. A2A names no code for a
// missing credential or a refused payment (§3.3.2 leaves it to the server); the
// HTTP status carries the first two, as every client's transport reads it.
const (
	a2aUnauthenticated = -32030 // HTTP 401, with the challenge naming where to sign in
	a2aForbidden       = -32031 // HTTP 403: a credential that names no org or no stable subject
	a2aPaymentRequired = -32040 // the balance gate refused: nothing ran and nothing was charged
	a2aUnavailable     = -32041 // the deployment cannot run the assistant now (no inference, no ledger)
)

// a2aHistoryTurns is how many earlier turns of a conversation a new message is sent
// with: enough for a conversation to hold together, few enough that an old thread
// does not spend the context a new question needs.
const a2aHistoryTurns = 20

// a2aRunning holds the cancel of each task running on this pod, so CancelTask can
// stop the run it names. A task running on another pod is still canceled — its row
// says so and keeps saying so — but its run finishes there.
var a2aRunning sync.Map // task id -> context.CancelFunc

// The wire shapes (A2A 1.0 §4). A Part is exactly one of text, raw, url or data.
type a2aPart struct {
	Text      *string         `json:"text,omitempty"`
	Raw       *string         `json:"raw,omitempty"`
	URL       *string         `json:"url,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
	Filename  string          `json:"filename,omitempty"`
}

type a2aMessage struct {
	MessageID string    `json:"messageId"`
	Role      string    `json:"role"`
	Parts     []a2aPart `json:"parts"`
	ContextID string    `json:"contextId,omitempty"`
	TaskID    string    `json:"taskId,omitempty"`
}

type a2aStatus struct {
	State     string      `json:"state"`
	Message   *a2aMessage `json:"message,omitempty"`
	Timestamp string      `json:"timestamp,omitempty"`
}

type a2aArtifact struct {
	ArtifactID string    `json:"artifactId"`
	Name       string    `json:"name,omitempty"`
	Parts      []a2aPart `json:"parts"`
}

type a2aTask struct {
	ID        string        `json:"id"`
	ContextID string        `json:"contextId,omitempty"`
	Status    a2aStatus     `json:"status"`
	Artifacts []a2aArtifact `json:"artifacts,omitempty"`
	History   []a2aMessage  `json:"history,omitempty"`
}

type a2aRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// a2aFailure is one JSON-RPC error, with the HTTP status it is written under.
type a2aFailure struct {
	status int
	code   int
	reason string // ErrorInfo.reason: the error's name, UPPER_SNAKE, without "Error"
	msg    string
	meta   map[string]string
}

func fail(code int, reason, msg string) *a2aFailure {
	return &a2aFailure{status: http.StatusOK, code: code, reason: reason, msg: msg}
}

// a2a answers one JSON-RPC request.
func a2a(s *cloud.Service[state], c *zip.Ctx) error {
	var req a2aRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return a2aWrite(c, nil, fail(rpcParse, "", "parse error: the body is not a JSON-RPC request"))
	}
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		return a2aWrite(c, req.ID, fail(rpcInvalidRequest, "", `a JSON-RPC 2.0 request names "jsonrpc":"2.0" and a method`))
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return a2aWrite(c, nil, fail(rpcInvalidRequest, "", "every A2A method is a request and carries an id"))
	}
	// A2A-Version is Major.Minor (§3.6). The v0.3 method names are slash paths
	// (message/send, tasks/get); this server speaks 1.0 only, and says so rather
	// than answering a 0.3 client in shapes it cannot read.
	if v := strings.TrimSpace(c.Header("A2A-Version")); v != "" && v != a2aproto.Protocol {
		return a2aWrite(c, req.ID, fail(a2aVersionNotSupported, "VERSION_NOT_SUPPORTED",
			"this server speaks A2A "+a2aproto.Protocol+" only; A2A-Version "+v+" is not supported"))
	}
	if strings.Contains(req.Method, "/") {
		return a2aWrite(c, req.ID, fail(a2aVersionNotSupported, "VERSION_NOT_SUPPORTED",
			req.Method+" is an A2A 0.3 method; this server speaks A2A "+a2aproto.Protocol+
				" (SendMessage, GetTask, ListTasks, CancelTask) and takes A2A-Version: "+a2aproto.Protocol))
	}

	org, actor, refused := a2aCaller(c)
	if refused != nil {
		return a2aWrite(c, req.ID, refused)
	}
	var (
		result any
		f      *a2aFailure
	)
	switch req.Method {
	case "SendMessage":
		result, f = a2aSend(s, c, req.Params, org, actor)
	case "GetTask":
		result, f = a2aGet(s, c, req.Params, org, actor)
	case "ListTasks":
		result, f = a2aList(s, c, req.Params, org, actor)
	case "CancelTask":
		result, f = a2aCancel(s, c, req.Params, org, actor)
	case "SendStreamingMessage", "SubscribeToTask":
		f = fail(a2aUnsupported, "UNSUPPORTED_OPERATION", "this agent does not stream: send SendMessage, and read the task with GetTask")
	case "GetExtendedAgentCard":
		f = fail(a2aUnsupported, "UNSUPPORTED_OPERATION", "this agent has no extended card; its card is public at "+a2aproto.Card)
	case "CreateTaskPushNotificationConfig", "GetTaskPushNotificationConfig",
		"ListTaskPushNotificationConfigs", "DeleteTaskPushNotificationConfig":
		f = fail(a2aPushNotSupported, "PUSH_NOTIFICATION_NOT_SUPPORTED", "this agent sends no push notifications")
	default:
		f = fail(rpcMethodNotFound, "", "method not found: "+req.Method)
	}
	if f != nil {
		return a2aWrite(c, req.ID, f)
	}
	return c.JSON(http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

// a2aCaller is the caller's org and actor, or the refusal that stands in for them:
// 401 with the challenge an OAuth client follows to sign in when there is no
// credential, 403 when the credential names no org or no stable subject.
func a2aCaller(c *zip.Ctx) (string, string, *a2aFailure) {
	org, ok := tenant(c)
	if !ok {
		if strings.TrimSpace(c.User()) == "" {
			origin := brand.Origin(string(c.Fiber().Request().Header.Host()))
			c.SetHeader("WWW-Authenticate", `Bearer resource_metadata="`+origin+mcp.Metadata+`"`)
			return "", "", &a2aFailure{status: http.StatusUnauthorized, code: a2aUnauthenticated, reason: "UNAUTHENTICATED",
				msg: "A2A needs a bearer credential: an access token from the authorization server the resource metadata at " +
					origin + mcp.Metadata + " names, or an API key"}
		}
		return "", "", &a2aFailure{status: http.StatusForbidden, code: a2aForbidden, reason: "FORBIDDEN", msg: principal.Refusal(c)}
	}
	sub, keyed := principal.SubjectFrom(principal.WithSubject(c.Forward(), c))
	if !keyed {
		return "", "", &a2aFailure{status: http.StatusForbidden, code: a2aForbidden, reason: "FORBIDDEN",
			msg: "a task is sent by a caller with a stable subject"}
	}
	return org, billingActor(org, sub), nil
}

// sendParams is SendMessageRequest (§3.1.1).
type sendParams struct {
	Message       *a2aMessage `json:"message"`
	Configuration *struct {
		ReturnImmediately   bool     `json:"returnImmediately"`
		HistoryLength       *int     `json:"historyLength"`
		AcceptedOutputModes []string `json:"acceptedOutputModes"`
	} `json:"configuration"`
}

func a2aSend(s *cloud.Service[state], c *zip.Ctx, raw json.RawMessage, org, actor string) (any, *a2aFailure) {
	var p sendParams
	if err := json.Unmarshal(raw, &p); err != nil || p.Message == nil {
		return nil, fail(rpcInvalidParams, "", "SendMessage takes {message: {messageId, role, parts}}")
	}
	m := p.Message
	if strings.TrimSpace(m.MessageID) == "" || m.Role != "ROLE_USER" || len(m.Parts) == 0 {
		return nil, fail(rpcInvalidParams, "", "a message the client sends has a messageId, role ROLE_USER and at least one part")
	}
	var history *int
	if p.Configuration != nil {
		history = p.Configuration.HistoryLength
		if modes := p.Configuration.AcceptedOutputModes; len(modes) > 0 && !acceptsText(modes) {
			return nil, fail(a2aContentType, "CONTENT_TYPE_NOT_SUPPORTED", "this agent answers in text/markdown or text/plain only")
		}
	}
	input, f := messageText(m.Parts)
	if f != nil {
		return nil, f
	}
	if len(input) > maxInput {
		return nil, fail(rpcInvalidParams, "", "the message is larger than this agent takes")
	}
	sto, err := s.State.storeFor(org)
	if err != nil {
		return nil, fail(rpcInternal, "", "store unavailable")
	}
	ctx := c.Context()
	// A message names a task only to continue it, and a task here is answered once:
	// the conversation goes on in its context, as a new task.
	if id := strings.TrimSpace(m.TaskID); id != "" {
		t, err := sto.Task(ctx, org, actor, id)
		if errors.Is(err, errNotFound) {
			return nil, fail(a2aTaskNotFound, "TASK_NOT_FOUND", "no task "+id)
		}
		if err != nil {
			return nil, fail(rpcInternal, "", "task lookup failed")
		}
		return nil, fail(a2aUnsupported, "UNSUPPORTED_OPERATION",
			"task "+id+" is "+t.State+" and takes no further message; send the next message with contextId "+t.ContextID)
	}
	if s.State.ai == nil {
		return nil, &a2aFailure{status: http.StatusOK, code: a2aUnavailable, reason: "UNAVAILABLE",
			msg: "inference is not configured on this deployment"}
	}
	a, err := sto.Resolve(ctx, org, a2aproto.Agent)
	if errors.Is(err, errNotFound) {
		// The same default the chat bridges reach (runOnBehalfModel): an org that
		// defined its own "hanzo" answers as its own; every other org answers as the
		// built-in assistant.
		def, ok := builtinAgent(org, a2aproto.Agent, aimodel.ChatModel)
		if !ok {
			return nil, &a2aFailure{status: http.StatusOK, code: a2aUnavailable, reason: "UNAVAILABLE", msg: "no model is configured for the assistant"}
		}
		a = def
	} else if err != nil {
		return nil, fail(rpcInternal, "", "agent lookup failed")
	}

	// The balance first, as the HTTP run does: an unfunded caller is refused here,
	// before a task exists, rather than handed a task that failed. runAgent asks
	// again before it spends anything.
	payer := meter.Payer(c)
	if err := s.State.bill.Authorize(ctx, payer, "", false, meterKind, cloud.ResourceFeeCents(agentFeeEnvPrefix, meterKind)); err != nil {
		return nil, a2aRefusal(err)
	}

	contextID := strings.TrimSpace(m.ContextID)
	var turns []infer.ChatMessage
	if contextID != "" {
		prior, err := sto.ContextTurns(ctx, org, actor, contextID, a2aHistoryTurns)
		if err != nil {
			return nil, fail(rpcInternal, "", "conversation lookup failed")
		}
		for _, t := range prior {
			turns = append(turns,
				infer.ChatMessage{Role: infer.RoleUser, Content: t.Input},
				infer.ChatMessage{Role: infer.RoleAssistant, Content: t.Output})
		}
	} else {
		contextID = mint.ID("ctx")
	}

	now := time.Now().UnixMilli()
	t := taskRow{ID: mint.ID("task"), Org: org, Actor: actor, ContextID: contextID, MessageID: m.MessageID,
		State: taskWorking, Input: input, CreatedAt: now, UpdatedAt: now}
	if err := sto.InsertTask(ctx, t); err != nil {
		return nil, fail(rpcInternal, "", "task could not be recorded")
	}

	// Detached from the request (see the file comment), bounded by the tool loop's
	// budget plus a minute for the bookkeeping after it. Like a turn (runTurn), the
	// run keeps the org, the trace and who pays — resolved now, while the request is
	// still live — and nothing else of the request: its *zip.Ctx is recycled the
	// moment the handler returns, and a run that read it afterwards would read
	// another request's headers, or none.
	rctx := cloud.For(context.Background(), org)
	rctx = trace.ContextWithSpanContext(rctx, trace.SpanContextFromContext(ctx))
	rctx = cloud.WithPayer(rctx, cloud.PayerOf(ctx))
	runCtx, cancel := context.WithTimeout(rctx, toolRunBudget+time.Minute)
	a2aRunning.Store(t.ID, cancel)
	done := make(chan struct{})
	reqID, ip := c.RequestID(), geo.IP(c)
	go func() {
		defer close(done)
		defer a2aRunning.Delete(t.ID)
		defer cancel()
		state, out, msg, runID := taskFailed, "", "", ""
		defer func() {
			if r := recover(); r != nil {
				s.Log.Error("a2a: run panic (recovered)", "org", org, "task", t.ID, "err", r)
				state, out, msg = taskFailed, "", "the run stopped unexpectedly"
			}
			if _, err := sto.FinishTask(context.Background(), org, t.ID, state, out, msg, runID, time.Now().UnixMilli()); err != nil {
				s.Log.Warn("a2a: record task outcome failed", "org", org, "task", t.ID, "err", err)
			}
		}()
		r, err := runAgent(s, runCtx, a, input, turns, payer, actor, reqID, ip)
		runID = r.ID
		switch {
		case err != nil:
			msg = a2aRefusal(err).msg
		case r.Status != "ok":
			msg = r.Error
		default:
			state, out = taskCompleted, r.Output
		}
	}()

	if p.Configuration == nil || !p.Configuration.ReturnImmediately {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	got, err := sto.Task(context.WithoutCancel(ctx), org, actor, t.ID)
	if err != nil {
		return nil, fail(rpcInternal, "", "task lookup failed")
	}
	return map[string]any{"task": got.view(history, true)}, nil
}

// getParams is GetTaskRequest (§3.1.3).
type getParams struct {
	ID            string `json:"id"`
	HistoryLength *int   `json:"historyLength"`
}

func a2aGet(s *cloud.Service[state], c *zip.Ctx, raw json.RawMessage, org, actor string) (any, *a2aFailure) {
	var p getParams
	if err := json.Unmarshal(raw, &p); err != nil || strings.TrimSpace(p.ID) == "" {
		return nil, fail(rpcInvalidParams, "", "GetTask takes {id}")
	}
	if p.HistoryLength != nil && *p.HistoryLength < 0 {
		return nil, fail(rpcInvalidParams, "", "historyLength is zero or more")
	}
	t, f := readTask(s, c, org, actor, p.ID)
	if f != nil {
		return nil, f
	}
	return t.view(p.HistoryLength, true), nil
}

// listParams is ListTasksRequest (§3.1.4).
type listParams struct {
	ContextID            string `json:"contextId"`
	Status               string `json:"status"`
	PageSize             *int   `json:"pageSize"`
	PageToken            string `json:"pageToken"`
	HistoryLength        *int   `json:"historyLength"`
	StatusTimestampAfter string `json:"statusTimestampAfter"`
	IncludeArtifacts     bool   `json:"includeArtifacts"`
}

func a2aList(s *cloud.Service[state], c *zip.Ctx, raw json.RawMessage, org, actor string) (any, *a2aFailure) {
	var p listParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fail(rpcInvalidParams, "", "ListTasks takes {contextId?, status?, pageSize?, pageToken?, historyLength?, statusTimestampAfter?, includeArtifacts?}")
		}
	}
	f := taskFilter{contextID: strings.TrimSpace(p.ContextID), size: 50, cursor: p.PageToken}
	if p.PageSize != nil {
		if *p.PageSize < 1 || *p.PageSize > 100 {
			return nil, fail(rpcInvalidParams, "", "pageSize is 1 to 100")
		}
		f.size = *p.PageSize
	}
	if p.Status != "" {
		if !taskStates[p.Status] {
			return nil, fail(rpcInvalidParams, "", "status is a TaskState, e.g. TASK_STATE_COMPLETED")
		}
		f.state = p.Status
	}
	if p.StatusTimestampAfter != "" {
		at, err := time.Parse(time.RFC3339Nano, p.StatusTimestampAfter)
		if err != nil {
			return nil, fail(rpcInvalidParams, "", "statusTimestampAfter is an ISO 8601 timestamp")
		}
		f.after = at.UnixMilli()
	}
	if p.HistoryLength != nil && *p.HistoryLength < 0 {
		return nil, fail(rpcInvalidParams, "", "historyLength is zero or more")
	}
	sto, err := s.State.storeFor(org)
	if err != nil {
		return nil, fail(rpcInternal, "", "store unavailable")
	}
	rows, total, next, err := sto.ListTasks(c.Context(), org, actor, f)
	if errors.Is(err, errBadCursor) {
		return nil, fail(rpcInvalidParams, "", err.Error())
	}
	if err != nil {
		return nil, fail(rpcInternal, "", "task listing failed")
	}
	tasks := make([]a2aTask, 0, len(rows))
	for _, t := range rows {
		tasks = append(tasks, t.view(p.HistoryLength, p.IncludeArtifacts))
	}
	return map[string]any{"tasks": tasks, "nextPageToken": next, "pageSize": f.size, "totalSize": total}, nil
}

// cancelParams is CancelTaskRequest (§3.1.5).
type cancelParams struct {
	ID string `json:"id"`
}

func a2aCancel(s *cloud.Service[state], c *zip.Ctx, raw json.RawMessage, org, actor string) (any, *a2aFailure) {
	var p cancelParams
	if err := json.Unmarshal(raw, &p); err != nil || strings.TrimSpace(p.ID) == "" {
		return nil, fail(rpcInvalidParams, "", "CancelTask takes {id}")
	}
	t, f := readTask(s, c, org, actor, p.ID)
	if f != nil {
		return nil, f
	}
	sto, err := s.State.storeFor(org)
	if err != nil {
		return nil, fail(rpcInternal, "", "store unavailable")
	}
	moved, err := sto.CancelTask(c.Context(), org, actor, t.ID, time.Now().UnixMilli())
	if err != nil {
		return nil, fail(rpcInternal, "", "cancel failed")
	}
	if !moved {
		return nil, fail(a2aTaskNotCancelable, "TASK_NOT_CANCELABLE", "task "+t.ID+" is "+t.State+" and can no longer be canceled")
	}
	if stop, ok := a2aRunning.Load(t.ID); ok {
		stop.(context.CancelFunc)()
	}
	t, f = readTask(s, c, org, actor, t.ID)
	if f != nil {
		return nil, f
	}
	return t.view(nil, true), nil
}

func readTask(s *cloud.Service[state], c *zip.Ctx, org, actor, id string) (taskRow, *a2aFailure) {
	sto, err := s.State.storeFor(org)
	if err != nil {
		return taskRow{}, fail(rpcInternal, "", "store unavailable")
	}
	t, err := sto.Task(c.Context(), org, actor, strings.TrimSpace(id))
	if errors.Is(err, errNotFound) {
		return taskRow{}, fail(a2aTaskNotFound, "TASK_NOT_FOUND", "no task "+strings.TrimSpace(id))
	}
	if err != nil {
		return taskRow{}, fail(rpcInternal, "", "task lookup failed")
	}
	return t, nil
}

// messageText is what the assistant is asked: the message's text parts, and its
// data parts as the JSON they are, in order. A file (raw or url) is not something
// this agent reads.
func messageText(parts []a2aPart) (string, *a2aFailure) {
	var b strings.Builder
	for _, p := range parts {
		var piece string
		switch {
		case p.Text != nil:
			if mt := p.MediaType; mt != "" && !strings.HasPrefix(mt, "text/") {
				return "", fail(a2aContentType, "CONTENT_TYPE_NOT_SUPPORTED", "a text part's mediaType is text/*, not "+mt)
			}
			piece = *p.Text
		case len(p.Data) > 0 && string(p.Data) != "null":
			piece = string(p.Data)
		case p.Raw != nil || p.URL != nil:
			return "", fail(a2aContentType, "CONTENT_TYPE_NOT_SUPPORTED", "this agent reads text and data parts; it does not take files")
		default:
			return "", fail(rpcInvalidParams, "", "a part is one of text, raw, url or data")
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(piece)
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", fail(rpcInvalidParams, "", "the message says nothing")
	}
	return b.String(), nil
}

// acceptsText reports whether a client's accepted output modes include what this
// agent answers in.
func acceptsText(modes []string) bool {
	for _, m := range modes {
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "text/markdown", "text/plain", "text/*", "*/*":
			return true
		}
	}
	return false
}

// a2aRefusal is a balance-gate denial as this server's JSON-RPC error: the fleet's
// own code (insufficient_balance, spend_cap_exceeded, balance_unavailable…) as the
// reason, its sentence as the message, and where to clear it as metadata.
func a2aRefusal(err error) *a2aFailure {
	var he *zip.HTTPError
	if !errors.As(cloud.Denied(err), &he) {
		return &a2aFailure{status: http.StatusOK, code: a2aUnavailable, reason: "UNAVAILABLE", msg: "billing is temporarily unavailable"}
	}
	code := a2aPaymentRequired
	if he.Status >= 500 {
		code = a2aUnavailable
	} else if he.Status == http.StatusForbidden {
		code = a2aForbidden
	}
	meta := map[string]string{}
	for k, v := range he.Detail {
		if k == "retry_after" {
			continue
		}
		if sv, ok := v.(string); ok {
			meta[k] = sv
		}
	}
	return &a2aFailure{status: http.StatusOK, code: code, reason: strings.ToUpper(he.Code), msg: he.Msg, meta: meta}
}

// a2aWrite writes a JSON-RPC error (§9.5): the A2A error's name as ErrorInfo, in
// the a2a-protocol.org domain for the errors A2A defines.
func a2aWrite(c *zip.Ctx, id json.RawMessage, f *a2aFailure) error {
	e := map[string]any{"code": f.code, "message": f.msg}
	if f.reason != "" {
		domain := "a2a-protocol.org"
		if f.code <= -32030 && f.code >= -32099 {
			domain = brand.For(brand.ForHost(string(c.Fiber().Request().Header.Host()))).Domain
		}
		info := map[string]any{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": f.reason, "domain": domain}
		if len(f.meta) > 0 {
			info["metadata"] = f.meta
		}
		e["data"] = []any{info}
	}
	var rid any
	if len(id) > 0 {
		rid = id
	}
	return c.JSON(f.status, map[string]any{"jsonrpc": "2.0", "id": rid, "error": e})
}

// view is the task as A2A states it. historyLength is the client's: absent is
// every message, zero is none, n is the newest n (§3.2.4).
func (t taskRow) view(historyLength *int, artifacts bool) a2aTask {
	out := a2aTask{ID: t.ID, ContextID: t.ContextID,
		Status: a2aStatus{State: t.State, Timestamp: time.UnixMilli(t.UpdatedAt).UTC().Format("2006-01-02T15:04:05.000Z")}}
	asked := a2aMessage{MessageID: t.MessageID, Role: "ROLE_USER", Parts: []a2aPart{{Text: &t.Input}}, ContextID: t.ContextID, TaskID: t.ID}
	history := []a2aMessage{asked}
	switch t.State {
	case taskCompleted:
		answer := a2aPart{Text: &t.Output, MediaType: "text/markdown"}
		history = append(history, a2aMessage{MessageID: t.ID + "-reply", Role: "ROLE_AGENT", Parts: []a2aPart{answer}, ContextID: t.ContextID, TaskID: t.ID})
		if artifacts {
			out.Artifacts = []a2aArtifact{{ArtifactID: t.ID + "-answer", Name: "answer", Parts: []a2aPart{answer}}}
		}
	case taskFailed, taskCanceled:
		why := t.Error
		if why == "" && t.State == taskCanceled {
			why = "canceled"
		}
		if why != "" {
			out.Status.Message = &a2aMessage{MessageID: t.ID + "-status", Role: "ROLE_AGENT",
				Parts: []a2aPart{{Text: &why}}, ContextID: t.ContextID, TaskID: t.ID}
		}
	}
	if historyLength != nil {
		n := *historyLength
		if n < len(history) {
			history = history[len(history)-n:]
		}
	}
	if len(history) > 0 {
		out.History = history
	}
	return out
}

// init states the route in the document. It is untyped for the run's reason: a
// JSON-RPC envelope is the body whatever the outcome, and an unauthenticated call
// answers 401 with a challenge, neither of which zip's error type can carry.
func init() {
	spec.Describe(a2aproto.Path, http.MethodPost,
		"Talk to the Hanzo assistant over A2A (Agent2Agent 1.0, JSON-RPC).",
		"The A2A server its agent card at /.well-known/agent-card.json names. Each SendMessage "+
			"is a task the default assistant answers with the tools your credential reaches: an agent "+
			"run like POST /v1/agent/:ref/run, so your balance is authorized before any inference and "+
			"the flat run fee is charged only when the run succeeds. A request waits for the answer "+
			"unless configuration.returnImmediately is set; either way the task is kept, and GetTask, "+
			"ListTasks and CancelTask read and stop your own tasks. A conversation continues by "+
			"sending the next message with the task's contextId.\n\n"+
			"Methods: SendMessage, GetTask, ListTasks, CancelTask. Streaming and push notifications "+
			"answer UnsupportedOperation (-32004) and PushNotificationNotSupported (-32003). The "+
			"A2A-Version header, when sent, is 1.0. With no credential the answer is 401 with a "+
			"WWW-Authenticate challenge naming the protected-resource metadata; every other outcome, "+
			"refusals included, is a JSON-RPC response.")
}
