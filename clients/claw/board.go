package claw

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The board is the dashboard a session carries: tabs of widgets an agent puts
// there and the person watching arranges. A widget is a sandboxed document, so
// what it asks of this cloud arrives as a method of its own carrying a ticket,
// never as a call from the person watching: board.event reports what the widget
// did, board.prompt.authorize asks whether the text it wants to send needs a
// click first, and board.data.read and board.action run the one read or the one
// action the widget declared and the person granted.
//
// Two facts shape the code.
//
// A ticket names a widget view, not a caller. The caller is the validated org
// already — these methods come through the same door as every other — so the
// ticket answers which widget of that org's board is asking, and it stops
// answering the moment that widget changes: it is a secret held beside the
// board, checked against the revision and the instance it was minted for and
// replaced whenever the widget is written. OpenClaw binds the same claim to the
// lifetime of one process; here the row is the authority, which keeps the
// property and drops the coupling to a single replica.
//
// A read a widget asks for is a method this surface already answers. So
// board.data.read and board.action check the grant and then run the ordinary
// method — sessions.list, cron.run — rather than keeping a second copy of it
// behind a board-shaped name. The delegated call carries no connection, which
// is what makes a board read one-shot: it cannot leave a subscription behind.
func init() {
	Register("board.get", Read, boardGet)
	Register("board.update", Write, boardUpdate)
	Register("board.widget.put", Write, widgetPut)
	Register("board.widget.grant", Approvals, widgetGrant)
	Register("board.event", Write, boardEvent)
	Register("board.prompt.authorize", Read, promptAuthorize)
	Register("board.data.read", Read, dataRead)
	Register("board.action", Write, boardAction)
	Register("wiki.get", Read, wikiGet)
	Announce("board.changed")
}

// The collections one board occupies. The org — and the bot, when the call is
// bound to one — is the file, so a board is addressed by session alone.
//
//	board         the wire snapshot: the tabs, and a row per widget
//	view          one entry per widget: its ticket secret and when that lapses
//	widget:<key>  the widget content, one document each, kept out of the
//	              snapshot because a document of HTML is far larger than the
//	              row describing it
//	notice        what the widgets have reported, and what each said last
const (
	colBoard  = "board"
	colView   = "view"
	colNotice = "notice"
	colWiki   = "wiki"
)

// docs is where one board's widget content lives.
func docs(k string) string { return "widget:" + k }

const (
	maxWidgets = 48         // widgets one board may hold
	maxHTML    = MaxDoc / 2 // widget content, leaving the document its JSON
	maxProps   = 8 << 10    // a plugin widget's props
	maxCall    = 8 << 10    // params of one data read or one action
	maxState   = 8 << 10    // one payload a widget reports
	maxSession = 512        // a session key, as the protocol bounds its claims
	maxTools   = 64         // tools one widget may declare
	maxOrigins = 32         // network origins one widget may declare
	maxTool    = 269        // "cron.trigger:" and the longest job id
	maxNotice  = 500        // characters of one notice
	keepNotice = 100        // notices retained per board
	ticketLife = 20 * time.Minute
	quiet      = 5 * time.Second // a widget repeating itself inside this said one thing
)

// The closed sets, spelled as the schema's own literals.
var (
	docks   = map[string]bool{"left": true, "right": true, "bottom": true, "hidden": true}
	shows   = map[string]bool{"card": true, "full-bleed": true, "frameless": true}
	heights = map[string]bool{"auto": true, "fixed": true}
	presets = map[string][2]int{
		"sm": {3, 3}, "md": {6, 4}, "lg": {8, 6}, "xl": {12, 8}, "full": {12, 8},
	}
)

// Where a widget's declaration stands. A widget that declared nothing is
// granted nothing and needs nothing, which is why it rests at none rather than
// waiting at pending.
const (
	grantNone     = "none"
	grantPending  = "pending"
	grantGranted  = "granted"
	grantRejected = "rejected"
)

var (
	tabRE    = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	nameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	agentRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	pluginRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}:[a-z0-9][a-z0-9._-]{0,63}$`)
)

// bindings is what a widget may read: each name is a method this surface
// already answers, read under the grant the widget was given. A name outside
// the set is refused before anything is dispatched, so a granted tool string
// can never become a way to reach an arbitrary method.
var bindings = map[string]bool{
	"sessions.list": true, "usage.status": true, "usage.cost": true,
	"cron.list": true, "cron.status": true, "agents.list": true, "health": true,
}

// ---- the wire shapes (BoardSnapshotSchema and what it holds) ----

type board struct {
	Session  string   `json:"sessionKey"`
	Revision int      `json:"revision"`
	Tabs     []tab    `json:"tabs"`
	Widgets  []widget `json:"widgets"`
}

type tab struct {
	ID       string `json:"tabId"`
	Title    string `json:"title"`
	Position int    `json:"position"`
	Dock     string `json:"chatDock"`
}

type widget struct {
	Name         string          `json:"name"`
	Tab          string          `json:"tabId"`
	Title        string          `json:"title,omitempty"`
	Kind         string          `json:"contentKind"`
	Owner        string          `json:"contentOwner,omitempty"`
	PluginKind   string          `json:"pluginKind,omitempty"`
	Props        json.RawMessage `json:"props,omitempty"`
	Presentation string          `json:"presentation,omitempty"`
	Height       string          `json:"heightMode,omitempty"`
	SizeW        int             `json:"sizeW"`
	SizeH        int             `json:"sizeH"`
	Position     int             `json:"position"`
	Grant        string          `json:"grantState"`
	Revision     int             `json:"revision"`
	Instance     string          `json:"instanceId,omitempty"`
	Generation   string          `json:"viewGeneration,omitempty"`
	Summary      []string        `json:"declaredSummary,omitempty"`
	Declared     *declared       `json:"declared,omitempty"`

	// Minted per read from the view kept beside the board, never stored on the
	// row: a ticket outlives neither the widget nor twenty minutes.
	Frame      string `json:"frameUrl,omitempty"`
	Ticket     string `json:"viewTicket,omitempty"`
	TicketLife int64  `json:"viewTicketTtlMs,omitempty"`

	// The bytes this widget holds, and the bytes it was granted at. They answer
	// the next write and nothing else, and the schema they would travel under
	// is closed, so wire() drops them before the row is sent.
	Sha       string `json:"sha,omitempty"`
	GrantedAt string `json:"grantedSha,omitempty"`
}

// declared is everything a widget asks for. It is what the person reads in the
// grant prompt, so it is normalized on the way in: an origin that is not one
// exact HTTPS origin is refused rather than shown.
type declared struct {
	Origins []string `json:"netOrigins,omitempty"`
	Tools   []string `json:"tools,omitempty"`
}

// view is the ticket state of one widget: the secret a ticket carries, and when
// it lapses. It is kept out of the snapshot because the snapshot is sent.
type view struct {
	Secret string `json:"secret"`
	Lapses int64  `json:"lapses"`
}

// content is one widget's document.
type content struct {
	HTML string `json:"html,omitempty"`
}

// placement is where a put asks for its widget to land.
type placement struct {
	Tab   string `json:"tabId"`
	Size  string `json:"size"`
	After string `json:"after"`
}

// changed is the event a board raises when it is written.
type changed struct {
	Session  string `json:"sessionKey"`
	Revision int    `json:"revision"`
	Widget   string `json:"widget,omitempty"`
}

// ---- addressing ----

// locate names the board a call acts on. The agent is part of that identity
// rather than a prefix on the session, so two agents watching one session key
// keep two boards; an agent id cannot hold a colon, which is what lets the
// session be read back out of the key.
func locate(session, agent string) (string, error) {
	session = strings.TrimSpace(session)
	if session == "" || len(session) > maxSession {
		return "", Invalid("sessionKey is required")
	}
	agent = strings.TrimSpace(agent)
	if agent != "" && !agentRE.MatchString(agent) {
		return "", Invalid("agentId is not an agent")
	}
	return agent + ":" + session, nil
}

// iframe is where the widget document is served. The board hands the client
// this path, the client loads it in the sandboxed frame, and the ticket in the
// query is what that document presents when it calls back.
func iframe(session, name, ticket string) string {
	return "/v1/claw/board/" + url.PathEscape(session) + "/" + url.PathEscape(name) +
		"/index.html?bt=" + url.QueryEscape(ticket)
}

// token is 128 bits of hex: a view secret, and the generation stamped on each
// write of a widget's content.
func token() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func sha(s string) string {
	if s == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---- reading and writing the board ----

func load(c *Call, st *Store, k, session string) (board, error) {
	var b board
	if err := st.Get(c.Context(), colBoard, k, &b); err != nil && !errors.Is(err, ErrNoDoc) {
		c.Log().Error("read board", "org", c.Org(), "err", err)
		return board{}, Unavailable("the board could not be read")
	}
	// A session that has never had a board reads as an empty one: a board comes
	// into being when something is put on it, not when it is first looked at.
	b.Session = session
	if b.Tabs == nil {
		b.Tabs = []tab{}
	}
	if b.Widgets == nil {
		b.Widgets = []widget{}
	}
	return b, nil
}

func save(c *Call, st *Store, k string, b board) error {
	if err := st.Put(c.Context(), colBoard, k, b); err != nil {
		var f *Fault
		if errors.As(err, &f) {
			return f
		}
		c.Log().Error("write board", "org", c.Org(), "err", err)
		return Unavailable("the board could not be written")
	}
	return nil
}

func held(c *Call, st *Store, k string) (map[string]view, error) {
	out := map[string]view{}
	if err := st.Get(c.Context(), colView, k, &out); err != nil && !errors.Is(err, ErrNoDoc) {
		c.Log().Error("read board views", "org", c.Org(), "err", err)
		return nil, Unavailable("the board could not be read")
	}
	if out == nil {
		out = map[string]view{}
	}
	return out, nil
}

func keep(c *Call, st *Store, k string, views map[string]view) error {
	if err := st.Put(c.Context(), colView, k, views); err != nil {
		c.Log().Error("write board views", "org", c.Org(), "err", err)
		return Unavailable("the board could not be written")
	}
	return nil
}

// wire projects the snapshot a client is sent.
func wire(b board) board {
	out := b
	out.Widgets = make([]widget, len(b.Widgets))
	copy(out.Widgets, b.Widgets)
	for i := range out.Widgets {
		out.Widgets[i].Sha = ""
		out.Widgets[i].GrantedAt = ""
	}
	return out
}

// normalize is the board's one ordering rule: tabs run 0..n-1 in the order
// their positions put them, and each tab's widgets run 0..n-1 within it. Every
// operation reorders by writing positions and then calls this, so no operation
// keeps the numbering itself.
func normalize(b *board) {
	slices.SortStableFunc(b.Tabs, func(x, y tab) int { return x.Position - y.Position })
	rank := map[string]int{}
	for i := range b.Tabs {
		b.Tabs[i].Position = i
		rank[b.Tabs[i].ID] = i
	}
	place := func(w widget) int {
		if r, ok := rank[w.Tab]; ok {
			return r
		}
		return len(b.Tabs) // a widget on a tab that is gone sorts last
	}
	slices.SortStableFunc(b.Widgets, func(x, y widget) int {
		if rx, ry := place(x), place(y); rx != ry {
			return rx - ry
		}
		return x.Position - y.Position
	})
	next := map[string]int{}
	for i := range b.Widgets {
		t := b.Widgets[i].Tab
		b.Widgets[i].Position = next[t]
		next[t]++
	}
}

func tabAt(b *board, id string) *tab {
	for i := range b.Tabs {
		if b.Tabs[i].ID == id {
			return &b.Tabs[i]
		}
	}
	return nil
}

func widgetAt(b *board, name string) *widget {
	for i := range b.Widgets {
		if b.Widgets[i].Name == name {
			return &b.Widgets[i]
		}
	}
	return nil
}

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }

// move places one widget on a tab, either at an index or after a named
// neighbour, and renumbers that tab. It writes positions only; normalize puts
// the slice in that order afterwards.
func move(b *board, w *widget, id string, position *int, after string) error {
	if tabAt(b, id) == nil {
		return Invalid("board tab not found: %s", id)
	}
	if position != nil && after != "" {
		return Invalid("widget_move accepts either position or after, not both")
	}
	peers := []*widget{}
	for i := range b.Widgets {
		if &b.Widgets[i] != w && b.Widgets[i].Tab == id {
			peers = append(peers, &b.Widgets[i])
		}
	}
	slices.SortStableFunc(peers, func(x, y *widget) int { return x.Position - y.Position })
	at := len(peers)
	switch {
	case after != "":
		if after == w.Name {
			return Invalid("widget cannot be placed after itself")
		}
		i := slices.IndexFunc(peers, func(p *widget) bool { return p.Name == after })
		if i < 0 {
			return Invalid("board widget anchor not found on tab %s: %s", id, after)
		}
		at = i + 1
	case position != nil:
		at = clamp(*position, 0, len(peers))
	}
	w.Tab = id
	for i, p := range peers {
		if i < at {
			p.Position = i
		} else {
			p.Position = i + 1
		}
	}
	w.Position = at
	return nil
}

// strict decodes one arm of a union, refusing a field it does not declare. It
// is the reading of a closed object that Call.Bind applies to a method's
// parameters, applied one level down.
func strict(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return Invalid("%v", err)
	}
	return nil
}

// ---- board.get ----

func boardGet(c *Call) (any, error) {
	var p struct {
		Session string `json:"sessionKey"`
		Agent   string `json:"agentId"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	k, err := locate(p.Session, p.Agent)
	if err != nil {
		return nil, err
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	// Reading a board mints the view tickets its widgets are served under, and
	// the tickets are one document: read and written separately, two reads of
	// one board each store the tickets they minted and one client is handed a
	// ticket that no longer resolves.
	var b board
	if err := st.Do(c.Context(), func(st *Store) error {
		b, err = load(c, st, k, p.Session)
		if err != nil {
			return err
		}
		return tickets(c, st, k, &b)
	}); err != nil {
		return nil, err
	}
	// Reading a board is how a client says it is watching one. The session key
	// is the whole address: the bot is a dimension of the event rather than
	// part of the key, so a connection bound to another bot never hears this
	// one whatever it watches.
	c.Watch(p.Session)
	return wire(b), nil
}

// tickets mints a view ticket for every widget the person may see. A ticket is
// replaced once it is inside the last quarter of its life, so the client's own
// refresh — which waits for the string to change — always gets a new one, while
// a board that is merely being read writes nothing.
func tickets(c *Call, st *Store, k string, b *board) error {
	views, err := held(c, st, k)
	if err != nil {
		return err
	}
	now := time.Now()
	fresh := false
	for i := range b.Widgets {
		w := &b.Widgets[i]
		if w.Grant != grantNone && w.Grant != grantGranted {
			continue
		}
		v := views[w.Name]
		if v.Secret == "" || now.Add(ticketLife/4).UnixMilli() > v.Lapses {
			v = view{Secret: token(), Lapses: now.Add(ticketLife).UnixMilli()}
			views[w.Name] = v
			fresh = true
		}
		w.Ticket = ticket(k, w.Name, v.Secret)
		w.TicketLife = ticketLife.Milliseconds()
		w.Frame = iframe(b.Session, w.Name, w.Ticket)
	}
	// Views of widgets that are gone are dropped on the same pass that mints.
	for name := range views {
		if widgetAt(b, name) == nil {
			delete(views, name)
			fresh = true
		}
	}
	if !fresh {
		return nil
	}
	return keep(c, st, k, views)
}

// ticket carries the board and the widget it was minted for, and a secret only
// this cloud holds. The first half addresses; the second half authorizes.
func ticket(k, name, secret string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(k+"\x00"+name)) + "." + secret
}

// seen resolves a ticket to the widget it names. It refuses one whose widget
// has been rewritten, re-granted or removed since it was minted, which is what
// makes a ticket stop working the moment what it names changes.
func seen(c *Call, t string) (*Store, string, widget, error) {
	stale := Invalid("board widget view ticket is stale")
	bad := Invalid("board widget view ticket is invalid")
	if len(t) > 2048 {
		return nil, "", widget{}, bad
	}
	addr, secret, ok := strings.Cut(t, ".")
	if !ok {
		return nil, "", widget{}, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(addr)
	if err != nil {
		return nil, "", widget{}, bad
	}
	k, name, ok := strings.Cut(string(raw), "\x00")
	if !ok {
		return nil, "", widget{}, bad
	}
	st, err := c.Store()
	if err != nil {
		return nil, "", widget{}, err
	}
	views, err := held(c, st, k)
	if err != nil {
		return nil, "", widget{}, err
	}
	v, ok := views[name]
	if !ok || v.Lapses <= time.Now().UnixMilli() ||
		subtle.ConstantTimeCompare([]byte(v.Secret), []byte(secret)) != 1 {
		return nil, "", widget{}, stale
	}
	_, session, _ := strings.Cut(k, ":")
	b, err := load(c, st, k, session)
	if err != nil {
		return nil, "", widget{}, err
	}
	w := widgetAt(&b, name)
	if w == nil || (w.Grant != grantNone && w.Grant != grantGranted) {
		return nil, "", widget{}, stale
	}
	return st, k, *w, nil
}

// ---- board.update ----

func boardUpdate(c *Call) (any, error) {
	var p struct {
		Session string            `json:"sessionKey"`
		Agent   string            `json:"agentId"`
		Ops     []json.RawMessage `json:"ops"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	k, err := locate(p.Session, p.Agent)
	if err != nil {
		return nil, err
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	// The board is one document, so the operations are applied to the board as
	// it stands when they are written, not to a copy read before another call
	// rewrote it — and the widget content a removal drops goes with them.
	var b board
	quiet := len(p.Ops) == 0
	if err := st.Do(c.Context(), func(st *Store) error {
		b, err = load(c, st, k, p.Session)
		if err != nil {
			return err
		}
		if quiet {
			return tickets(c, st, k, &b)
		}
		gone := []string{}
		for _, raw := range p.Ops {
			dropped, err := apply(&b, raw)
			if err != nil {
				return err
			}
			if dropped != "" {
				gone = append(gone, dropped)
			}
			normalize(&b)
		}
		b.Revision++
		if err := save(c, st, k, b); err != nil {
			return err
		}
		for _, name := range gone {
			if err := st.Delete(c.Context(), docs(k), name); err != nil {
				return err
			}
		}
		return tickets(c, st, k, &b)
	}); err != nil {
		return nil, err
	}
	if quiet {
		return wire(b), nil
	}
	Publish(c.Org(), c.Bot(), p.Session, "board.changed",
		changed{Session: p.Session, Revision: b.Revision})
	return wire(b), nil
}

// apply runs one operation, answering with the name of a widget it removed.
// The operations are the seven arms of BoardOpSchema and each arm is closed, so
// the kind is read first and the rest is decoded against it.
func apply(b *board, raw []byte) (string, error) {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", Invalid("op: %v", err)
	}
	switch head.Kind {
	case "tab_create":
		var op struct {
			Kind  string `json:"kind"`
			ID    string `json:"tabId"`
			Title string `json:"title"`
			Dock  string `json:"chatDock"`
		}
		if err := strict(raw, &op); err != nil {
			return "", err
		}
		if !tabRE.MatchString(op.ID) {
			return "", Invalid("tabId is not a tab")
		}
		if op.Title == "" || len(op.Title) > 80 {
			return "", Invalid("a tab title is 1 to 80 characters")
		}
		if op.Dock == "" {
			op.Dock = "right"
		}
		if !docks[op.Dock] {
			return "", Invalid("chatDock is one of left, right, bottom, hidden")
		}
		if tabAt(b, op.ID) != nil {
			return "", Invalid("board tab already exists: %s", op.ID)
		}
		b.Tabs = append(b.Tabs, tab{ID: op.ID, Title: op.Title, Position: len(b.Tabs), Dock: op.Dock})

	case "tab_update":
		var op struct {
			Kind     string `json:"kind"`
			ID       string `json:"tabId"`
			Title    string `json:"title"`
			Dock     string `json:"chatDock"`
			Position *int   `json:"position"`
		}
		if err := strict(raw, &op); err != nil {
			return "", err
		}
		t := tabAt(b, op.ID)
		if t == nil {
			return "", Invalid("board tab not found: %s", op.ID)
		}
		if op.Title == "" && op.Dock == "" && op.Position == nil {
			return "", Invalid("tab_update has no changes")
		}
		if op.Title != "" {
			if len(op.Title) > 80 {
				return "", Invalid("a tab title is 1 to 80 characters")
			}
			t.Title = op.Title
		}
		if op.Dock != "" {
			if !docks[op.Dock] {
				return "", Invalid("chatDock is one of left, right, bottom, hidden")
			}
			t.Dock = op.Dock
		}
		if op.Position != nil {
			// A tab takes the position it asked for and pushes the rest along;
			// normalize turns that into 0..n-1 again.
			to, from := clamp(*op.Position, 0, len(b.Tabs)-1), t.Position
			for i := range b.Tabs {
				switch at := b.Tabs[i].Position; {
				case b.Tabs[i].ID == op.ID:
					b.Tabs[i].Position = to
				case from < to && at > from && at <= to:
					b.Tabs[i].Position = at - 1
				case from > to && at >= to && at < from:
					b.Tabs[i].Position = at + 1
				}
			}
		}

	case "tab_delete":
		var op struct {
			Kind string `json:"kind"`
			ID   string `json:"tabId"`
		}
		if err := strict(raw, &op); err != nil {
			return "", err
		}
		if tabAt(b, op.ID) == nil {
			return "", Invalid("board tab not found: %s", op.ID)
		}
		orphans := 0
		for i := range b.Widgets {
			if b.Widgets[i].Tab == op.ID {
				orphans++
			}
		}
		if len(b.Tabs) == 1 && orphans > 0 {
			return "", Invalid("cannot delete the last board tab while it holds widgets")
		}
		b.Tabs = slices.DeleteFunc(b.Tabs, func(t tab) bool { return t.ID == op.ID })
		// The widgets of a deleted tab move to the first tab left, after
		// whatever is already there.
		for i := range b.Widgets {
			if b.Widgets[i].Tab == op.ID {
				b.Widgets[i].Tab = b.Tabs[0].ID
				b.Widgets[i].Position = len(b.Widgets)
			}
		}

	case "tabs_reorder":
		var op struct {
			Kind string   `json:"kind"`
			IDs  []string `json:"tabIds"`
		}
		if err := strict(raw, &op); err != nil {
			return "", err
		}
		wrong := Invalid("tabs_reorder must name every tab exactly once")
		if len(op.IDs) != len(b.Tabs) {
			return "", wrong
		}
		named := map[string]bool{}
		for i, id := range op.IDs {
			t := tabAt(b, id)
			if t == nil || named[id] {
				return "", wrong
			}
			named[id] = true
			t.Position = i
		}

	case "widget_move":
		var op struct {
			Kind     string `json:"kind"`
			Name     string `json:"name"`
			Tab      string `json:"tabId"`
			Position *int   `json:"position"`
			After    string `json:"after"`
		}
		if err := strict(raw, &op); err != nil {
			return "", err
		}
		w := widgetAt(b, op.Name)
		if w == nil {
			return "", Invalid("board widget not found: %s", op.Name)
		}
		id := op.Tab
		if id == "" {
			id = w.Tab
		}
		if err := move(b, w, id, op.Position, op.After); err != nil {
			return "", err
		}

	case "widget_resize":
		var op struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			SizeW  int    `json:"sizeW"`
			SizeH  int    `json:"sizeH"`
			Height string `json:"heightMode"`
		}
		if err := strict(raw, &op); err != nil {
			return "", err
		}
		w := widgetAt(b, op.Name)
		if w == nil {
			return "", Invalid("board widget not found: %s", op.Name)
		}
		if op.Height != "" && !heights[op.Height] {
			return "", Invalid("heightMode is auto or fixed")
		}
		w.SizeW, w.SizeH = clamp(op.SizeW, 1, 12), clamp(op.SizeH, 1, 20)
		// A resize is the person's own intent, so the height stops following
		// the content: without this the next content report undoes the drag.
		w.Height = "fixed"
		if op.Height != "" {
			w.Height = op.Height
		}

	case "widget_remove":
		var op struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}
		if err := strict(raw, &op); err != nil {
			return "", err
		}
		if widgetAt(b, op.Name) == nil {
			return "", Invalid("board widget not found: %s", op.Name)
		}
		b.Widgets = slices.DeleteFunc(b.Widgets, func(w widget) bool { return w.Name == op.Name })
		return op.Name, nil

	default:
		return "", Invalid("unknown board op: %s", head.Kind)
	}
	return "", nil
}

// ---- board.widget.put ----

func widgetPut(c *Call) (any, error) {
	var p struct {
		Session      string          `json:"sessionKey"`
		Agent        string          `json:"agentId"`
		Name         string          `json:"name"`
		Title        string          `json:"title"`
		Content      json.RawMessage `json:"content"`
		Presentation string          `json:"presentation"`
		Height       string          `json:"heightMode"`
		Placement    *placement      `json:"placement"`
		Declared     *declared       `json:"declared"`
		// A generated identity lets a producer keep one widget's name across
		// rewrites. Nothing here generates one, so it is read and ignored.
		Identity json.RawMessage `json:"generatedIdentity"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	k, err := locate(p.Session, p.Agent)
	if err != nil {
		return nil, err
	}
	if !nameRE.MatchString(p.Name) {
		return nil, Invalid("name is not a widget")
	}
	if len(p.Title) > 80 {
		return nil, Invalid("a widget title is at most 80 characters")
	}
	if p.Presentation != "" && !shows[p.Presentation] {
		return nil, Invalid("presentation is one of card, full-bleed, frameless")
	}
	if p.Height != "" && !heights[p.Height] {
		return nil, Invalid("heightMode is auto or fixed")
	}
	kind, it, err := unpack(p.Content)
	if err != nil {
		return nil, err
	}
	dec, err := normalized(p.Declared)
	if err != nil {
		return nil, err
	}
	if kind == "plugin" {
		// A plugin widget is drawn by the client's own plugin rather than by a
		// document this cloud holds, so it declares nothing and holds nothing.
		dec = nil
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	// A widget is three documents — the board, the bytes it serves, and the
	// view tickets — and they say one thing about one widget. Written
	// separately, a rewrite that lands its bytes while another call rewrites
	// the board leaves the board granting a capability over bytes nobody
	// approved. So they are written together, and a board read here is read
	// inside the act that writes it: the board is one document, and two puts
	// that each load it and then store the whole of it keep only the second
	// one's widget.
	var b board
	if err := st.Do(c.Context(), func(st *Store) error {
		b, err = load(c, st, k, p.Session)
		if err != nil {
			return err
		}
		if len(b.Tabs) == 0 {
			b.Tabs = append(b.Tabs, tab{ID: "main", Title: "Main", Dock: "right"})
		}
		old := widgetAt(&b, p.Name)
		if old == nil && len(b.Widgets) >= maxWidgets {
			return Invalid("a board holds at most %d widgets", maxWidgets)
		}
		if old != nil && old.Owner != kind {
			return Invalid("board widget %s holds %s content; remove it before replacing it with %s content",
				p.Name, old.Owner, kind)
		}

		w := widget{
			Name:         p.Name,
			Title:        p.Title,
			Kind:         kind,
			Owner:        kind,
			PluginKind:   it.PluginKind,
			Props:        it.Props,
			Presentation: p.Presentation,
			Height:       p.Height,
			Grant:        grantNone,
			Revision:     1,
			Instance:     token(),
			Summary:      summary(dec),
			Declared:     dec,
			Sha:          sha(it.HTML),
		}
		w.Generation = w.Instance

		id := b.Tabs[0].ID
		switch {
		case p.Placement != nil && p.Placement.Tab != "":
			id = p.Placement.Tab
		case old != nil:
			id = old.Tab
		}
		if tabAt(&b, id) == nil {
			return Invalid("board tab not found: %s", id)
		}
		size := presets["md"]
		if p.Placement != nil && p.Placement.Size != "" {
			s, ok := presets[p.Placement.Size]
			if !ok {
				return Invalid("size is one of sm, md, lg, xl, full")
			}
			size = s
		}
		w.SizeW, w.SizeH = size[0], size[1]
		if old != nil {
			// A rewrite keeps what the person set and the producer did not.
			w.Revision = old.Revision + 1
			w.Position = old.Position
			if p.Title == "" {
				w.Title = old.Title
			}
			if p.Presentation == "" {
				w.Presentation = old.Presentation
			}
			if p.Height == "" {
				w.Height = old.Height
			}
			if p.Placement == nil || p.Placement.Size == "" {
				w.SizeW, w.SizeH = old.SizeW, old.SizeH
			}
			// A grant is over particular bytes and a particular declaration. The
			// same bytes asking for no more than was granted keep it; anything
			// else asks again.
			if old.Grant == grantGranted && dec != nil && w.Sha == old.GrantedAt && within(dec, old.Declared) {
				w.Grant, w.GrantedAt = grantGranted, old.GrantedAt
			}
		}
		if w.Grant != grantGranted && len(w.Summary) > 0 {
			w.Grant = grantPending
		}

		moved := old == nil || (p.Placement != nil && (p.Placement.Tab != "" || p.Placement.After != ""))
		if old != nil {
			*old = w
		} else {
			b.Widgets = append(b.Widgets, w)
		}
		if moved {
			after := ""
			if p.Placement != nil {
				after = p.Placement.After
			}
			if err := move(&b, widgetAt(&b, p.Name), id, nil, after); err != nil {
				return err
			}
		} else {
			widgetAt(&b, p.Name).Tab = id
		}
		normalize(&b)
		b.Revision++

		if kind == "html" {
			if err := st.Put(c.Context(), docs(k), p.Name, content{HTML: it.HTML}); err != nil {
				var f *Fault
				if errors.As(err, &f) {
					return f
				}
				c.Log().Error("write widget content", "org", c.Org(), "err", err)
				return Unavailable("the widget could not be written")
			}
		}
		if err := save(c, st, k, b); err != nil {
			return err
		}
		// A rewritten widget is a different view: the ticket its last revision
		// carried must not resolve to this one.
		views, err := held(c, st, k)
		if err != nil {
			return err
		}
		delete(views, p.Name)
		if err := keep(c, st, k, views); err != nil {
			return err
		}
		return tickets(c, st, k, &b)
	}); err != nil {
		return nil, err
	}
	Publish(c.Org(), c.Bot(), p.Session, "board.changed",
		changed{Session: p.Session, Revision: b.Revision, Widget: p.Name})

	return struct {
		board
		Resolved string `json:"resolvedWidgetName"`
	}{wire(b), p.Name}, nil
}

// arm is the part of a content arm this cloud keeps.
type arm struct {
	HTML       string
	PluginKind string
	Props      json.RawMessage
}

// unpack reads one arm of BoardWidgetPutContentSchema. Two of the five are
// served here: a document of HTML, and a plugin widget the client draws itself.
// The other three name state this cloud does not hold — an MCP App view, a
// canvas document, a plugin-registered content kind — and are refused rather
// than half-answered.
func unpack(raw json.RawMessage) (string, arm, error) {
	if len(raw) == 0 {
		return "", arm{}, Invalid("content is required")
	}
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", arm{}, Invalid("content: %v", err)
	}
	switch head.Kind {
	case "html":
		var it struct {
			Kind string `json:"kind"`
			HTML string `json:"html"`
		}
		if err := strict(raw, &it); err != nil {
			return "", arm{}, err
		}
		if len(it.HTML) > maxHTML {
			return "", arm{}, Invalid("board widget HTML exceeds %d bytes", maxHTML)
		}
		return "html", arm{HTML: it.HTML}, nil
	case "plugin":
		var it struct {
			Kind       string          `json:"kind"`
			PluginKind string          `json:"pluginKind"`
			Props      json.RawMessage `json:"props"`
		}
		if err := strict(raw, &it); err != nil {
			return "", arm{}, err
		}
		if !pluginRE.MatchString(it.PluginKind) {
			return "", arm{}, Invalid("pluginKind is not a plugin widget kind")
		}
		if len(it.Props) > maxProps {
			return "", arm{}, Invalid("board widget props exceed %d bytes", maxProps)
		}
		return "plugin", arm{PluginKind: it.PluginKind, Props: it.Props}, nil
	case "mcp-app", "canvas-doc", "registered":
		return "", arm{}, Invalid("board widget content kind is unavailable: %s", head.Kind)
	default:
		return "", arm{}, Invalid("board widget content kind is unknown: %s", head.Kind)
	}
}

// normalized checks a declaration and puts it in one order, so the same request
// always reads the same in the grant prompt and compares equal to itself.
func normalized(d *declared) (*declared, error) {
	if d == nil {
		return nil, nil
	}
	if len(d.Origins) > maxOrigins {
		return nil, Invalid("a board widget declares at most %d network origins", maxOrigins)
	}
	if len(d.Tools) > maxTools {
		return nil, Invalid("a board widget declares at most %d tools", maxTools)
	}
	out := declared{}
	for _, raw := range d.Origins {
		o, err := origin(raw)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out.Origins, o) {
			out.Origins = append(out.Origins, o)
		}
	}
	for _, raw := range d.Tools {
		t := strings.TrimSpace(raw)
		if t == "" || t != raw || len(t) > maxTool || strings.ContainsFunc(t, control) {
			return nil, Invalid("invalid board widget tool capability: %s", raw)
		}
		if !slices.Contains(out.Tools, t) {
			out.Tools = append(out.Tools, t)
		}
	}
	slices.Sort(out.Origins)
	slices.Sort(out.Tools)
	if len(out.Origins) == 0 && len(out.Tools) == 0 {
		return nil, nil
	}
	return &out, nil
}

func control(r rune) bool { return r <= 31 || r == 127 }

// origin admits one exact HTTPS origin and nothing else. A wildcard, a path or
// a credential inside what the person is shown would make the prompt a lie
// about what it grants.
func origin(raw string) (string, error) {
	bad := Invalid("a board widget network origin is one exact HTTPS origin: %s", raw)
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 2048 {
		return "", bad
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" ||
		strings.Contains(u.Host, "*") || strings.HasSuffix(u.Hostname(), ".") {
		return "", bad
	}
	return u.Scheme + "://" + u.Host, nil
}

// summary is what the person reads before granting: one line per thing asked
// for, in the declaration's own words.
func summary(d *declared) []string {
	if d == nil {
		return nil
	}
	out := []string{}
	for _, o := range d.Origins {
		out = append(out, "Network access: "+o)
	}
	for _, t := range d.Tools {
		out = append(out, "Tool access: "+t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// within reports whether one declaration asks for no more than another. It is
// what lets a rewritten widget keep its grant: narrowing is free, widening asks
// again.
func within(want, have *declared) bool {
	if want == nil {
		return true
	}
	if have == nil {
		return false
	}
	for _, o := range want.Origins {
		if !slices.Contains(have.Origins, o) {
			return false
		}
	}
	for _, t := range want.Tools {
		if !slices.Contains(have.Tools, t) {
			return false
		}
	}
	return true
}

// ---- board.widget.grant ----

func widgetGrant(c *Call) (any, error) {
	var p struct {
		Session  string `json:"sessionKey"`
		Agent    string `json:"agentId"`
		Name     string `json:"name"`
		Decision string `json:"decision"`
		Revision int    `json:"revision"`
		Instance string `json:"instanceId"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	k, err := locate(p.Session, p.Agent)
	if err != nil {
		return nil, err
	}
	if p.Decision != grantGranted && p.Decision != grantRejected {
		return nil, Invalid("decision is granted or rejected")
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	// Checking what the person is answering about and recording the answer are
	// one act. The revision and instance below say the widget has not moved
	// since the prompt; read outside the act that writes, they say it about a
	// board another call is already replacing, and the approval is stored into
	// a copy that is then thrown away — answered ok, and gone.
	var b board
	if err := st.Do(c.Context(), func(st *Store) error {
		b, err = load(c, st, k, p.Session)
		if err != nil {
			return err
		}
		w := widgetAt(&b, p.Name)
		if w == nil {
			return Invalid("board widget not found: %s", p.Name)
		}
		// The decision is over one revision of one widget. If either moved
		// between the prompt and the answer, the person answered about
		// something else.
		if w.Revision != p.Revision {
			return Invalid("board widget revision changed: %s is revision %d, not %d",
				p.Name, w.Revision, p.Revision)
		}
		if w.Instance != p.Instance {
			return Invalid("board widget instance changed: %s", p.Name)
		}
		if w.Grant != grantPending {
			return Invalid("board widget grant is not pending: %s", p.Name)
		}
		w.Grant = p.Decision
		if p.Decision == grantGranted {
			w.GrantedAt = w.Sha
		}
		b.Revision++
		if err := save(c, st, k, b); err != nil {
			return err
		}
		return tickets(c, st, k, &b)
	}); err != nil {
		return nil, err
	}
	Publish(c.Org(), c.Bot(), p.Session, "board.changed",
		changed{Session: p.Session, Revision: b.Revision})
	return wire(b), nil
}

// ---- board.event ----

// notes is what a board has been told, and what each widget said last. The
// recent entries are the quiet window: a widget repeating itself inside it has
// said one thing, not two.
type notes struct {
	Recent map[string]said `json:"recent,omitempty"`
	Log    []note          `json:"log,omitempty"`
}

type said struct {
	Summary string `json:"summary"`
	At      int64  `json:"at"`
}

type note struct {
	Widget string `json:"widget"`
	Text   string `json:"text"`
	At     int64  `json:"at"`
}

func boardEvent(c *Call) (any, error) {
	var p struct {
		Ticket  string          `json:"ticket"`
		Session string          `json:"sessionKey"`
		Agent   string          `json:"agentId"`
		Widget  string          `json:"widget"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	var (
		st   *Store
		k    string
		name string
		err  error
	)
	if p.Ticket != "" {
		var w widget
		if st, k, w, err = seen(c, p.Ticket); err != nil {
			return nil, err
		}
		name = w.Name
	} else {
		if k, err = locate(p.Session, p.Agent); err != nil {
			return nil, err
		}
		if st, err = c.Store(); err != nil {
			return nil, err
		}
		b, err := load(c, st, k, p.Session)
		if err != nil {
			return nil, err
		}
		if widgetAt(&b, p.Widget) == nil {
			return nil, Invalid("board widget not found: %s", p.Widget)
		}
		name = p.Widget
	}

	// What is recorded is the payload's JSON, bounded, because a report is a
	// line in a log rather than a document.
	body := "null"
	if len(p.Payload) > 0 {
		body = string(p.Payload)
	}
	if len(body) > maxState {
		return nil, Invalid("board event payload exceeds %d bytes", maxState)
	}

	// The notices are one document holding a ring of the last few and what each
	// widget said last, so reading it and appending to it are one act: two
	// widgets reporting at once would otherwise each store the ring they read,
	// and one of the two reports would never have happened.
	repeat := false
	if err := st.Do(c.Context(), func(st *Store) error {
		var kept notes
		if err := st.Get(c.Context(), colNotice, k, &kept); err != nil && !errors.Is(err, ErrNoDoc) {
			c.Log().Error("read board notices", "org", c.Org(), "err", err)
			return Unavailable("the board could not be read")
		}
		if kept.Recent == nil {
			kept.Recent = map[string]said{}
		}
		now := time.Now().UnixMilli()
		if last, ok := kept.Recent[name]; ok && last.Summary == body && now-last.At < quiet.Milliseconds() {
			repeat = true
			return nil
		}
		kept.Recent[name] = said{Summary: body, At: now}
		for w, last := range kept.Recent {
			if now-last.At >= quiet.Milliseconds() {
				delete(kept.Recent, w)
			}
		}
		kept.Log = append(kept.Log, note{Widget: name, Text: line(name, body), At: now})
		if len(kept.Log) > keepNotice {
			kept.Log = kept.Log[len(kept.Log)-keepNotice:]
		}
		if err := st.Put(c.Context(), colNotice, k, kept); err != nil {
			var f *Fault
			if errors.As(err, &f) {
				return f
			}
			c.Log().Error("write board notices", "org", c.Org(), "err", err)
			return Unavailable("the notice could not be recorded")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "appended": !repeat}, nil
}

// line is one notice as the agent reads it, clipped whole so a long report
// cannot crowd out the rest of what it is reading.
func line(name, body string) string {
	const head = "[dashboard] "
	tail := " on widget " + name
	room := max(maxNotice-len(head)-len(tail), 0)
	if r := []rune(body); len(r) > room {
		if room == 0 {
			body = ""
		} else {
			body = string(r[:room-1]) + "…"
		}
	}
	return head + body + tail
}

// ---- board.prompt.authorize ----

func promptAuthorize(c *Call) (any, error) {
	var p struct {
		Ticket string `json:"ticket"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	_, _, w, err := seen(c, p.Ticket)
	if err != nil {
		return nil, err
	}
	// The person confirms every prompt a widget sends, unless this widget said
	// it would send prompts and was granted that.
	return map[string]any{"confirmationRequired": !granted(w, "prompt")}, nil
}

// granted reports whether a widget holds one tool. Declaring and granting are
// read together because either alone means nothing: a tool nobody granted is a
// request, and a grant over a tool no longer declared is stale.
func granted(w widget, tool string) bool {
	return w.Grant == grantGranted && w.Declared != nil && slices.Contains(w.Declared.Tools, tool)
}

// ---- board.data.read and board.action ----

func dataRead(c *Call) (any, error) {
	var p struct {
		Ticket  string          `json:"ticket"`
		Binding string          `json:"bindingId"`
		Params  json.RawMessage `json:"params"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if p.Binding == "" || len(p.Binding) > 64 {
		return nil, Invalid("bindingId is required")
	}
	if err := arguments(p.Params, "data binding"); err != nil {
		return nil, err
	}
	again, err := holds(c, p.Ticket, p.Binding)
	if err != nil {
		return nil, err
	}
	if !bindings[p.Binding] {
		return nil, Invalid("board widget data binding is not allowed: %s", p.Binding)
	}
	out, err := delegate(c, p.Binding, p.Params)
	if err != nil {
		return nil, err
	}
	if err := again(); err != nil {
		return nil, err
	}
	return out, nil
}

func boardAction(c *Call) (any, error) {
	var p struct {
		Ticket string          `json:"ticket"`
		Action string          `json:"action"`
		Job    string          `json:"jobId"`
		Params json.RawMessage `json:"params"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if p.Job != "" {
		// The cron arm: one job, run now. The capability names the job, so a
		// widget granted one job's trigger holds no other.
		if p.Action != "cron.trigger" {
			return nil, Invalid("jobId belongs to the cron.trigger action")
		}
		if len(p.Job) > 256 {
			return nil, Invalid("jobId is at most 256 characters")
		}
		if len(p.Params) > 0 {
			return nil, Invalid("cron.trigger takes no params")
		}
		again, err := holds(c, p.Ticket, "cron.trigger:"+p.Job)
		if err != nil {
			return nil, err
		}
		// The job belongs to whoever owns cron. Say that it is absent rather
		// than report the widget's own call as an unknown method.
		if !known("cron.run") {
			return nil, Unavailable("no cron surface is mounted to run job %s", p.Job)
		}
		params, err := json.Marshal(map[string]string{"id": p.Job, "mode": "force"})
		if err != nil {
			return nil, Invalid("jobId is not a job")
		}
		out, err := delegate(c, "cron.run", params)
		if err != nil {
			return nil, err
		}
		if err := again(); err != nil {
			return nil, err
		}
		return out, nil
	}
	if p.Action == "" || len(p.Action) > maxTool {
		return nil, Invalid("action is required")
	}
	if err := arguments(p.Params, "action"); err != nil {
		return nil, err
	}
	if _, err := holds(c, p.Ticket, p.Action); err != nil {
		return nil, err
	}
	// An action verb is declared by a plugin, together with the shape of its
	// params and the method it runs. This cloud holds no such declaration, so a
	// verb it was never told about is refused rather than dispatched by name.
	return nil, Invalid("board widget action verb is not allowed: %s", p.Action)
}

// holds resolves a ticket and requires the widget to hold one capability. It
// answers with the check to run once the work is done: retained work belongs to
// the widget that asked for it, and a widget rewritten or re-granted in the
// meantime is no longer that widget.
func holds(c *Call, t, capability string) (func() error, error) {
	_, _, w, err := seen(c, t)
	if err != nil {
		return nil, err
	}
	if !granted(w, capability) {
		return nil, Invalid("board widget tool is not granted: %s", capability)
	}
	return func() error {
		_, _, now, err := seen(c, t)
		if err != nil {
			return err
		}
		if now.Revision != w.Revision || now.Instance != w.Instance || !granted(now, capability) {
			return Invalid("board widget changed; reload the dashboard")
		}
		return nil
	}, nil
}

// arguments bounds what a widget may hand a method, and requires it to be the
// object the schema says it is.
func arguments(raw json.RawMessage, what string) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxCall {
		return Invalid("board widget %s params exceed %d bytes", what, maxCall)
	}
	if bytes.TrimSpace(raw)[0] != '{' {
		return Invalid("board widget %s params must be an object", what)
	}
	return nil
}

// delegate runs one ordinary method on a widget's behalf, through the same
// relay every other composition uses. The call carries the same caller and the
// same capabilities: a widget never reaches past the person watching it.
func delegate(c *Call, method string, params json.RawMessage) (any, error) {
	return relay(c, method, params)
}

// ---- wiki.get ----

// A wiki page is one document: a path, a title, a body, and when it last
// changed. The method answers a range of its lines, which is how the memory
// panel previews a page without loading all of it.
//
// Nothing writes these pages yet. A page that is not there answers null, which
// the panel renders as an empty preview, so the method tells the truth in an
// estate with no memory corpus and needs no change once one exists.
type page struct {
	Title   string `json:"title,omitempty"`
	Body    string `json:"body"`
	Updated string `json:"updatedAt,omitempty"`
}

func wikiGet(c *Call) (any, error) {
	var p struct {
		Lookup  string `json:"lookup"`
		From    int    `json:"fromLine"`
		Count   int    `json:"lineCount"`
		Backend string `json:"backend"`
		Corpus  string `json:"corpus"`
		Agent   string `json:"agentId"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	lookup := strings.TrimSpace(p.Lookup)
	if lookup == "" || len(lookup) > 1024 {
		return nil, Invalid("lookup is required")
	}
	if p.From < 0 || p.Count < 0 {
		return nil, Invalid("fromLine and lineCount are positive integers")
	}
	from, count := max(p.From, 1), p.Count
	if count == 0 {
		count = 200
	}
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	var doc page
	if err := st.Get(c.Context(), colWiki, lookup, &doc); errors.Is(err, ErrNoDoc) {
		return nil, nil
	} else if err != nil {
		c.Log().Error("read wiki page", "org", c.Org(), "err", err)
		return nil, Unavailable("the page could not be read")
	}
	all := strings.Split(doc.Body, "\n")
	start := min(from-1, len(all))
	end := min(start+count, len(all))
	title := doc.Title
	if title == "" {
		title = lookup
	}
	return map[string]any{
		"corpus":     "wiki",
		"path":       lookup,
		"title":      title,
		"kind":       "page",
		"content":    strings.Join(all[start:end], "\n"),
		"fromLine":   from,
		"lineCount":  end - start,
		"totalLines": len(all),
		"truncated":  end < len(all),
		"updatedAt":  doc.Updated,
	}, nil
}
