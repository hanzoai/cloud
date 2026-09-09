package bot

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The session family: the roster the operator UI is built around, and the
// mutations that act on one row of it.
//
// A session here is a row, not a transcript. The row is what the sidebar, the
// Sessions page and the Activity page render — a key, a title, a category, a
// run status, the model and permission settings the composer shows — and it is
// the whole of what this file owns. Message history, model turns and worker
// placement are other families' work, and where a method's substance lives
// there, the method is left unregistered rather than answered with an
// invention: a control the UI hides is honest, a control that always fails is
// not.
//
// Rows and the group catalog are documents in the store the call resolves
// (Call.Store): the org's file, or a bot's when the call is bound to one. The
// org is the file, so it is not a column and no query can forget it.
//
// Scopes are the protocol's own, from src/gateway/methods/core-descriptors.ts.
// sessions.patch is declared "dynamic" there, so it registers at the lower of
// its two scopes and raises the bar itself on the parameters that need it.
//
// Every name this file adds to the package is qualified by the family it
// belongs to, because eleven families share this package and a bare `row` or
// `catalog` belongs to all of them and therefore to none.
func init() {
	Register("sessions.list", Read, listSessions)
	Register("sessions.patch", Write, patchSession)
	Register("sessions.reset", Admin, resetSession)
	Register("sessions.abort", Write, abortSession)
	Register("sessions.groups.put", Write, putGroups)
	Register("sessions.groups.rename", Write, renameGroup)
	Register("sessions.groups.update", Write, updateGroupDefaults)
	Register("sessions.groups.delete", Write, deleteGroup)
	Register("session.visibility.set", Write, setVisibility)
	Register("session.typing", Write, setTyping)
	Declare("sessions.changed", "session.sharing", "session.typing")
}

const (
	// Where this family's documents live: one per session, addressed by its
	// session key, and one catalog of groups, because every group mutation
	// rewrites the order of all of them.
	sessionsIn = "session"
	groupsIn   = "catalog"
	groupsAt   = "groups"

	// sessionStore names the store a result reports itself as coming from.
	// The store is addressed by org and collection, not by a path on a disk;
	// the field is required by the result type and the UI never reads it, so
	// it names the store rather than disclosing where it sits.
	sessionStore = "bot/session"

	// Bounds, transcribed from the parameter schemas.
	sessionLabel   = 512 // SessionLabelString
	sessionNote    = 120 // statusNote
	sessionPreview = 400 // a typing preview

	// sessionScan bounds one list. Filtering, sorting and paging all need the
	// whole matching set, so the read is bounded here rather than by the page.
	sessionScan = 10000
	// sessionPage is the server's own page size when a caller states none
	// (SESSIONS_LIST_DEFAULT_LIMIT).
	sessionPage = 100

	// sessionAgent is the agent a row belongs to when it names none
	// (src/routing/session-key.ts: LEGACY_IMPLICIT_AGENT_ID).
	sessionAgent = "main"
)

// The closed sets a mutation may write. A value outside one is a request the
// caller can correct, so it is refused rather than stored.
var (
	sessionFaces       = []string{"chat", "dashboard"}
	sessionModes       = []string{"read-only", "guarded", "workspace", "full"}
	sessionUsages      = []string{"off", "tokens", "full", "on"}
	sessionPolicies    = []string{"allow", "deny"}
	sessionActivations = []string{"mention", "always"}
	sessionAttentions  = []string{"hand", "key", "alert", "flag", "lock", "hourglass"}
	sessionVisibles    = []string{"shared", "read-only", "suggest", "draft"}
	// sessionRunning is the pair of run states an abort has something to stop.
	sessionRunning = []string{"queued", "running"}
)

// ---- the stored row -------------------------------------------------------

// sessionActor identifies whoever made a session or is answerable for it, in
// the shape SessionCreatedActorSchema declares.
type sessionActor struct {
	Type  string `json:"type"` // human | agent | system
	ID    string `json:"id,omitempty"`
	Label string `json:"label,omitempty"`
}

// sessionTools is one session's sparse tool overlay
// (SessionToolOverridesSchema).
type sessionTools struct {
	Servers  map[string]bool     `json:"mcpServers,omitempty"`
	ToolsOff map[string][]string `json:"mcpToolsDeny,omitempty"`
	Skills   map[string]bool     `json:"skills,omitempty"`
	Web      *bool               `json:"webSearch,omitempty"`
}

// sessionOwner is mutable responsibility for a session, separate from the
// provenance in Creator: an owner can be reassigned, a creator cannot.
type sessionOwner struct {
	Actor      sessionActor `json:"actor"`
	AssignedAt int64        `json:"assignedAt,omitempty"`
}

// session is one row as this surface stores and projects it. The field set is
// SessionRowSchema narrowed to what the methods here write and what the UI
// reads back; that schema is open, so a row that says less is a row with less
// to say rather than a malformed one.
type session struct {
	Key       string `json:"key"`
	SessionID string `json:"sessionId,omitempty"`
	Kind      string `json:"kind"` // direct | group | global | unknown
	AgentID   string `json:"agentId,omitempty"`

	Label     string `json:"label,omitempty"`
	Icon      string `json:"icon,omitempty"`
	Color     string `json:"color,omitempty"`
	Category  string `json:"category,omitempty"`
	BoardFace string `json:"boardFace,omitempty"`
	Note      string `json:"statusNote,omitempty"`
	NoteUntil int64  `json:"statusNoteExpiresAt,omitempty"`
	Attention string `json:"attention,omitempty"`

	Archived       bool   `json:"archived,omitempty"`
	ArchivedAt     int64  `json:"archivedAt,omitempty"`
	ArchiveReason  string `json:"archiveReason,omitempty"`
	Pinned         bool   `json:"pinned,omitempty"`
	PinnedAt       int64  `json:"pinnedAt,omitempty"`
	Unread         bool   `json:"unread,omitempty"`
	LastReadAt     int64  `json:"lastReadAt,omitempty"`
	MarkedUnreadAt int64  `json:"markedUnreadAt,omitempty"`

	CreatedAt      int64 `json:"createdAt,omitempty"`
	UpdatedAt      int64 `json:"updatedAt,omitempty"`
	LastActivityAt int64 `json:"lastActivityAt,omitempty"`
	LastSpokenAt   int64 `json:"lastInteractionAt,omitempty"`

	Status       string `json:"status,omitempty"` // queued|running|done|failed|killed|timeout
	LastRunID    string `json:"lastRunId,omitempty"`
	LastRunError string `json:"lastRunError,omitempty"`

	PermissionMode string          `json:"permissionMode,omitempty"`
	Model          string          `json:"model,omitempty"`
	ModelProvider  string          `json:"modelProvider,omitempty"`
	ContextWindow  string          `json:"contextWindow,omitempty"`
	ThinkingLevel  string          `json:"thinkingLevel,omitempty"`
	FastMode       json.RawMessage `json:"fastMode,omitempty"` // bool | "auto"
	VerboseLevel   string          `json:"verboseLevel,omitempty"`
	TraceLevel     string          `json:"traceLevel,omitempty"`
	Reasoning      string          `json:"reasoningLevel,omitempty"`
	Elevated       string          `json:"elevatedLevel,omitempty"`
	ResponseUsage  string          `json:"responseUsage,omitempty"`
	Tools          *sessionTools   `json:"toolOverrides,omitempty"`
	SendPolicy     string          `json:"sendPolicy,omitempty"`
	Activation     string          `json:"groupActivation,omitempty"`
	ExecHost       string          `json:"execHost,omitempty"`
	ExecNode       string          `json:"execNode,omitempty"`
	Completion     string          `json:"completionOwnerSessionKey,omitempty"`
	PolicyVersion  int             `json:"inheritedToolPolicyVersion,omitempty"`
	ToolAllow      []string        `json:"inheritedToolAllow,omitempty"`
	ToolDeny       []string        `json:"inheritedToolDeny,omitempty"`

	Visibility  string        `json:"visibility,omitempty"`
	SharingRole string        `json:"sharingRole,omitempty"`
	Owner       *sessionOwner `json:"owner,omitempty"`
	Creator     *sessionActor `json:"createdActor,omitempty"`
	CreatedVia  string        `json:"createdVia,omitempty"`
	SpawnedBy   string        `json:"spawnedBy,omitempty"`
	HasBoard    bool          `json:"hasBoard,omitempty"`
}

// agent names the agent a row belongs to, defaulting the way the protocol's
// own routing does rather than leaving a required event field empty.
func (s *session) agent() string {
	if s.AgentID != "" {
		return s.AgentID
	}
	return sessionAgent
}

// moved stamps the row with a new generation: when it last changed, and never
// a value it has already had. The generation is what sessionRevision names, so
// a caller that read one row and states it is refused when another write landed
// first — including one that landed inside the same millisecond, which a plain
// clock reading cannot tell apart from no write at all.
func (s *session) moved(at int64) { s.UpdatedAt = max(at, s.UpdatedAt+1) }

// stamp is the timestamp a list sorts on.
func (s *session) stamp(by string) int64 {
	if by == "lastInteractionAt" {
		return s.LastSpokenAt
	}
	return s.UpdatedAt
}

// ---- reading and writing a row --------------------------------------------

// getSession loads one row out of the store it is given, or reports that the
// caller named a session that is not there. The protocol has no not-found
// code: a key that does not resolve is a request the caller can correct.
//
// It takes the store rather than opening one because a mutation reads its row
// inside the act that writes it (Store.Do), and the act holds the file.
func getSession(c *Call, st *Store, key string) (*session, error) {
	var s session
	if err := st.Get(c.Context(), sessionsIn, key, &s); errors.Is(err, ErrNoDoc) {
		return nil, Invalid("no session %q", key)
	} else if err != nil {
		return nil, err
	}
	return &s, nil
}

// readSession opens the call's store and loads one row from it. It is the read
// for a method that only reads; a method that writes what it read uses
// changeSession, so the read and the write are one act.
func readSession(c *Call, key string) (*Store, *session, error) {
	st, err := c.Store()
	if err != nil {
		return nil, nil, err
	}
	s, err := getSession(c, st, key)
	if err != nil {
		return nil, nil, err
	}
	return st, s, nil
}

// changeSession reads one row, hands it to fn as of now, and writes it back —
// all inside one act. Two mutations of a row therefore cannot each write over
// what the other had read: because a write replaces the whole document, the
// loser's change would be undone rather than merged, and the caller would have
// been told it landed. fn may refuse, and then nothing is written.
//
// fn works on the row it is given and reaches nothing else: the act holds the
// file's one connection, so a second read of the same file inside fn would
// wait for the act that is already holding it.
func changeSession(c *Call, key string, fn func(s *session, now int64) error) (*session, error) {
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var out *session
	if err := st.Do(c.Context(), func(st *Store) error {
		s, err := getSession(c, st, key)
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		if err := fn(s, now); err != nil {
			return err
		}
		out = s
		return saveSession(c, st, s, now)
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// listSessionRows loads the rows this call can see out of the store it is
// given, bounded.
func listSessionRows(c *Call, st *Store) ([]session, error) {
	docs, err := st.List(c.Context(), sessionsIn, sessionScan, 0)
	if err != nil {
		return nil, err
	}
	out := make([]session, 0, len(docs))
	for _, d := range docs {
		var s session
		if err := json.Unmarshal(d.Doc, &s); err != nil {
			c.Log().Error("bot: unreadable session row", "id", d.ID, "err", err)
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// scanSessions opens the call's store and loads the rows this call can see.
func scanSessions(c *Call) (*Store, []session, error) {
	st, err := c.Store()
	if err != nil {
		return nil, nil, err
	}
	out, err := listSessionRows(c, st)
	if err != nil {
		return nil, nil, err
	}
	return st, out, nil
}

// saveSession writes a row back, moved to a new generation.
func saveSession(c *Call, st *Store, s *session, at int64) error {
	s.moved(at)
	return st.Put(c.Context(), sessionsIn, s.Key, s)
}

// sessionMoved publishes a roster change to the org. The payload names what
// moved and why; the client debounces a burst of them into one refresh, so a
// mutation announces itself and does not try to describe the new state.
func sessionMoved(c *Call, s *session, reason string) {
	Publish(c.Org(), c.Bot(), "", "sessions.changed", sessionChanged{
		SessionKey: s.Key,
		SessionID:  s.SessionID,
		AgentID:    s.agent(),
		Reason:     reason,
		TS:         time.Now().UnixMilli(),
	})
}

type sessionChanged struct {
	SessionKey string `json:"sessionKey,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
	AgentID    string `json:"agentId,omitempty"`
	Reason     string `json:"reason"`
	TS         int64  `json:"ts"`
}

// ---- sessions.list --------------------------------------------------------

// sessionListParams is SessionsListParamsSchema. Every field is optional and
// the schema is closed, so an unknown one is a client that has misunderstood
// the method.
type sessionListParams struct {
	Limit                  int    `json:"limit"`
	Offset                 int    `json:"offset"`
	ActiveMinutes          int    `json:"activeMinutes"`
	ActiveOnly             bool   `json:"activeOnly"`
	RequireLastInteraction bool   `json:"requireLastInteraction"`
	SortBy                 string `json:"sortBy"`
	IncludeGlobal          bool   `json:"includeGlobal"`
	IncludeUnknown         bool   `json:"includeUnknown"`
	ConfiguredAgentsOnly   bool   `json:"configuredAgentsOnly"`
	IncludeDerivedTitles   bool   `json:"includeDerivedTitles"`
	IncludeLastMessage     bool   `json:"includeLastMessage"`
	Label                  string `json:"label"`
	BoardFace              string `json:"boardFace"`
	HasBoard               *bool  `json:"hasBoard"`
	CreatorID              string `json:"creatorId"`
	OwnerID                string `json:"ownerId"`
	OwnerFirst             bool   `json:"ownerFirst"`
	InvolvingMe            bool   `json:"involvingMe"`
	InvolvingProfileID     string `json:"involvingProfileId"`
	IncludePeople          bool   `json:"includePeople"`
	SpawnedBy              string `json:"spawnedBy"`
	AgentID                string `json:"agentId"`
	Search                 string `json:"search"`
	Archived               any    `json:"archived"` // bool | "all"
}

// sessionListResult is SessionsListResultBase.
type sessionListResult struct {
	TS           int64           `json:"ts"`
	Path         string          `json:"path"`
	Count        int             `json:"count"`
	TotalCount   int             `json:"totalCount"`
	LimitApplied int             `json:"limitApplied"`
	Offset       int             `json:"offset,omitempty"`
	NextOffset   *int            `json:"nextOffset"`
	HasMore      bool            `json:"hasMore"`
	Defaults     sessionDefaults `json:"defaults"`
	Sessions     []session       `json:"sessions"`
}

// sessionDefaults is GatewaySessionsDefaults. It is required on the result and
// feeds the composer's model, context-window and thinking controls, so it is
// always sent. The model catalog belongs to the models family; until one is
// reachable there is no default to report, and a null says "none" where an
// omission would say "not projected".
type sessionDefaults struct {
	ModelProvider *string `json:"modelProvider"`
	Model         *string `json:"model"`
	ContextTokens *int    `json:"contextTokens"`
}

func listSessions(c *Call) (any, error) {
	var p sessionListParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	archived, both, err := sessionArchived(p.Archived)
	if err != nil {
		return nil, err
	}
	_, found, err := scanSessions(c)
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	keep := found[:0]
	for _, s := range found {
		if sessionMatches(&s, &p, archived, both, now, c.User()) {
			keep = append(keep, s)
		}
	}
	sessionOrder(keep, p.SortBy, p.OwnerFirst, c.User())

	limit := p.Limit
	if limit <= 0 {
		limit = sessionPage
	}
	offset := max(p.Offset, 0)
	page := []session{}
	if offset < len(keep) {
		page = slices.Clone(keep[offset:min(offset+limit, len(keep))])
	}
	for i := range page {
		page[i].SharingRole = sessionRole(c, &page[i])
	}
	next := offset + len(page)
	out := sessionListResult{
		TS:           now,
		Path:         sessionStore,
		Count:        len(page),
		TotalCount:   len(keep),
		LimitApplied: limit,
		Offset:       offset,
		HasMore:      next < len(keep),
		Sessions:     page,
	}
	if out.HasMore {
		out.NextOffset = &next
	}
	return out, nil
}

// sessionArchived reads the tri-state `archived` parameter: absent or false
// lists active rows, true lists archived ones, "all" lists both.
func sessionArchived(v any) (want, both bool, err error) {
	switch x := v.(type) {
	case nil:
		return false, false, nil
	case bool:
		return x, false, nil
	case string:
		if x == "all" {
			return false, true, nil
		}
	}
	return false, false, Invalid(`archived must be a boolean or "all"`)
}

// sessionMatches applies the list filters to one row. A filter with no state
// behind it here — a configured agent set, a transcript, a profile directory —
// narrows nothing rather than pretending to.
func sessionMatches(s *session, p *sessionListParams, archived, both bool, now int64, me string) bool {
	if !both && s.Archived != archived {
		return false
	}
	if !p.IncludeGlobal && s.Kind == "global" {
		return false
	}
	if !p.IncludeUnknown && s.Kind == "unknown" {
		return false
	}
	if p.AgentID != "" && s.agent() != p.AgentID {
		return false
	}
	if p.Label != "" && s.Label != p.Label {
		return false
	}
	if p.BoardFace != "" && s.BoardFace != p.BoardFace {
		return false
	}
	if p.HasBoard != nil && s.HasBoard != *p.HasBoard {
		return false
	}
	if p.SpawnedBy != "" && s.SpawnedBy != p.SpawnedBy {
		return false
	}
	if p.CreatorID != "" && (s.Creator == nil || s.Creator.ID != p.CreatorID) {
		return false
	}
	if p.OwnerID != "" && sessionOwnerID(s) != p.OwnerID {
		return false
	}
	if p.InvolvingProfileID != "" && !sessionInvolves(s, p.InvolvingProfileID) {
		return false
	}
	if p.InvolvingMe && !sessionInvolves(s, me) {
		return false
	}
	if p.ActiveOnly && !slices.Contains(sessionRunning, s.Status) {
		return false
	}
	if p.RequireLastInteraction && s.LastSpokenAt == 0 {
		return false
	}
	if p.ActiveMinutes > 0 && max(s.LastActivityAt, s.UpdatedAt) < now-int64(p.ActiveMinutes)*60_000 {
		return false
	}
	return p.Search == "" || sessionFinds(s, p.Search)
}

func sessionOwnerID(s *session) string {
	if s.Owner == nil {
		return ""
	}
	return s.Owner.Actor.ID
}

// sessionInvolves reports whether an identity is answerable for a session or
// made it.
func sessionInvolves(s *session, id string) bool {
	if id == "" {
		return false
	}
	return sessionOwnerID(s) == id || (s.Creator != nil && s.Creator.ID == id)
}

// sessionFinds is the roster search: the words an operator can see on a row.
func sessionFinds(s *session, q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	for _, field := range []string{s.Key, s.Label, s.Category, s.Note} {
		if strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

// sessionOrder is the protocol's own ordering
// (src/gateway/session-list-order.ts): pinned rows lead, most recent next, and
// the session key breaks the tie so that paging by offset returns each row
// exactly once.
func sessionOrder(rs []session, by string, ownerFirst bool, me string) {
	slices.SortStableFunc(rs, func(a, b session) int {
		if ownerFirst {
			mine, theirs := sessionOwnerID(&a) == me, sessionOwnerID(&b) == me
			if mine != theirs {
				if mine {
					return -1
				}
				return 1
			}
		}
		if by != "lastInteractionAt" && a.PinnedAt != b.PinnedAt {
			return int(sign(b.PinnedAt - a.PinnedAt))
		}
		if x, y := a.stamp(by), b.stamp(by); x != y {
			return int(sign(y - x))
		}
		return strings.Compare(a.Key, b.Key)
	})
}

func sign(v int64) int64 {
	switch {
	case v < 0:
		return -1
	case v > 0:
		return 1
	default:
		return 0
	}
}

// sessionRole is the caller's authority over one session's sharing, derived
// from IAM rather than stored: an admin of the org administers every session
// in it, the identity answerable for a session owns it, and any other
// validated member is a member of it.
func sessionRole(c *Call, s *session) string {
	switch {
	case c.Allows(Admin):
		return "admin"
	case sessionInvolves(s, c.User()):
		return "owner"
	default:
		return "member"
	}
}

// ---- sessions.patch -------------------------------------------------------

// The parameters whose presence keeps sessions.patch at operator.write. Every
// other field, and permissionMode "full" whatever else is sent, raises it to
// operator.admin. Transcribed from src/shared/session-method-scopes-base.ts so
// that this server and the UI's own pre-check agree; a hard-coded single scope
// would silently disagree with the controls the UI enables.
var (
	sessionEnvelope = []string{
		"key", "agentId", "expectedSessionId", "expectedLifecycleRevision",
		"expectedPermissionMode", "expectedMarkedUnreadAt",
	}
	sessionWritable = []string{
		"label", "icon", "color", "category", "boardFace",
		"pinned", "archived", "unread", "model", "permissionMode",
	}
)

// sessionPatchParams is SessionsPatchParamsSchema. A pointer distinguishes a
// value from a null, and the set of parameters actually sent distinguishes
// both from an absent field: null clears, absence leaves alone, and conflating
// them is how a rename silently erases a colour.
type sessionPatchParams struct {
	Key     string `json:"key"`
	AgentID string `json:"agentId"`

	ExpectedSessionID    *string       `json:"expectedSessionId"`
	ExpectedRevision     *string       `json:"expectedLifecycleRevision"`
	ExpectedPermission   *string       `json:"expectedPermissionMode"`
	ExpectedTools        *sessionTools `json:"expectedToolOverrides"`
	ExpectedMarkedUnread *int64        `json:"expectedMarkedUnreadAt"`

	Label         *string         `json:"label"`
	Icon          *string         `json:"icon"`
	Color         *string         `json:"color"`
	Category      *string         `json:"category"`
	BoardFace     *string         `json:"boardFace"`
	Note          *string         `json:"statusNote"`
	Attention     *string         `json:"attention"`
	TTLMinutes    *int            `json:"ttlMinutes"`
	Archived      *bool           `json:"archived"`
	Pinned        *bool           `json:"pinned"`
	Unread        *bool           `json:"unread"`
	Context       *string         `json:"contextWindow"`
	Thinking      *string         `json:"thinkingLevel"`
	FastMode      json.RawMessage `json:"fastMode"`
	Tools         *sessionTools   `json:"toolOverrides"`
	Verbose       *string         `json:"verboseLevel"`
	Trace         *string         `json:"traceLevel"`
	Reasoning     *string         `json:"reasoningLevel"`
	Usage         *string         `json:"responseUsage"`
	Elevated      *string         `json:"elevatedLevel"`
	ExecHost      *string         `json:"execHost"`
	ExecSecurity  *string         `json:"execSecurity"`
	ExecAsk       *string         `json:"execAsk"`
	ExecNode      *string         `json:"execNode"`
	Permission    *string         `json:"permissionMode"`
	Model         *string         `json:"model"`
	Completion    *string         `json:"completionOwnerSessionKey"`
	PolicyVersion *int            `json:"inheritedToolPolicyVersion"`
	ToolAllow     *[]string       `json:"inheritedToolAllow"`
	ToolDeny      *[]string       `json:"inheritedToolDeny"`
	SendPolicy    *string         `json:"sendPolicy"`
	Activation    *string         `json:"groupActivation"`
}

// sessionPatchResult is SessionsPatchResult, and also what sessions.reset
// answers with. `entry` is the row as it now stands; the UI narrows it to the
// handful of fields it re-reads after a write.
type sessionPatchResult struct {
	OK    bool     `json:"ok"`
	Path  string   `json:"path"`
	Key   string   `json:"key"`
	Entry *session `json:"entry"`
}

func patchSession(c *Call) (any, error) {
	sent, err := sentFields(c.Params())
	if err != nil {
		return nil, err
	}
	var p sessionPatchParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Key) == "" {
		return nil, Invalid("key is required")
	}
	if sessionPatchScope(sent, p.Permission) == Admin && !c.Allows(Admin) {
		return nil, MissingScope(Admin)
	}

	// The precondition is checked inside the act that writes, or it is not a
	// precondition: read outside it, a caller can state the revision it saw,
	// pass, and still be overwritten by a patch that read the same one.
	s, err := changeSession(c, p.Key, func(s *session, now int64) error {
		if err := sessionStale(&p, s); err != nil {
			return err
		}
		return sessionApply(sent, &p, s, now)
	})
	if err != nil {
		return nil, err
	}
	s.SharingRole = sessionRole(c, s)
	sessionMoved(c, s, "patch")
	return sessionPatchResult{OK: true, Path: sessionStore, Key: s.Key, Entry: s}, nil
}

// sessionPatchScope is the protocol's own rule for how much authority one
// patch costs.
func sessionPatchScope(sent map[string]bool, mode *string) Scope {
	if mode != nil && *mode == "full" {
		return Admin
	}
	for k := range sent {
		if !slices.Contains(sessionEnvelope, k) && !slices.Contains(sessionWritable, k) {
			return Admin
		}
	}
	return Write
}

// sessionStale rejects a mutation whose caller described a session that has
// since moved on. These are preconditions, not hints: a compare-and-set that
// does not match must refuse, never overwrite.
func sessionStale(p *sessionPatchParams, s *session) error {
	if p.ExpectedSessionID != nil && *p.ExpectedSessionID != s.SessionID {
		return Invalid("session %q has been reset since it was read", s.Key)
	}
	if p.ExpectedRevision != nil && *p.ExpectedRevision != sessionRevision(s) {
		return Invalid("session %q has changed since it was read", s.Key)
	}
	if p.ExpectedPermission != nil && *p.ExpectedPermission != s.PermissionMode {
		return Invalid("session %q is no longer in permission mode %q", s.Key, *p.ExpectedPermission)
	}
	if p.ExpectedMarkedUnread != nil && *p.ExpectedMarkedUnread != s.MarkedUnreadAt {
		return Invalid("session %q was marked unread since it was read", s.Key)
	}
	if p.ExpectedTools != nil && !sameTools(p.ExpectedTools, s.Tools) {
		return Invalid("session %q has a different tool overlay than the one stated", s.Key)
	}
	return nil
}

// sameTools compares two overlays by their encoding, which is what is stored
// and therefore what "still matches" means here.
func sameTools(a, b *sessionTools) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// sessionRevision names the generation of a row, so a caller that read one can
// say which one it acted on. Nothing here mutates a row without moving
// UpdatedAt, so the transcript identity and that time are enough.
func sessionRevision(s *session) string {
	return s.SessionID + ":" + strconv.FormatInt(s.UpdatedAt, 10)
}

// sessionApply writes the mutations the caller sent. A field the caller did
// not name is left where it was; a field named as null is cleared.
func sessionApply(sent map[string]bool, p *sessionPatchParams, s *session, now int64) error {
	for _, f := range []struct {
		name    string
		from    *string
		to      *string
		allowed []string
		max     int
	}{
		{"label", p.Label, &s.Label, nil, sessionLabel},
		{"icon", p.Icon, &s.Icon, nil, sessionLabel},
		{"color", p.Color, &s.Color, nil, sessionLabel},
		{"category", p.Category, &s.Category, nil, sessionLabel},
		{"boardFace", p.BoardFace, &s.BoardFace, sessionFaces, 0},
		{"attention", p.Attention, &s.Attention, sessionAttentions, 0},
		{"statusNote", p.Note, &s.Note, nil, sessionNote},
		{"contextWindow", p.Context, &s.ContextWindow, nil, sessionLabel},
		{"thinkingLevel", p.Thinking, &s.ThinkingLevel, nil, sessionLabel},
		{"verboseLevel", p.Verbose, &s.VerboseLevel, nil, sessionLabel},
		{"traceLevel", p.Trace, &s.TraceLevel, nil, sessionLabel},
		{"reasoningLevel", p.Reasoning, &s.Reasoning, nil, sessionLabel},
		{"responseUsage", p.Usage, &s.ResponseUsage, sessionUsages, 0},
		{"elevatedLevel", p.Elevated, &s.Elevated, nil, sessionLabel},
		{"execHost", p.ExecHost, &s.ExecHost, nil, sessionLabel},
		{"execNode", p.ExecNode, &s.ExecNode, nil, sessionLabel},
		{"permissionMode", p.Permission, &s.PermissionMode, sessionModes, 0},
		{"model", p.Model, &s.Model, nil, sessionLabel},
		{"completionOwnerSessionKey", p.Completion, &s.Completion, nil, sessionLabel},
		{"sendPolicy", p.SendPolicy, &s.SendPolicy, sessionPolicies, 0},
		{"groupActivation", p.Activation, &s.Activation, sessionActivations, 0},
	} {
		if !sent[f.name] {
			continue
		}
		v := ""
		if f.from != nil {
			v = strings.TrimSpace(*f.from)
		}
		if f.max > 0 && len(v) > f.max {
			return Invalid("%s exceeds %d bytes", f.name, f.max)
		}
		if v != "" && f.allowed != nil && !slices.Contains(f.allowed, v) {
			return Invalid("%s must be one of %s", f.name, strings.Join(f.allowed, ", "))
		}
		*f.to = v
	}

	// Clearing the note clears the attention and the expiry it was declared
	// with; they describe the note and outlive nothing.
	if sent["statusNote"] && s.Note == "" {
		s.Attention, s.NoteUntil = "", 0
	}
	if sent["ttlMinutes"] && p.TTLMinutes != nil {
		if *p.TTLMinutes < 1 || *p.TTLMinutes > 120 {
			return Invalid("ttlMinutes must be between 1 and 120")
		}
		s.NoteUntil = now + int64(*p.TTLMinutes)*60_000
	}
	if sent["fastMode"] {
		mode, err := sessionFast(p.FastMode)
		if err != nil {
			return err
		}
		s.FastMode = mode
	}
	if sent["toolOverrides"] {
		s.Tools = p.Tools
	}
	if sent["inheritedToolPolicyVersion"] {
		s.PolicyVersion = 0
		if p.PolicyVersion != nil {
			if *p.PolicyVersion != 1 {
				return Invalid("inheritedToolPolicyVersion must be 1")
			}
			s.PolicyVersion = 1
		}
	}
	if sent["inheritedToolAllow"] {
		s.ToolAllow = nil
		if p.ToolAllow != nil {
			s.ToolAllow = *p.ToolAllow
		}
	}
	if sent["inheritedToolDeny"] {
		s.ToolDeny = nil
		if p.ToolDeny != nil {
			s.ToolDeny = *p.ToolDeny
		}
	}
	if sent["archived"] && p.Archived != nil {
		s.Archived, s.ArchivedAt, s.ArchiveReason = *p.Archived, 0, ""
		if s.Archived {
			s.ArchivedAt, s.ArchiveReason = now, "manual"
		}
	}
	if sent["pinned"] && p.Pinned != nil {
		s.Pinned, s.PinnedAt = *p.Pinned, 0
		if s.Pinned {
			s.PinnedAt = now
		}
	}
	if sent["unread"] && p.Unread != nil {
		s.Unread = *p.Unread
		if s.Unread {
			s.MarkedUnreadAt = now
		} else {
			s.MarkedUnreadAt, s.LastReadAt = 0, now
		}
	}
	// execSecurity, execAsk and execNode are retired. They stay wire-valid so
	// that a client still sending them is answered rather than rejected;
	// nothing here routes on them, so they are accepted and not stored.
	return nil
}

// sessionFast reads the fastMode value: a boolean, the word "auto", or null to
// inherit.
func sessionFast(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, Invalid("fastMode: %v", err)
	}
	switch x := v.(type) {
	case bool:
		return raw, nil
	case string:
		if x == "auto" {
			return raw, nil
		}
	}
	return nil, Invalid(`fastMode must be a boolean, "auto" or null`)
}

// sentFields names the parameters the caller actually sent. The scope a patch
// costs and the difference between clearing a field and leaving it alone are
// both functions of that set, and neither can be recovered from the decoded
// value.
func sentFields(raw []byte) (map[string]bool, error) {
	out := map[string]bool{}
	if len(raw) == 0 {
		return out, nil
	}
	var seen map[string]json.RawMessage
	if err := json.Unmarshal(raw, &seen); err != nil {
		return nil, Invalid("params: %v", err)
	}
	for k := range seen {
		out[k] = true
	}
	return out, nil
}

// ---- sessions.reset -------------------------------------------------------

type sessionResetParams struct {
	Key     string `json:"key"`
	AgentID string `json:"agentId"`
	Reason  string `json:"reason"`
}

func resetSession(c *Call) (any, error) {
	var p sessionResetParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Key) == "" {
		return nil, Invalid("key is required")
	}
	reason := "reset"
	switch p.Reason {
	case "", "reset":
	case "new":
		reason = "new"
	default:
		return nil, Invalid(`reason must be "new" or "reset"`)
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var (
		s       *session
		deleted bool
	)
	// A turn still running would write its answer into the conversation this
	// call is emptying, and settle the row this call is clearing. It is stopped
	// first, through the one stop path.
	chatStop(c.Org(), c.Bot(), "", strings.TrimSpace(p.Key))
	// Reading the row and disposing of it are one act: a reset that read a row
	// another call has since rewritten would restore what that call removed.
	err = st.Do(c.Context(), func(st *Store) error {
		s, err = getSession(c, st, p.Key)
		if err != nil {
			return err
		}
		// The conversation goes with the row it belongs to. It is addressed by
		// the key, so nothing else disposes of it: a reset that left it in
		// place would answer ok, show the same history back, and carry the
		// discarded exchange into the next prompt.
		if err := st.Drop(c.Context(), chatAt(s.Key)); err != nil {
			return err
		}
		// An incognito session has nowhere to keep a fresh transcript, so it is
		// deleted rather than reset and the roster hears about a removal.
		if sessionIncognito(s.Key) {
			deleted = true
			return st.Delete(c.Context(), sessionsIn, s.Key)
		}
		// The key survives and the conversation does not. That split is the
		// substance of a reset: everything addressed by the key — the sidebar
		// row, its board, its settings — stays, and the exchange itself starts
		// again under a session id nothing carries over.
		now := time.Now().UnixMilli()
		s.SessionID = mint("ses")
		s.Status, s.LastRunID, s.LastRunError = "", "", ""
		s.LastSpokenAt, s.LastActivityAt = 0, now
		s.Unread, s.MarkedUnreadAt = false, 0
		return saveSession(c, st, s, now)
	})
	if err != nil {
		return nil, err
	}
	if deleted {
		sessionMoved(c, s, "delete")
		return map[string]any{"ok": true, "key": s.Key, "deleted": true}, nil
	}
	s.SharingRole = sessionRole(c, s)
	sessionMoved(c, s, reason)
	return sessionPatchResult{OK: true, Path: sessionStore, Key: s.Key, Entry: s}, nil
}

// sessionIncognito reports a session key whose transcript is never persisted
// (src/shared/incognito-session-key.ts).
func sessionIncognito(key string) bool {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(key)), ":")
	if len(parts) < 3 || parts[0] != "agent" || parts[1] == "" {
		return false
	}
	rest := strings.Join(parts[2:], ":")
	for _, prefix := range []string{"dashboard:", "subagent:", "internal-session-effects:"} {
		tail, cut := strings.CutPrefix(rest, prefix)
		if cut && strings.HasPrefix(tail, "incognito-") && !strings.Contains(tail, ":") {
			return true
		}
	}
	return false
}

// ---- sessions.abort -------------------------------------------------------

type sessionAbortParams struct {
	Key         string `json:"key"`
	RunID       string `json:"runId"`
	AgentID     string `json:"agentId"`
	ClearQueued bool   `json:"clearQueued"`
}

// sessionAbortResult is what the handler's own tests pin
// (src/gateway/server-methods/sessions-abort.test.ts): whether anything was
// stopped, and which run it was.
type sessionAbortResult struct {
	OK      bool    `json:"ok"`
	Aborted *string `json:"abortedRunId"`
	Status  string  `json:"status"` // aborted | no-active-run
}

// abortSession stops the run a session is carrying.
//
// It is the same stop chat.abort makes, resolved through the same registry: the
// live turn is the only thing that can be cancelled, and a row that says
// "killed" over a goroutine still asking a model is a Stop button that reports
// success and changes nothing. The UI reaches this method rather than chat.abort
// whenever the tab holds no run id — after a reload mid-turn, or from a second
// tab (ui/src/pages/chat/chat-session-action-access.ts) — which is exactly when
// the row and the work must not disagree.
//
// A turn that stops settles its own row as it unwinds, so this answers and
// leaves the bookkeeping to it. The row is rewritten only when nothing was
// running: a row left claiming a run that no longer exists is what an operator
// is looking at when they press Stop a second time.
func abortSession(c *Call) (any, error) {
	var p sessionAbortParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	key, runID := strings.TrimSpace(p.Key), strings.TrimSpace(p.RunID)
	if key == "" && runID == "" {
		return nil, Invalid("an abort names a session or a run")
	}
	if stopped, ok := chatStop(c.Org(), c.Bot(), runID, key); ok {
		return sessionAbortResult{OK: true, Status: "aborted", Aborted: &stopped}, nil
	}

	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var (
		target  *session
		stopped string
		idle    bool
	)
	// Finding the row and writing the stop into it are one act. A turn settles
	// its own row as it ends, so a stop that read the row a moment before that
	// would put back the run state the turn had just cleared.
	err = st.Do(c.Context(), func(st *Store) error {
		found, err := listSessionRows(c, st)
		if err != nil {
			return err
		}
		for i := range found {
			if (key != "" && found[i].Key == key) || (key == "" && found[i].LastRunID == runID) {
				target = &found[i]
				break
			}
		}
		switch {
		case target == nil && key != "":
			return Invalid("no session %q", key)
		case target == nil,
			runID != "" && target.LastRunID != runID,
			!slices.Contains(sessionRunning, target.Status):
			idle = true
			return nil
		}
		stopped = target.LastRunID
		target.Status = "killed"
		if p.ClearQueued {
			// The queue a followup would wait in is clients/tasks, and nothing
			// here has put work there. There is no second queue to drain.
			target.LastRunError = ""
		}
		return saveSession(c, st, target, time.Now().UnixMilli())
	})
	if err != nil {
		return nil, err
	}
	if idle {
		return sessionAbortResult{OK: true, Status: "no-active-run"}, nil
	}
	sessionMoved(c, target, "abort")
	out := sessionAbortResult{OK: true, Status: "aborted"}
	if stopped != "" {
		out.Aborted = &stopped
	}
	return out, nil
}

// ---- the group catalog ----------------------------------------------------

// sessionGroup is one catalog entry (SessionGroupSchema).
type sessionGroup struct {
	Name     string `json:"name"`
	Position int    `json:"position"`
}

// sessionPreset is a group's New Session defaults
// (SessionGroupDefaultsSchema). It is a separate surface from the catalog
// because it names filesystem paths: the catalog is readable by anyone, a path
// is not.
type sessionPreset struct {
	Name     string `json:"name"`
	Cwd      string `json:"cwd,omitempty"`
	Worktree bool   `json:"worktree,omitempty"`
}

// sessionCatalog is the whole catalog, kept as one document because every
// mutation rewrites the order of all of it.
type sessionCatalog struct {
	Groups   []sessionGroup  `json:"groups"`
	Sections []string        `json:"sectionOrder,omitempty"`
	Presets  []sessionPreset `json:"defaults,omitempty"`
}

func (b *sessionCatalog) at(name string) int {
	return slices.IndexFunc(b.Groups, func(g sessionGroup) bool { return g.Name == name })
}

// write restates position as the order of the slice — the one place order is
// decided — and stores the catalog.
func (b *sessionCatalog) write(c *Call, st *Store) error {
	for i := range b.Groups {
		b.Groups[i].Position = i
	}
	return st.Put(c.Context(), groupsIn, groupsAt, b)
}

// groupResult is the result shape the catalog mutations share.
type groupResult struct {
	OK       bool           `json:"ok"`
	Groups   []sessionGroup `json:"groups"`
	Sections []string       `json:"sectionOrder,omitempty"`
	Updated  int            `json:"updatedSessions"`
}

func readGroups(c *Call) (*Store, *sessionCatalog, error) {
	st, err := c.Store()
	if err != nil {
		return nil, nil, err
	}
	b, err := getGroups(c, st)
	if err != nil {
		return nil, nil, err
	}
	return st, b, nil
}

// getGroups reads the catalog out of the store it is given, so a mutation
// reads it inside the act that writes it.
func getGroups(c *Call, st *Store) (*sessionCatalog, error) {
	var b sessionCatalog
	if err := st.Get(c.Context(), groupsIn, groupsAt, &b); err != nil && !errors.Is(err, ErrNoDoc) {
		return nil, err
	}
	if b.Groups == nil {
		b.Groups = []sessionGroup{}
	}
	return &b, nil
}

// groupsMoved announces a catalog change to the org, once the change has
// landed. A mutation that sweeps member rows and then rewrites the catalog does
// both inside one act, so a failed one has moved nothing and there is nothing
// to announce.
func groupsMoved(c *Call) {
	Publish(c.Org(), c.Bot(), "", "sessions.changed", sessionChanged{
		Reason: "groups",
		TS:     time.Now().UnixMilli(),
	})
}

type groupPutParams struct {
	Names    []string `json:"names"`
	Sections []string `json:"sectionOrder"`
}

// putGroups replaces the catalog and the sidebar's section order.
//
// Dropping a name whose group still holds sessions is refused, and that
// refusal is the half of the contract the UI depends on: retiring a group is
// sessions.groups.delete's work, because only it sweeps the member rows, and a
// put that dropped a populated name would leave categories pointing at a group
// that no longer exists.
func putGroups(c *Call) (any, error) {
	var p groupPutParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	names, err := tidyNames(p.Names)
	if err != nil {
		return nil, err
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	type occupied struct {
		Name    string `json:"name"`
		Members int    `json:"memberSessions"`
	}
	// Counting the members of a group and dropping it are one act: a session
	// that joined the group between the count and the write would be left
	// pointing at a name the catalog no longer carries.
	var b *sessionCatalog
	err = st.Do(c.Context(), func(st *Store) error {
		b, err = getGroups(c, st)
		if err != nil {
			return err
		}
		found, err := listSessionRows(c, st)
		if err != nil {
			return err
		}
		var busy []occupied
		for _, g := range b.Groups {
			if slices.Contains(names, g.Name) {
				continue
			}
			members := 0
			for i := range found {
				if found[i].Category == g.Name {
					members++
				}
			}
			if members > 0 {
				busy = append(busy, occupied{Name: g.Name, Members: members})
			}
		}
		if len(busy) > 0 {
			return &Fault{
				Code:    codeInvalid,
				Message: "a group with member sessions cannot be dropped by a catalog write",
				Details: busy,
			}
		}

		b.Groups = b.Groups[:0]
		for _, n := range names {
			b.Groups = append(b.Groups, sessionGroup{Name: n})
		}
		// A preset belongs to a group; one whose group is gone has nothing to
		// name.
		b.Presets = slices.DeleteFunc(b.Presets, func(d sessionPreset) bool {
			return !slices.Contains(names, d.Name)
		})
		if p.Sections != nil {
			b.Sections, _ = tidyNames(p.Sections)
		}
		return b.write(c, st)
	})
	if err != nil {
		return nil, err
	}
	groupsMoved(c)
	return groupResult{OK: true, Groups: b.Groups, Sections: b.Sections}, nil
}

type groupRenameParams struct {
	Name string `json:"name"`
	To   string `json:"to"`
}

// renameGroup repoints every member row's category and retires the old catalog
// entry. Both are one act, so a rename that fails leaves the catalog and the
// rows it names saying the same thing they said before it started.
func renameGroup(c *Call) (any, error) {
	var p groupRenameParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	from, to := strings.TrimSpace(p.Name), strings.TrimSpace(p.To)
	if from == "" || to == "" {
		return nil, Invalid("a rename names a group and what to call it")
	}
	if len(to) > sessionLabel {
		return nil, Invalid("a group name exceeds %d bytes", sessionLabel)
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var (
		b     *sessionCatalog
		moved int
		same  bool
	)
	err = st.Do(c.Context(), func(st *Store) error {
		b, err = getGroups(c, st)
		if err != nil {
			return err
		}
		at := b.at(from)
		if at < 0 {
			return Invalid("no group %q", from)
		}
		if from == to {
			same = true
			return nil
		}
		if b.at(to) >= 0 {
			return Invalid("group %q already exists", to)
		}
		if moved, err = regroup(c, st, from, to); err != nil {
			return err
		}
		b.Groups[at].Name = to
		for i := range b.Presets {
			if b.Presets[i].Name == from {
				b.Presets[i].Name = to
			}
		}
		return b.write(c, st)
	})
	if err != nil {
		return nil, err
	}
	if same {
		return groupResult{OK: true, Groups: b.Groups, Sections: b.Sections}, nil
	}
	groupsMoved(c)
	return groupResult{OK: true, Groups: b.Groups, Sections: b.Sections, Updated: moved}, nil
}

type groupDeleteParams struct {
	Name string `json:"name"`
}

// deleteGroup retires a group and clears the category on its members. The
// sessions themselves are untouched, which is what the UI's confirmation says
// will happen.
func deleteGroup(c *Call) (any, error) {
	var p groupDeleteParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, Invalid("name is required")
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var (
		b     *sessionCatalog
		moved int
	)
	err = st.Do(c.Context(), func(st *Store) error {
		b, err = getGroups(c, st)
		if err != nil {
			return err
		}
		at := b.at(name)
		if at < 0 {
			return Invalid("no group %q", name)
		}
		if moved, err = regroup(c, st, name, ""); err != nil {
			return err
		}
		b.Groups = slices.Delete(b.Groups, at, at+1)
		b.Presets = slices.DeleteFunc(b.Presets, func(d sessionPreset) bool { return d.Name == name })
		return b.write(c, st)
	})
	if err != nil {
		return nil, err
	}
	groupsMoved(c)
	return groupResult{OK: true, Groups: b.Groups, Sections: b.Sections, Updated: moved}, nil
}

type groupUpdateParams struct {
	Name     string  `json:"name"`
	Cwd      *string `json:"cwd"`
	Worktree bool    `json:"worktree"`
}

// updateGroupDefaults sets the working directory and worktree preference a new
// session in this group starts with.
//
// A working directory is an absolute path on a machine, so naming one acts on
// the deployment rather than on the caller's own sessions, and it is admitted
// only to an admin of the org. The protocol resolves a member's path inside a
// configured workspace instead; this surface configures no workspaces, so
// there is nothing for a member's path to resolve inside.
func updateGroupDefaults(c *Call) (any, error) {
	var p groupUpdateParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, Invalid("name is required")
	}
	cwd := ""
	if p.Cwd != nil {
		cwd = strings.TrimSpace(*p.Cwd)
	}
	if cwd != "" {
		if !strings.HasPrefix(cwd, "/") {
			return nil, Invalid("cwd must be an absolute path")
		}
		if !c.Allows(Admin) {
			return nil, MissingScope(Admin)
		}
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var b *sessionCatalog
	err = st.Do(c.Context(), func(st *Store) error {
		b, err = getGroups(c, st)
		if err != nil {
			return err
		}
		if b.at(name) < 0 {
			return Invalid("no group %q", name)
		}
		next := sessionPreset{Name: name, Cwd: cwd, Worktree: p.Worktree}
		if at := slices.IndexFunc(b.Presets, func(d sessionPreset) bool { return d.Name == name }); at >= 0 {
			b.Presets[at] = next
		} else {
			b.Presets = append(b.Presets, next)
		}
		return b.write(c, st)
	})
	if err != nil {
		return nil, err
	}
	groupsMoved(c)
	if b.Presets == nil {
		b.Presets = []sessionPreset{}
	}
	return map[string]any{"ok": true, "defaults": b.Presets}, nil
}

// regroup moves every row in one category to another, or clears it when `to`
// is empty, and reports how many rows moved.
func regroup(c *Call, st *Store, from, to string) (int, error) {
	found, err := listSessionRows(c, st)
	if err != nil {
		return 0, err
	}
	moved, now := 0, time.Now().UnixMilli()
	for i := range found {
		if found[i].Category != from {
			continue
		}
		found[i].Category = to
		if err := saveSession(c, st, &found[i], now); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// tidyNames normalizes an ordered list of names: trimmed, bounded, in the
// order given, each one once.
func tidyNames(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		n := strings.TrimSpace(raw)
		if n == "" {
			continue
		}
		if len(n) > sessionLabel {
			return nil, Invalid("a name exceeds %d bytes", sessionLabel)
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// ---- session.visibility.set -----------------------------------------------

type sessionVisibilityParams struct {
	SessionKey string `json:"sessionKey"`
	AgentID    string `json:"agentId"`
	Visibility string `json:"visibility"`
}

// sessionShared is SessionSharingEventSchema.
type sessionShared struct {
	Action     string       `json:"action"`
	SessionKey string       `json:"sessionKey"`
	AgentID    string       `json:"agentId"`
	Visibility string       `json:"visibility,omitempty"`
	Actor      sessionActor `json:"actor"`
	TS         int64        `json:"ts"`
}

// setVisibility changes who can see one session.
//
// The write is one field; the substance is who may make it. Sharing authority
// is IAM's answer and not a second permission system: an admin of the org, or
// the identity answerable for the session, may change it, and any other member
// may not.
func setVisibility(c *Call) (any, error) {
	var p sessionVisibilityParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.SessionKey) == "" {
		return nil, Invalid("sessionKey is required")
	}
	if !slices.Contains(sessionVisibles, p.Visibility) {
		return nil, Invalid("visibility must be one of %s", strings.Join(sessionVisibles, ", "))
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var (
		s    *session
		same bool
		now  int64
	)
	err = st.Do(c.Context(), func(st *Store) error {
		s, err = getSession(c, st, p.SessionKey)
		if err != nil {
			return err
		}
		if role := sessionRole(c, s); role != "admin" && role != "owner" {
			return Forbidden("only a session's owner or an admin of the org may share it")
		}
		// Setting what is already set changes nothing and announces nothing: a
		// broadcast here would make every open client redraw for a write that
		// did not happen, and moving the row's revision would fail the next
		// caller's precondition over a write nobody made.
		if s.Visibility == p.Visibility {
			same = true
			return nil
		}
		s.Visibility = p.Visibility
		now = time.Now().UnixMilli()
		return saveSession(c, st, s, now)
	})
	if err != nil {
		return nil, err
	}
	answer := map[string]any{"ok": true, "sessionKey": s.Key, "visibility": p.Visibility}
	if same {
		return answer, nil
	}
	Publish(c.Org(), c.Bot(), "", "session.sharing", sessionShared{
		Action:     "visibility",
		SessionKey: s.Key,
		AgentID:    s.agent(),
		Visibility: s.Visibility,
		Actor:      sessionActor{Type: "human", ID: c.User()},
		TS:         now,
	})
	sessionMoved(c, s, "sharing")
	return answer, nil
}

// ---- session.typing -------------------------------------------------------

type sessionTypingParams struct {
	SessionKey string `json:"sessionKey"`
	AgentID    string `json:"agentId"`
	SessionID  string `json:"sessionId"`
	Typing     bool   `json:"typing"`
	Preview    string `json:"preview"`
}

// sessionTyped is SessionTypingEventSchema.
type sessionTyped struct {
	SessionKey string       `json:"sessionKey"`
	SessionID  string       `json:"sessionId"`
	AgentID    string       `json:"agentId"`
	Actor      sessionActor `json:"actor"`
	Typing     bool         `json:"typing"`
	Preview    string       `json:"preview,omitempty"`
	TS         int64        `json:"ts"`
}

// setTyping tells the other people looking at a session that this viewer is
// writing, and optionally what.
//
// The draft goes only to the connections that asked to hear about this session
// (Call.Watch), never to the org: a preview is the operator's unsent words,
// and the people entitled to see them are exactly the people watching the
// session they are being written into. When nobody else is watching, the
// answer says so — broadcast false — and nothing is sent. Every reason to stay
// quiet answers that way rather than failing, because the client sends this on
// every keystroke and reads an error as an error.
func setTyping(c *Call) (any, error) {
	var p sessionTypingParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.SessionKey) == "" || strings.TrimSpace(p.SessionID) == "" {
		return nil, Invalid("a typing notice names a session and its current transcript")
	}
	if len(p.Preview) > sessionPreview {
		return nil, Invalid("preview exceeds %d bytes", sessionPreview)
	}
	quiet := map[string]any{"ok": true, "broadcast": false}

	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var s session
	if err := st.Get(c.Context(), sessionsIn, p.SessionKey, &s); errors.Is(err, ErrNoDoc) {
		return quiet, nil
	} else if err != nil {
		return nil, err
	}
	// A notice about a transcript the row has moved past belongs to a view
	// that is no longer current.
	if s.SessionID != p.SessionID || sessionWatchers(c, s.Key) == 0 {
		return quiet, nil
	}
	Publish(c.Org(), c.Bot(), s.Key, "session.typing", sessionTyped{
		SessionKey: s.Key,
		SessionID:  s.SessionID,
		AgentID:    s.agent(),
		Actor:      sessionActor{Type: "human", ID: c.User()},
		Typing:     p.Typing,
		Preview:    p.Preview,
		TS:         time.Now().UnixMilli(),
	})
	return map[string]any{"ok": true, "broadcast": true}, nil
}

// sessionWatchers counts the connections a notice about this session would
// reach, other than the caller's own. It reads the hub through the same
// audience the publish uses, so "nobody is watching" and "nobody was sent it"
// are one answer rather than two.
func sessionWatchers(c *Call, key string) int {
	n := 0
	for _, k := range watchers(c.Org(), c.Bot(), key) {
		if k != c.conn {
			n++
		}
	}
	return n
}
