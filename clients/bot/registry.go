package bot

// The registry: which bots this org has, where each runs, what it is allowed
// to drive, whether it is running, parked or gone, and what it has reported.
//
// It lives in the org's own file, as documents, like everything else this
// package keeps — a bot is one document in "bot", and each thing it reported is
// one document in "report:<id>". A bot bound call reads its own file (Call.Bot,
// Call.Store); the registry never does, because the roster is the org's answer
// and not any one bot's.
//
// Isolation is physical, as everywhere else in this cloud: the org chose the
// file, so it appears in no query and no read can forget to filter on it.
//
// Every transition is one act. Parking a bot writes a status, a token and a
// report, and resuming it writes a status and hands the token back; each is a
// read-modify-write, and two of them at once on the same bot would otherwise
// each write over what the other had just read (Store.Do).

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	maxField   = 256   // name, host, model, user, project
	maxURL     = 2048  // where a bot publishes itself
	maxMsg     = 16384 // one report's message
	maxResume  = 65536 // a resume token, opaque and held not read
	maxReports = 500   // the most reports one list returns
)

// colBot holds the roster, one document per bot. reported names the collection
// of one bot's reports, which is dropped whole when that bot is forgotten.
const colBot = "bot"

func reported(id string) string { return "report:" + id }

// wheres is where a bot runs. Local means hardware this cloud does not own:
// it announces outward and only it can stop itself. Cloud means a sandbox this
// cloud placed it in, which is what makes suspend something the cloud may do
// rather than merely record.
var wheres = map[string]bool{"local": true, "cloud": true}

// editions is what the loop is allowed to drive, which is the boundary the
// sandbox is built to.
var editions = map[string]bool{"plain": true, "computer": true, "browser": true}

// statuses is the lifecycle. suspended is a resting state, not an end: a
// suspended bot holds a resume token and is expected back.
var statuses = map[string]bool{
	"starting": true, "running": true, "waiting": true,
	"suspended": true, "ended": true, "error": true,
}

// kinds is what a bot may report.
var kinds = map[string]bool{
	"notification": true, "log": true, "error": true,
	"suspend": true, "resume": true,
}

// Bot is one Hanzo Bot that has announced itself and is expected to keep saying
// so: a loop running somewhere — on a person's laptop, or in a sandbox this
// cloud placed it in — that can be listed, reached, suspended and resumed.
//
// Where says which of the two it is. A local bot runs on hardware the cloud
// does not own, announces outward, and is reachable only at whatever URL it
// publishes. A cloud bot runs in a sandbox this cloud placed it in, so the
// cloud knows where it is and may suspend it. The distinction decides who may
// stop a bot, not what a bot can do.
//
// Edition says what the loop is allowed to drive. A plain bot answers. A
// computer bot has a machine to use, a browser bot has a browser. The word is
// the capability boundary the sandbox is built to, so it is recorded here
// rather than inferred from what a bot happens to call.
//
// Resume carries whatever a suspended bot needs to come back as itself — a
// checkpoint reference, opaque here. The gateway does not read it; it holds it,
// so that a bot suspended on one host can resume on another. It is empty for a
// bot that has never suspended, and it is the one field view withholds.
type Bot struct {
	ID          string `json:"id"`
	Project     string `json:"project,omitempty"`
	User        string `json:"user,omitempty"`
	Name        string `json:"name,omitempty"`
	Where       string `json:"where"`
	Edition     string `json:"edition"`
	Model       string `json:"model,omitempty"`
	Host        string `json:"host,omitempty"`
	URL         string `json:"url,omitempty"`
	Status      string `json:"status"`
	Resume      string `json:"resume,omitempty"`
	StartedAt   int64  `json:"startedAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	SuspendedAt int64  `json:"suspendedAt,omitempty"`
	EndedAt     int64  `json:"endedAt,omitempty"`
}

// Report is something a bot said about itself: a notification it raised, a
// suspension, a resumption, an error. They are kept so a console can say what a
// bot has been doing without holding a connection open to it.
//
// A report is stored as it is sent. Unlike a Bot it holds nothing the org may
// not see, so there is no second shape to keep in step.
type Report struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
	At      int64  `json:"createdAt"`
}

// ---- HTTP shapes (the published contract) ----

type announceReq struct {
	ID      string `json:"id"`      // client-minted; generated if empty
	Name    string `json:"name"`    // what to call it in a list
	Where   string `json:"where"`   // local | cloud
	Edition string `json:"edition"` // plain | computer | browser
	Model   string `json:"model"`
	Host    string `json:"host"`
	Project string `json:"project"`
	User    string `json:"user"`
	URL     string `json:"url"` // set now if the bot is already reachable
}

type updateReq struct {
	Status string `json:"status"`
	URL    string `json:"url"`
	Model  string `json:"model"`
}

type suspendReq struct {
	Resume  string `json:"resume"`  // opaque; held so the bot can come back
	Message string `json:"message"` // why, for the record
}

type resumeReq struct {
	Host string `json:"host"` // where it came back, if it moved
	URL  string `json:"url"`
}

type reportReq struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// view is the wire shape of a Bot. Resume is deliberately absent: it is the
// bot's to hold and the gateway's to keep, and a list of bots is not the place
// to hand it out.
type view struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Where       string `json:"where"`
	Edition     string `json:"edition"`
	Model       string `json:"model,omitempty"`
	Host        string `json:"host,omitempty"`
	Project     string `json:"project,omitempty"`
	User        string `json:"user,omitempty"`
	URL         string `json:"url,omitempty"`
	Status      string `json:"status"`
	StartedAt   int64  `json:"startedAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	SuspendedAt int64  `json:"suspendedAt,omitempty"`
	EndedAt     int64  `json:"endedAt,omitempty"`
}

func viewOf(x Bot) view {
	return view{
		ID: x.ID, Name: x.Name, Where: x.Where, Edition: x.Edition,
		Model: x.Model, Host: x.Host, Project: x.Project, User: x.User,
		URL: x.URL, Status: x.Status,
		StartedAt: x.StartedAt, UpdatedAt: x.UpdatedAt,
		SuspendedAt: x.SuspendedAt, EndedAt: x.EndedAt,
	}
}

// resumeView is what a resume returns: the bot as everyone sees it, plus the
// token, which leaves through this one door and no other.
type resumeView struct {
	Bot    view   `json:"bot"`
	Resume string `json:"resume,omitempty"`
}

// ---- reading and writing the roster ----

// readBot reads one bot of this org. It answers ErrNoDoc for an id this org
// never announced, which every caller reads as "no such bot" — a 404 to a
// console, a refusal to a protocol call that tried to bind to it.
func readBot(ctx context.Context, st *Store, id string) (Bot, error) {
	var x Bot
	err := st.Get(ctx, colBot, id, &x)
	return x, err
}

// bots reads the whole roster, newest announcement first.
func bots(ctx context.Context, st *Store) ([]Bot, error) {
	docs, err := st.List(ctx, colBot, 0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Bot, 0, len(docs))
	for _, d := range docs {
		var x Bot
		if err := json.Unmarshal(d.Doc, &x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	slices.SortStableFunc(out, func(a, b Bot) int { return cmp.Compare(b.StartedAt, a.StartedAt) })
	return out, nil
}

// matches reports whether x answers the narrowing a list asked for. An absent
// parameter does not constrain. live selects what has neither stopped nor
// parked — the console's default question, "what is running right now".
func matches(c *zip.Ctx, x Bot) bool {
	for _, want := range [][2]string{
		{"status", x.Status}, {"where", x.Where}, {"edition", x.Edition},
		{"host", x.Host}, {"project", x.Project},
	} {
		if q := clip(c.Query(want[0]), maxField); q != "" && q != want[1] {
			return false
		}
	}
	if c.Query("live") != "" {
		switch x.Status {
		case "ended", "error", "suspended":
			return false
		}
	}
	return true
}

func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// storeFor opens one SQLite for a caller that has no *Call: the org's own file,
// where the roster lives, when bot is empty, and a bot's own file when it is
// not. It is Call.Store's counterpart on the REST half, and the only difference
// between the two is the shape of the refusal — a Fault there, an HTTP error
// here — so the failure is logged and answered in one place either way. What
// went wrong opening a file is this deployment's business; the caller is told
// that it did.
func storeFor(s *cloud.Service[state], org, bot string) (*Store, error) {
	st, err := s.State.stores.For(org, bot)
	if err != nil {
		s.Log.Error("open bot store", "org", org, "bot", bot, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "open store")
	}
	return st, nil
}

func idParam(c *zip.Ctx) string { return strings.TrimSpace(c.Param("id")) }

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// missing turns an absent document into the answer a console reads.
func missing(err error) error {
	if errors.Is(err, ErrNoDoc) {
		return zip.ErrNotFound("bot not found")
	}
	return err
}

// ---- handlers ----

func announce(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req announceReq
	if err := c.Bind(&req); err != nil {
		return err
	}

	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = mint("bot")
	} else if !idRE.MatchString(id) {
		return zip.ErrBadRequest("bad id")
	}
	where := strings.TrimSpace(req.Where)
	if !wheres[where] {
		return zip.ErrBadRequest("where must be local or cloud")
	}
	edition := strings.TrimSpace(req.Edition)
	if edition == "" {
		edition = "plain"
	}
	if !editions[edition] {
		return zip.ErrBadRequest("edition must be plain, computer or browser")
	}

	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	x := Bot{
		ID:      id,
		Project: clip(req.Project, maxField),
		User:    clip(req.User, maxField),
		Name:    clip(req.Name, maxField),
		Where:   where, Edition: edition,
		Model:     clip(req.Model, maxField),
		Host:      clip(req.Host, maxField),
		URL:       clip(req.URL, maxURL),
		Status:    "starting",
		StartedAt: now, UpdatedAt: now,
	}
	// Taking an id that is already in use would erase the bot that holds it,
	// along with the token that brings it back, so the check and the write are
	// one act.
	err = st.Do(c.Context(), func(st *Store) error {
		if _, err := readBot(c.Context(), st, id); err == nil {
			return zip.Errorf(http.StatusConflict, "that id names a bot already")
		} else if !errors.Is(err, ErrNoDoc) {
			return err
		}
		return st.Put(c.Context(), colBot, id, x)
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, viewOf(x))
}

func list(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	xs, err := bots(c.Context(), st)
	if err != nil {
		return err
	}
	out := make([]view, 0, len(xs))
	for _, x := range xs {
		if matches(c, x) {
			out = append(out, viewOf(x))
		}
	}
	return c.JSON(http.StatusOK, out)
}

func get(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	x, err := readBot(c.Context(), st, idParam(c))
	if err != nil {
		return missing(err)
	}
	return c.JSON(http.StatusOK, viewOf(x))
}

// update is the heartbeat. A bot that says nothing but its own name still moves
// updatedAt, which is how "live" is answered without holding a connection open.
func update(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req updateReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	status := strings.TrimSpace(req.Status)
	if status != "" && !statuses[status] {
		return zip.ErrBadRequest("unknown status")
	}
	// Suspension and resumption are their own doors, because each has to write
	// more than a status: a token in one direction, a clearing in the other.
	if status == "suspended" {
		return zip.ErrBadRequest("use POST /v1/bot/:id/suspend")
	}

	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	var x Bot
	err = st.Do(c.Context(), func(st *Store) error {
		id := idParam(c)
		cur, err := readBot(c.Context(), st, id)
		if err != nil {
			return missing(err)
		}
		now := time.Now().UnixMilli()
		// An empty field leaves what is there alone, so a heartbeat carrying
		// only a status does not blank the URL a bot published earlier.
		cur.Status = pick(status, cur.Status)
		cur.URL = pick(clip(req.URL, maxURL), cur.URL)
		cur.Model = pick(clip(req.Model, maxField), cur.Model)
		cur.UpdatedAt = now
		if status == "ended" || status == "error" {
			cur.EndedAt = now
		}
		x = cur
		return st.Put(c.Context(), colBot, id, cur)
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, viewOf(x))
}

// pick is "what was sent, or what was already there".
func pick(sent, held string) string {
	if sent == "" {
		return held
	}
	return sent
}

func forget(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	id := idParam(c)
	if _, err := readBot(c.Context(), st, id); err != nil {
		return missing(err)
	}
	// A bot has four homes and the row is only one of them: a file of its own,
	// coordinates in KMS, the turns running for it and the sockets bound to it.
	// All of that goes first (empty) and the row goes last, because the row is
	// what makes an id announceable again — a bot that took the id while the
	// last one was still being cleared would have its own state cleared
	// instead. A step that fails answers a failure: a 204 over state still in
	// place is how a forgotten bot comes back as somebody else's.
	if err := empty(c.Context(), s, o, id); err != nil {
		return err
	}
	// A bot and everything it reported go together: a report of a bot nobody
	// has is a row no console can ask about and nothing will ever delete.
	err = st.Do(c.Context(), func(st *Store) error {
		if err := st.Delete(c.Context(), colBot, id); err != nil {
			return err
		}
		return st.Drop(c.Context(), reported(id))
	})
	if err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// empty disposes of everything one bot holds, leaving its partition as it was
// before the bot announced itself.
//
// The turns stop first: a turn writes into the file this is about to empty and
// publishes onto the connections it is about to end. The sockets go next, for
// the same reason and one of its own — a socket resolves its bot once, at the
// upgrade, so a client that bound to this bot goes on reading and writing its
// file until the socket ends. The credentials go before the file, because the
// file is the only record of which coordinates were sealed. The file is emptied
// rather than removed: the handle stays good, so nothing has to notice.
func empty(ctx context.Context, s *cloud.Service[state], org, bot string) error {
	chatHaltBot(org, bot)
	s.State.hub.closeBot(org, bot, "bot forgotten")
	st, err := storeFor(s, org, bot)
	if err != nil {
		return err
	}
	if err := dropSecrets(ctx, s, org, bot, st); err != nil {
		return err
	}
	return st.Empty(ctx)
}

// suspend parks a bot. The resume token is whatever the bot needs to come back
// as itself; the gateway holds it without reading it, so that what a bot
// checkpoints is the bot's business and not this package's.
func suspend(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req suspendReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	var x Bot
	err = st.Do(c.Context(), func(st *Store) error {
		id := idParam(c)
		cur, err := readBot(c.Context(), st, id)
		if err != nil {
			return missing(err)
		}
		if cur.Status == "ended" || cur.Status == "error" {
			return zip.Errorf(http.StatusConflict, "bot has stopped")
		}
		now := time.Now().UnixMilli()
		cur.Status = "suspended"
		cur.Resume = clip(req.Resume, maxResume)
		cur.SuspendedAt, cur.UpdatedAt = now, now
		x = cur
		if err := st.Put(c.Context(), colBot, id, cur); err != nil {
			return err
		}
		r := Report{ID: mint("report"), Kind: "suspend", Message: clip(req.Message, maxMsg), At: now}
		return st.Put(c.Context(), reported(id), r.ID, r)
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, viewOf(x))
}

// resume hands a suspended bot back its token and marks it running. The token
// leaves through this one door and no other, which is why it is not on view.
func resume(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req resumeReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	var x Bot
	var held string
	err = st.Do(c.Context(), func(st *Store) error {
		id := idParam(c)
		cur, err := readBot(c.Context(), st, id)
		if err != nil {
			return missing(err)
		}
		if cur.Status != "suspended" {
			return zip.Errorf(http.StatusConflict, "bot is not suspended")
		}
		now := time.Now().UnixMilli()
		held = cur.Resume
		cur.Status = "running"
		cur.Host = pick(clip(req.Host, maxField), cur.Host)
		cur.URL = pick(clip(req.URL, maxURL), cur.URL)
		cur.UpdatedAt = now
		x = cur
		if err := st.Put(c.Context(), colBot, id, cur); err != nil {
			return err
		}
		r := Report{ID: mint("report"), Kind: "resume", At: now}
		return st.Put(c.Context(), reported(id), r.ID, r)
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, resumeView{Bot: viewOf(x), Resume: held})
}

func record(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req reportReq
	if err := c.Bind(&req); err != nil {
		return err
	}
	kind := strings.TrimSpace(req.Kind)
	if !kinds[kind] {
		return zip.ErrBadRequest("unknown kind")
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	r := Report{
		ID: mint("report"), Kind: kind,
		Message: clip(req.Message, maxMsg), At: time.Now().UnixMilli(),
	}
	// A report of a bot this org does not have is a row nothing would ever
	// read, so the bot is checked and the report written as one act.
	err = st.Do(c.Context(), func(st *Store) error {
		id := idParam(c)
		if _, err := readBot(c.Context(), st, id); err != nil {
			return missing(err)
		}
		return st.Put(c.Context(), reported(id), r.ID, r)
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, r)
}

func reports(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	limit := maxReports
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 && n < maxReports {
		limit = n
	}
	// Newest first: a report is written once and never edited, so the document
	// order List reads in is the order they were made.
	docs, err := st.List(c.Context(), reported(idParam(c)), limit, 0)
	if err != nil {
		return err
	}
	out := make([]Report, 0, len(docs))
	for _, d := range docs {
		var r Report
		if err := json.Unmarshal(d.Doc, &r); err != nil {
			return err
		}
		out = append(out, r)
	}
	return c.JSON(http.StatusOK, out)
}
