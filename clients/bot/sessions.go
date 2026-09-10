package bot

// The rest of the sessions family: how a conversation comes to exist, how a
// client is told about the ones that already do, and how a message reaches one.
//
// work.go owns the roster — the stored session, the group catalog, and the
// mutations that act on them. This file adds the calls around those and keeps
// no second copy of either: every row here is that same session, read with
// readSession and written with saveSession, and every catalog read is
// readGroups. Where a method's substance belongs to another family it is
// dispatched to that family instead of being written again, so a run has one
// owner and a transcript one keeper.
//
//	sessions.subscribe        the boot call: start hearing roster events, and
//	                          answer with the first page in the same trip
//	sessions.describe         one row, which is also the placement poll
//	sessions.resolve          the row a short link, an id or a label names
//	sessions.create           the only way a row comes to exist
//	sessions.send             resolve a session, then dispatch chat.send
//	sessions.reclaim          return a session to the gateway
//	sessions.groups.list      the catalog the sidebar's sections come from
//	sessions.groups.defaults  the same catalog with its paths on it
//
//	sessions.messages.subscribe   hear one session's messages
//	sessions.messages.unsubscribe stop hearing them
//
// Those last two narrow nothing here, and answer that they succeeded anyway,
// because they have. This surface delivers a session's events to every
// connection on the same bot, and the control UI declares none of the
// capabilities that would opt it into narrower delivery (ConnectParams.caps),
// so a connection that asks to hear one session already hears it. Answering
// true is the fact; refusing would say the messages will not arrive, which is
// the one thing that is not true — and the client holds a lease per session and
// shows the refusal on every chat pane it opens.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

func init() {
	Register("sessions.subscribe", Read, subscribeSessions)
	Register("sessions.messages.subscribe", Read, subscribeMessages)
	Register("sessions.messages.unsubscribe", Read, unsubscribeMessages)
	Register("sessions.describe", Read, describeSession)
	Register("sessions.resolve", Read, resolveSession)
	Register("sessions.create", Write, createSession)
	Register("sessions.send", Write, sendSession)
	Register("sessions.reclaim", Write, reclaimSession)
	Register("sessions.groups.list", Read, listGroups)
	Register("sessions.groups.defaults", Write, listGroupDefaults)
}

const (
	// sessionKeyMax bounds a session key. It is the ceiling chat.send accepts
	// (CHAT_SEND_SESSION_KEY_MAX_LENGTH), and a key that cannot be sent to is a
	// key nothing can use.
	sessionKeyMax = 512

	// createIn holds one document per create that named an idempotency key, so
	// a retry after a lost answer lands on the session the first attempt made
	// rather than beside it.
	createIn = "create"

	// sessionCandidates bounds how many near matches an ambiguous resolve
	// reports (SessionsResolveResultSchema: maxItems 10).
	sessionCandidates = 10
)

// ---- sessions.subscribe ----

// subscribeSessions is the client's first call after the handshake. It takes
// the same parameters as sessions.list and answers with that same page, so a
// client is subscribed and holding the roster after one round trip rather than
// two. A client that sends no parameters is asking only to be subscribed and
// gets the acknowledgement alone.
//
// Roster events reach every connection on the same bot (sessionMoved), so a
// connection is subscribed by being connected, and what this reports is
// whether there is a connection at all. On the single-frame door there is
// nowhere to deliver an event, and it says so rather than claiming otherwise.
// subscribeMessages answers the lease the chat pane takes on one session. It
// echoes the key it was given rather than a canonical one: the client adopts
// whatever comes back and holds its lease under that name, and since nothing
// here is narrowed by key, every name for a session works equally. Returning a
// different one would imply a mapping this surface does not apply.
func subscribeMessages(c *Call) (any, error) {
	var p struct {
		Key   string `json:"key"`
		Agent string `json:"agentId"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(p.Key)
	if key == "" {
		return nil, Invalid("a subscription names a session key")
	}
	return map[string]any{"subscribed": true, "key": key}, nil
}

// unsubscribeMessages releases that lease. Nothing was narrowed, so nothing
// widens; what it answers is that the client may stop tracking it.
func unsubscribeMessages(c *Call) (any, error) {
	var p struct {
		Key   string `json:"key"`
		Agent string `json:"agentId"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(p.Key)
	if key == "" {
		return nil, Invalid("an unsubscribe names a session key")
	}
	return map[string]any{"subscribed": false, "key": key}, nil
}

func subscribeSessions(c *Call) (any, error) {
	sent, err := sentFields(c.Params())
	if err != nil {
		return nil, err
	}
	open := c.conn != nil
	if !open || len(sent) == 0 {
		return map[string]any{"subscribed": open}, nil
	}
	page, err := listSessions(c)
	if err != nil {
		return nil, err
	}
	return map[string]any{"subscribed": true, "list": page}, nil
}

// ---- sessions.describe ----

type sessionDescribeParams struct {
	Key           string `json:"key"`
	AgentID       string `json:"agentId"`
	DerivedTitles bool   `json:"includeDerivedTitles"`
	LastMessage   bool   `json:"includeLastMessage"`
}

// describeSession reads one row. A key that is not there answers with a null
// session rather than a refusal: the caller asked what the gateway holds under
// that key, and "nothing" is an answer. The UI polls this while a session
// settles, and a poll that errored on the state it is waiting to leave would
// read as a failure.
//
// The two projection flags are accepted and produce nothing. A derived title
// and a last-message preview are read off a transcript, and a transcript is the
// chat family's to keep; a row that carries neither says less rather than says
// something untrue.
func describeSession(c *Call) (any, error) {
	var p sessionDescribeParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(p.Key)
	if key == "" {
		return nil, Invalid("key is required")
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var s session
	if err := st.Get(c.Context(), sessionsIn, key, &s); errors.Is(err, ErrNoDoc) {
		return map[string]any{"session": nil}, nil
	} else if err != nil {
		c.Log().Error("bot: read session", "org", c.Org(), "key", key, "err", err)
		return nil, Unavailable("the session could not be read")
	}
	if p.AgentID != "" && s.agent() != p.AgentID {
		return map[string]any{"session": nil}, nil
	}
	return map[string]any{"session": &s}, nil
}

// ---- sessions.resolve ----

// sessionResolveParams is SessionsResolveParamsSchema: several ways to name one
// session, of which a caller sends one.
type sessionResolveParams struct {
	Key       string `json:"key"`
	SessionID string `json:"sessionId"`
	Label     string `json:"label"`
	Reference *struct {
		Key  string `json:"key"`
		Slug string `json:"slug"`
	} `json:"reference"`
	ShortID        string `json:"shortId"`
	SlugHint       string `json:"slugHint"`
	AgentID        string `json:"agentId"`
	SpawnedBy      string `json:"spawnedBy"`
	IncludeGlobal  bool   `json:"includeGlobal"`
	IncludeUnknown bool   `json:"includeUnknown"`
	AllowMissing   bool   `json:"allowMissing"`
}

// sessionFound is SessionsResolveResultSchema: the one session a selector
// named, or the near matches when it named more than one. It is also what
// chat.startup reads a short link through.
type sessionFound struct {
	OK          bool               `json:"ok"`
	Key         string             `json:"key,omitempty"`
	AgentID     string             `json:"agentId,omitempty"`
	DisplayName string             `json:"displayName,omitempty"`
	BoardFace   string             `json:"boardFace,omitempty"`
	Candidates  []sessionCandidate `json:"candidates,omitempty"`
}

// sessionCandidate is one near match.
type sessionCandidate struct {
	Key         string `json:"key"`
	AgentID     string `json:"agentId"`
	DisplayName string `json:"displayName,omitempty"`
	BoardFace   string `json:"boardFace,omitempty"`
}

// resolveSession turns whichever way a caller named a session into the key
// every other method takes. A short link is a prefix of the id the row carries,
// which is what a shared URL abbreviates; a label and a display name resolve on
// their own text.
//
// A selector matching several sessions answers ok:false with the matches, so
// the caller picks rather than the gateway guessing. A selector matching none
// answers ok:false too when the caller allowed it, and is refused otherwise —
// the caller named something that is not there and can correct it.
func resolveSession(c *Call) (any, error) {
	var p sessionResolveParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if p.Reference != nil && strings.TrimSpace(p.Reference.Key) == "" {
		return nil, Invalid("a reference names a key")
	}
	pick := sessionPicker(&p)
	if pick == nil {
		return nil, Invalid("a resolve names a key, a session id, a label, a reference or a short id")
	}
	_, rows, err := scanSessions(c)
	if err != nil {
		c.Log().Error("bot: read sessions", "org", c.Org(), "err", err)
		return nil, Unavailable("the sessions could not be read")
	}
	hits := make([]sessionCandidate, 0, sessionCandidates)
	for i := range rows {
		s := &rows[i]
		if p.AgentID != "" && s.agent() != p.AgentID {
			continue
		}
		if p.SpawnedBy != "" && s.SpawnedBy != p.SpawnedBy {
			continue
		}
		if s.Kind == "global" && !p.IncludeGlobal {
			continue
		}
		if s.Kind == "unknown" && !p.IncludeUnknown {
			continue
		}
		if !pick(s) {
			continue
		}
		hits = append(hits, sessionCandidate{
			Key: s.Key, AgentID: s.agent(), DisplayName: s.Label, BoardFace: s.BoardFace,
		})
	}
	switch {
	case len(hits) == 1:
		one := hits[0]
		return sessionFound{
			OK: true, Key: one.Key, AgentID: one.AgentID,
			DisplayName: one.DisplayName, BoardFace: one.BoardFace,
		}, nil
	case len(hits) > 1:
		// A slug hint narrows an ambiguous short link before the caller is
		// asked to choose, which is what makes a shared URL land.
		if hint := strings.TrimSpace(p.SlugHint); hint != "" {
			narrowed := hits[:0:0]
			for _, hit := range hits {
				if sessionSlug(hit.DisplayName) == sessionSlug(hint) {
					narrowed = append(narrowed, hit)
				}
			}
			if len(narrowed) == 1 {
				one := narrowed[0]
				return sessionFound{
					OK: true, Key: one.Key, AgentID: one.AgentID,
					DisplayName: one.DisplayName, BoardFace: one.BoardFace,
				}, nil
			}
			if len(narrowed) > 1 {
				hits = narrowed
			}
		}
		return sessionFound{OK: false, Candidates: hits[:min(len(hits), sessionCandidates)]}, nil
	case p.AllowMissing:
		return sessionFound{OK: false}, nil
	}
	return nil, Invalid("no session matches")
}

// sessionPicker is the one selector a resolve carries, as a predicate over a
// row. The order is the protocol's: an exact key first, then an id, then a
// reference, then a label, then the short link.
func sessionPicker(p *sessionResolveParams) func(*session) bool {
	switch {
	case strings.TrimSpace(p.Key) != "":
		key := strings.TrimSpace(p.Key)
		return func(s *session) bool { return s.Key == key }
	case strings.TrimSpace(p.SessionID) != "":
		id := strings.TrimSpace(p.SessionID)
		return func(s *session) bool { return s.SessionID == id }
	case p.Reference != nil:
		key := strings.TrimSpace(p.Reference.Key)
		return func(s *session) bool { return s.Key == key }
	case strings.TrimSpace(p.Label) != "":
		label := strings.TrimSpace(p.Label)
		return func(s *session) bool { return strings.EqualFold(s.Label, label) }
	case strings.TrimSpace(p.ShortID) != "":
		short := strings.ToLower(strings.TrimSpace(p.ShortID))
		return func(s *session) bool {
			return strings.HasPrefix(strings.ToLower(s.SessionID), short) ||
				strings.HasPrefix(strings.ToLower(s.Key), short)
		}
	}
	return nil
}

// sessionSlug is a display name reduced to the form a URL carries: lower case,
// with each run of anything but a letter or a digit collapsed to one dash.
func sessionSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	return b.String()
}

// ---- sessions.create ----

// sessionCreateParams is SessionsCreateParamsSchema. The fields below the
// break are declared so the parameter object stays the one the protocol
// states, and are refused by name: each asks for something a session on this
// gateway does not have.
type sessionCreateParams struct {
	Key            string          `json:"key"`
	IdempotencyKey string          `json:"idempotencyKey"`
	AgentID        string          `json:"agentId"`
	Label          string          `json:"label"`
	DisplayName    string          `json:"displayName"`
	Category       string          `json:"category"`
	Model          string          `json:"model"`
	ContextWindow  string          `json:"contextWindow"`
	ThinkingLevel  string          `json:"thinkingLevel"`
	FastMode       json.RawMessage `json:"fastMode"`
	PermissionMode string          `json:"permissionMode"`
	Tools          *sessionTools   `json:"toolOverrides"`
	Incognito      bool            `json:"incognito"`
	Visibility     string          `json:"visibility"`
	Message        string          `json:"message"`
	Mentions       json.RawMessage `json:"mentions"`
	Attachments    json.RawMessage `json:"attachments"`

	// Lineage. The parent is provenance and is kept; the rest asks for a
	// subagent tree this gateway does not build.
	ParentKey    string `json:"parentSessionKey"`
	CommandHooks bool   `json:"emitCommandHooks"`
	Succeeds     bool   `json:"succeedsParent"`
	SpawnDepth   int    `json:"spawnDepth"`

	Fork       bool            `json:"fork"`
	ForkFrom   string          `json:"forkFrom"`
	Task       string          `json:"task"`
	CatalogID  string          `json:"catalogId"`
	ProjectID  string          `json:"projectId"`
	ProjectGit string          `json:"projectGitUrl"`
	Repository json.RawMessage `json:"repository"`
	Worktree   bool            `json:"worktree"`
	BaseRef    string          `json:"worktreeBaseRef"`
	TreeName   string          `json:"worktreeName"`
	ExecNode   string          `json:"execNode"`
	Cwd        string          `json:"cwd"`
}

// sessionElsewhere names the create parameters this gateway cannot honour.
// Each asks for something built by a subsystem that is not here — a forked
// transcript, a checked-out project, a git working copy, a host to run on, a
// foreign CLI's catalog — and a create that took one and made a plain session
// anyway would hand back a session that looks like the one asked for and is
// not.
//
// parentSessionKey, emitCommandHooks and succeedsParent are not among them.
// The New Chat control sends all three whenever a session is already open
// (ui/src/lib/sessions/create.ts), so refusing them refuses the primary way a
// conversation is started. The parent is recorded as the new session's
// provenance. The hooks govern the parent's session_start and session_end
// notifications, which are emitted by the command interpreter, and this gateway
// interprets no commands: the flag selects between two behaviours that are the
// same here, which is why it is accepted rather than refused. succeedsParent is
// checked below, because its two values are not the same here.
var sessionElsewhere = []string{
	"spawnDepth", "fork", "forkFrom", "task", "catalogId",
	"projectId", "projectGitUrl", "repository",
	"worktree", "worktreeBaseRef", "worktreeName", "execNode", "cwd",
}

// createSession makes a session, or adopts the one already under the key. It
// is the only way a row comes to exist, so it is where the shape of one is
// decided: the caller is its creator and its first owner, and every value it
// names is checked against the same closed sets a later patch is.
//
// What it costs follows the parameters, as the protocol's own predicate says
// (resolveSessionsCreateRequiredScope): an incognito session, a tool overlay
// or full permission is an act on the deployment and belongs to an admin of
// the org; everything else is a member's own work.
func createSession(c *Call) (any, error) {
	sent, err := sentFields(c.Params())
	if err != nil {
		return nil, err
	}
	var p sessionCreateParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	for _, name := range sessionElsewhere {
		if sent[name] {
			return nil, Invalid("this gateway starts plain sessions; %q asks for one it cannot start", name)
		}
	}
	if sent["succeedsParent"] && p.Succeeds {
		return nil, Invalid("this gateway starts a session beside its parent; succeedsParent asks it to end the parent, which nothing here does")
	}
	key := strings.TrimSpace(p.Key)
	if p.Incognito || sessionIncognito(key) || p.PermissionMode == "full" || sent["toolOverrides"] {
		if !c.Allows(Admin) {
			return nil, MissingScope(Admin)
		}
	}
	if len(key) > sessionKeyMax {
		return nil, Invalid("key exceeds %d bytes", sessionKeyMax)
	}
	// A label and a display name are one thing here. A label on this gateway
	// is the title shown and claims no uniqueness a display name would have to
	// stay clear of, so the two collapse; a caller that sent both keeps its
	// label.
	label := strings.TrimSpace(p.Label)
	if label == "" {
		label = strings.TrimSpace(p.DisplayName)
	}
	category := strings.TrimSpace(p.Category)
	if len(label) > sessionLabel || len(category) > sessionLabel {
		return nil, Invalid("a label or category exceeds %d bytes", sessionLabel)
	}
	if p.PermissionMode != "" && !slices.Contains(sessionModes, p.PermissionMode) {
		return nil, Invalid("permissionMode is one of %s", strings.Join(sessionModes, ", "))
	}
	if p.Visibility != "" && !slices.Contains(sessionVisibles, p.Visibility) {
		return nil, Invalid("visibility is one of %s", strings.Join(sessionVisibles, ", "))
	}
	fast, err := sessionFast(p.FastMode)
	if err != nil {
		return nil, err
	}

	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	// An idempotency key makes the create itself replayable. The UI's New Chat
	// names no session key, so without this a retry after a lost answer opens a
	// second empty conversation beside the first; the record maps the key to the
	// session the first attempt made. It is per person, because two people
	// retrying with the same key are two conversations.
	idem := strings.TrimSpace(p.IdempotencyKey)
	stamp := createAt(c.User(), idem)
	at := time.Now().UnixMilli()
	var s session
	adopted := false
	// Resolving the key, adopting a row that is already there and writing a new
	// one are one act. Outside it, two retries of one idempotent create both
	// find no record of the first, both mint a key, and the person ends up with
	// two conversations where they asked for one — which is the whole of what
	// the idempotency key is for.
	if err := st.Do(c.Context(), func(st *Store) error {
		if idem != "" && key == "" {
			var first string
			switch err := st.Get(c.Context(), createIn, stamp, &first); {
			case err == nil:
				key = first
			case !errors.Is(err, ErrNoDoc):
				c.Log().Error("bot: read create", "org", c.Org(), "err", err)
				return Unavailable("the session could not be read")
			}
		}
		if key == "" {
			key = mint("ses")
		}

		// A create naming a key already held adopts it rather than making a
		// second row. The message still runs: a caller that sent one asked for
		// a turn, and adopting the row it landed on is not a reason to drop it.
		switch err := st.Get(c.Context(), sessionsIn, key, &s); {
		case err == nil:
			adopted = true
			return nil
		case !errors.Is(err, ErrNoDoc):
			c.Log().Error("bot: read session", "org", c.Org(), "key", key, "err", err)
			return Unavailable("the session could not be read")
		}

		me := sessionActor{Type: "human", ID: c.User()}
		s = session{
			Key:            key,
			SessionID:      mint("txn"),
			Kind:           "direct",
			AgentID:        strings.TrimSpace(p.AgentID),
			Label:          label,
			Category:       category,
			Model:          strings.TrimSpace(p.Model),
			ContextWindow:  strings.TrimSpace(p.ContextWindow),
			ThinkingLevel:  strings.TrimSpace(p.ThinkingLevel),
			FastMode:       fast,
			PermissionMode: p.PermissionMode,
			Tools:          p.Tools,
			Visibility:     p.Visibility,
			SpawnedBy:      strings.TrimSpace(p.ParentKey),
			CreatedAt:      at,
			CreatedVia:     "operator",
			Creator:        &me,
			Owner:          &sessionOwner{Actor: me, AssignedAt: at},
		}
		if err := saveSession(c, st, &s, at); err != nil {
			c.Log().Error("bot: write session", "org", c.Org(), "key", key, "err", err)
			return Unavailable("the session could not be written")
		}
		if idem == "" {
			return nil
		}
		return st.Put(c.Context(), createIn, stamp, key)
	}); err != nil {
		return nil, err
	}
	if !adopted {
		sessionMoved(c, &s, "create")
	}

	if strings.TrimSpace(p.Message) == "" {
		return sessionMade(&s, nil, nil), nil
	}
	ack, err := sendMessage(c, st, &s, sessionMessage{
		Text:           p.Message,
		Mentions:       p.Mentions,
		Attachments:    p.Attachments,
		IdempotencyKey: idem,
	})
	// The session exists whether or not its first turn started. Reporting the
	// create as failed would send the client back to retry it, which lands on
	// the row this one already wrote; the run's own failure is reported beside
	// the session, which is the branch the client has for it
	// (ui/src/lib/sessions/create.ts: result.runError).
	if err != nil {
		return sessionMade(&s, nil, fault(err)), nil
	}
	return sessionMade(&s, ack, nil), nil
}

// createAt addresses one person's create-idempotency record.
func createAt(user, idem string) string {
	sum := sha256.Sum256([]byte(user + "\x00" + idem))
	return hex.EncodeToString(sum[:])
}

// sessionMade is SessionsCreateResultSchema. A started run carries the chat
// family's own acknowledgement — its run id and the position it wrote the
// message at — rather than a second reading of it.
func sessionMade(s *session, ack any, why *Fault) map[string]any {
	out := map[string]any{"ok": true, "key": s.Key, "sessionId": s.SessionID, "entry": s}
	if why != nil {
		out["runError"] = why
		return out
	}
	if ack == nil {
		return out
	}
	var started struct {
		RunID      string `json:"runId"`
		MessageSeq int    `json:"messageSeq"`
	}
	if b, err := json.Marshal(ack); err == nil {
		_ = json.Unmarshal(b, &started)
	}
	out["runStarted"] = true
	if started.RunID != "" {
		out["runId"] = started.RunID
	}
	if started.MessageSeq > 0 {
		out["messageSeq"] = started.MessageSeq
	}
	return out
}

// ---- sessions.send ----

// sessionMessage is the part of a send that is the message, shared by
// sessions.send and the first turn of a create.
type sessionMessage struct {
	Text           string          `json:"message"`
	Mentions       json.RawMessage `json:"mentions"`
	Thinking       string          `json:"thinking"`
	Attachments    json.RawMessage `json:"attachments"`
	TimeoutMs      *int            `json:"timeoutMs"`
	IdempotencyKey string          `json:"idempotencyKey"`
}

type sessionSendParams struct {
	Key     string `json:"key"`
	AgentID string `json:"agentId"`
	sessionMessage
}

// sendSession delivers one message into an existing session. Resolving and
// checking the session is this family's work; running the turn is the chat
// family's, so the message is handed to chat.send and its answer comes back
// unchanged — the acknowledgement the UI reads is the run's own, and restating
// it here would be a second reading of the same thing.
func sendSession(c *Call) (any, error) {
	var p sessionSendParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	st, s, err := readSession(c, strings.TrimSpace(p.Key))
	if err != nil {
		return nil, err
	}
	if p.AgentID != "" && s.agent() != p.AgentID {
		return nil, Invalid("session %q belongs to agent %q", s.Key, s.agent())
	}
	// An archived session is one the operator put away, and sending to it is
	// refused — unless the caller carries its own idempotency key, which makes
	// this a retry of a send already accepted. That retry must be allowed to
	// find its first result rather than be turned away by a state the first
	// attempt did not meet.
	if s.Archived && strings.TrimSpace(p.IdempotencyKey) == "" {
		return nil, Invalid("session %q is archived; restore it before sending", s.Key)
	}
	return sendMessage(c, st, s, p.sessionMessage)
}

// sendMessage hands one message to the chat family. The idempotency key is what
// makes a retry replay the first result instead of sending twice; chat.send
// requires one, so a caller that named none gets one minted here.
//
// Nothing is written back afterwards. chat.send records that the session was
// spoken to and which run it is carrying, and it does so on its own reading of
// the row; writing a copy taken before the relay would put the row back the way
// it was before the turn started, erasing the run state a moment after it was
// set. The whole document is replaced on write, so a stale copy is not a
// partial update but a full undo.
func sendMessage(c *Call, st *Store, s *session, m sessionMessage) (any, error) {
	if !known("chat.send") {
		return nil, Unavailable("no chat surface is mounted to run the message")
	}
	idem := strings.TrimSpace(m.IdempotencyKey)
	if idem == "" {
		idem = mint("send")
	}
	ask := map[string]any{
		"sessionKey":     s.Key,
		"agentId":        s.agent(),
		"message":        m.Text,
		"idempotencyKey": idem,
	}
	if s.SessionID != "" {
		ask["sessionId"] = s.SessionID
	}
	if len(m.Mentions) > 0 {
		ask["mentions"] = m.Mentions
	}
	if len(m.Attachments) > 0 {
		ask["attachments"] = m.Attachments
	}
	if m.Thinking != "" {
		ask["thinking"] = m.Thinking
	}
	if m.TimeoutMs != nil {
		ask["timeoutMs"] = *m.TimeoutMs
	}
	return relay(c, "chat.send", ask)
}

// ---- sessions.reclaim ----

type sessionReclaimParams struct {
	Key     string `json:"key"`
	AgentID string `json:"agentId"`
}

// reclaimSession returns a session to the gateway. A session this gateway runs
// is already here, so the answer is the local placement that says so.
//
// That is the method, not a stand-in for it. Reclaim is what the UI calls to
// undo a dispatch it cancelled mid-flight, and undoing a dispatch that never
// placed the session anywhere leaves it exactly where this answer says it is.
// Draining a cloud worker's workspace and tearing it down is the other half,
// and there is no worker environment here to drain — see the note below.
func reclaimSession(c *Call) (any, error) {
	var p sessionReclaimParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	_, s, err := readSession(c, strings.TrimSpace(p.Key))
	if err != nil {
		return nil, err
	}
	if p.AgentID != "" && s.agent() != p.AgentID {
		return nil, Invalid("session %q belongs to agent %q", s.Key, s.agent())
	}
	at := time.Now().UnixMilli()
	return map[string]any{
		"ok":        true,
		"key":       s.Key,
		"sessionId": s.SessionID,
		"placement": sessionPlace{
			State:     "local",
			CreatedAt: s.CreatedAt,
			UpdatedAt: at,
			ChangedAt: at,
		},
	}, nil
}

// sessionPlace is LocalSessionPlacementSchema: where a session runs, when the
// answer is here.
type sessionPlace struct {
	State      string `json:"state"`
	Generation int    `json:"generation"`
	CreatedAt  int64  `json:"createdAtMs"`
	UpdatedAt  int64  `json:"updatedAtMs"`
	ChangedAt  int64  `json:"stateChangedAtMs"`
}

// sessions.dispatch, sessions.move and sessions.catalog.archive are not
// registered, and the client is told so by their absence from the advertised
// method list: it disables a control it cannot call, which leaves an operator
// with a greyed button instead of one that fails when pressed.
//
// Dispatch and move hand a session's execution to a cloud worker or a paired
// device under a (generation, environmentId, ownerEpoch) ownership fence, with
// a workspace base manifest synced out and drained back. Answering either with
// an active placement would mean inventing an environment id and a bundle
// hash, and the client would then send the session's first message to the
// worker they name.
//
// sessions.catalog.archive deletes a thread inside another CLI's session store
// through a plugin-provided catalog. Nothing here enumerates such a store, and
// those methods only work as a set: archive alone reaches nothing to archive.

// ---- the group catalog ----

// listGroups is the catalog the sidebar's sections are built from. It carries
// names and order and no filesystem path, which is why anyone who may read the
// roster may read it; the paths are the separate surface below.
func listGroups(c *Call) (any, error) {
	_, b, err := readGroups(c)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"groups": b.Groups}
	if len(b.Sections) > 0 {
		out["sectionOrder"] = b.Sections
	}
	return out, nil
}

// listGroupDefaults is the same catalog with each group's New Session defaults
// on it. A working directory is a path on a machine, so this half costs write
// where the catalog itself does not.
func listGroupDefaults(c *Call) (any, error) {
	_, b, err := readGroups(c)
	if err != nil {
		return nil, err
	}
	// The result type declares an array. A group never given defaults has
	// none, and an empty list says that; a null would say the defaults were
	// not projected at all.
	out := b.Presets
	if out == nil {
		out = []sessionPreset{}
	}
	return map[string]any{"defaults": out}, nil
}
