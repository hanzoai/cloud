package bot

// The registry: which runs this org has, where each one is, what it drives,
// whether it is going, parked or stopped, and what it has reported.
//
// A run is a bot instance — a loop doing somebody's work. It begins either on
// their own machine, in which case that machine tells this cloud about it, or
// in a sandbox something placed for them, in which case whatever placed it
// knows and this asks (cloud.Deps.Runs). One roster answers for both, because
// "my bots" is one question however the machine underneath was found.
//
// It lives in the org's own file, as documents, like everything else this
// package keeps — a run is one document in "bot", and each thing it reported is
// one document in "report:<id>". A bound protocol call reads its own file
// (Call.Bot, Call.Store); the registry never does, because the roster is the
// org's answer and not any one run's.
//
// Isolation is physical, as everywhere else in this cloud: the org chose the
// file, so it appears in no query and no read can forget to filter on it.
//
// Every transition is one act. Parking a run writes a status, a token and a
// report, and resuming it writes a status and hands the token back; each is a
// read-modify-write, and two of them at once on the same run would otherwise
// each write over what the other had just read (Store.Do).

import (
	"bytes"
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
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

const (
	maxField   = 256   // name, host, model, user, project, surface
	maxTask    = 4096  // the instruction a run is carrying out
	maxURL     = 2048  // where a run publishes its live session
	maxMsg     = 16384 // one report's message
	maxResume  = 65536 // a resume token, opaque and held not read
	maxReports = 500   // the most reports one list returns
)

// colRun holds the roster, one document per run. reported names the collection
// of one run's reports, which is dropped whole when that run is forgotten.
//
// Both strings are where the rows are rather than what they are called: they
// address documents already written, so they do not follow the vocabulary.
const colRun = "bot"

func reported(id string) string { return "report:" + id }

// wheres is where a run is. Local means hardware this cloud does not own: it
// announces outward and only it can stop itself. Cloud means a sandbox
// something placed for it, which is what makes stopping something this cloud
// may do rather than merely record.
var wheres = map[string]bool{"local": true, "cloud": true}

// surfaces is what the loop is allowed to drive, which is the boundary the
// sandbox is built to.
var surfaces = map[string]bool{"plain": true, "computer": true, "browser": true}

// statuses is the lifecycle. suspended is a resting state, not an end: a
// suspended run holds a resume token and is expected back.
//
// stopped and error are the two ends, and they are not two ways to stop. A run
// is stopped by POST /v1/bot/runs/:id/stop and by nothing else; error is the
// run's own last word about itself, which only it can send and which stops
// nothing that was still going.
var statuses = map[string]bool{
	"starting": true, "running": true, "waiting": true,
	"suspended": true, "stopped": true, "error": true,
}

// over reports a run that has ended. Suspension is not over — that is the
// point of it.
func over(status string) bool { return status == "stopped" || status == "error" }

// kinds is what a run may report.
var kinds = map[string]bool{
	"notification": true, "log": true, "error": true,
	"suspend": true, "resume": true, "stop": true,
}

// Run is one bot instance this org has: a loop running somewhere — on a
// person's laptop, or in a sandbox placed for them — that can be listed,
// reached, suspended, resumed and stopped.
//
// Where says which of the two it is. A local run is on hardware this cloud does
// not own, announces outward, and is reachable only at whatever session URL it
// publishes. A cloud run is in a sandbox something placed, so where it is is
// known and it can be stopped from here. The distinction decides who may stop a
// run, not what a run can do.
//
// Surface says what the loop drives. A plain run answers. A computer run has a
// machine to use, a browser run has a browser. The word is the capability
// boundary the sandbox is built to, so it is recorded here rather than inferred
// from what a run happens to call.
//
// Resume carries whatever a suspended run needs to come back as itself — a
// checkpoint reference, opaque here. The gateway does not read it; it holds it,
// so that a run suspended on one machine can come back on another. It is empty
// for a run that has never suspended, and it is the one field view withholds.
type Run struct {
	ID          string `json:"id"`
	Project     string `json:"project,omitempty"`
	User        string `json:"user,omitempty"`
	Name        string `json:"name,omitempty"`
	Task        string `json:"task,omitempty"`
	Where       string `json:"where"`
	Surface     string `json:"surface"`
	Model       string `json:"model,omitempty"`
	Host        string `json:"host,omitempty"`
	SessionURL  string `json:"sessionUrl,omitempty"`
	Status      string `json:"status"`
	Resume      string `json:"resume,omitempty"`
	StartedAt   int64  `json:"startedAt"`
	UpdatedAt   int64  `json:"updatedAt"`
	SuspendedAt int64  `json:"suspendedAt,omitempty"`
	StoppedAt   int64  `json:"stoppedAt,omitempty"`
}

// Report is something a run said about itself: a notification it raised, a
// suspension, a resumption, a stop, an error. They are kept so a console can
// say what a run has been doing without holding a connection open to it, and
// they outlive the run — a stopped run's account of itself is most of what
// anyone wants afterwards.
//
// A report is stored as it is sent. Unlike a Run it holds nothing the org may
// not see, so there is no second shape to keep in step.
type Report struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
	At      int64  `json:"createdAt"`
}

// ---- HTTP shapes (the published contract) ----

type beginReq struct {
	RunID      string `json:"runId"`   // client-minted; generated if empty
	Name       string `json:"name"`    // what to call it in a list
	Task       string `json:"task"`    // the instruction it is carrying out
	Where      string `json:"where"`   // local | cloud
	Surface    string `json:"surface"` // plain | computer | browser
	Model      string `json:"model"`
	Host       string `json:"host"`
	Project    string `json:"project"`
	User       string `json:"user"`
	SessionURL string `json:"sessionUrl"` // set now if the run is already reachable
}

type updateReq struct {
	Status     string `json:"status"`
	SessionURL string `json:"sessionUrl"`
	Model      string `json:"model"`
}

type suspendReq struct {
	Resume  string `json:"resume"`  // opaque; held so the run can come back
	Message string `json:"message"` // why, for the record
}

type resumeReq struct {
	Host       string `json:"host"` // where it came back, if it moved
	SessionURL string `json:"sessionUrl"`
}

type stopReq struct {
	Message string `json:"message"` // why, for the record
}

type reportReq struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// view is the wire shape of a run, and the field names are the ones the
// published surface already uses (plugin/bot/openapi.json: BotRun) — runId,
// task, surface, status, sessionUrl and an RFC 3339 startedAt. The rest are
// this registry's own and are additions to that shape, never renamings of it.
//
// Resume is deliberately absent: it is the run's to hold and the gateway's to
// keep, and a list of runs is not the place to hand it out.
type view struct {
	RunID       string `json:"runId"`
	Name        string `json:"name,omitempty"`
	Task        string `json:"task,omitempty"`
	Where       string `json:"where"`
	Surface     string `json:"surface"`
	Model       string `json:"model,omitempty"`
	Host        string `json:"host,omitempty"`
	Project     string `json:"project,omitempty"`
	User        string `json:"user,omitempty"`
	SessionURL  string `json:"sessionUrl,omitempty"`
	Status      string `json:"status"`
	StartedAt   string `json:"startedAt"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
	SuspendedAt string `json:"suspendedAt,omitempty"`
	StoppedAt   string `json:"stoppedAt,omitempty"`
}

func viewOf(x Run) view {
	return view{
		RunID: x.ID, Name: x.Name, Task: x.Task, Where: x.Where, Surface: x.Surface,
		Model: x.Model, Host: x.Host, Project: x.Project, User: x.User,
		SessionURL: x.SessionURL, Status: x.Status,
		StartedAt: stamp(x.StartedAt), UpdatedAt: stamp(x.UpdatedAt),
		SuspendedAt: stamp(x.SuspendedAt), StoppedAt: stamp(x.StoppedAt),
	}
}

// runs is what a list answers. The key is "bots" because that is the key the
// published surface answers with (BotRuns), and a generated client reads it by
// name.
type runs struct {
	Bots []view `json:"bots"`
}

// stopped is what a stop answers (BotStopped). Status is the run's terminal
// state, which is "stopped" for every run this cloud stops and stays "error"
// for one that had already failed on its own.
type stopped struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
}

// resumeView is what a resume returns: the run as everyone sees it, plus the
// token, which leaves through this one door and no other.
type resumeView struct {
	Run    view   `json:"run"`
	Resume string `json:"resume,omitempty"`
}

// stamp renders an instant as RFC 3339, which is how every time on this surface
// is spelled: the published shape says startedAt is RFC 3339, and one surface
// does not spell a time two ways. Zero is absent.
func stamp(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// began reads a stamp back for ordering. One that cannot be read sorts last,
// which is where a run nobody can date belongs.
func began(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// ---- reading and writing the roster ----

// readRun reads one run of this org. It answers ErrNoDoc for an id this org
// never announced, which every caller reads as "no such run" — a 404 to a
// console, a refusal to a protocol call that tried to bind to it.
func readRun(ctx context.Context, st *Store, id string) (Run, error) {
	var x Run
	err := st.Get(ctx, colRun, id, &x)
	return x, err
}

// ours reads the runs this registry holds, newest first: the ones that
// announced themselves here. Its counterpart is placed.
func ours(ctx context.Context, st *Store) ([]Run, error) {
	docs, err := st.List(ctx, colRun, 0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(docs))
	for _, d := range docs {
		var x Run
		if err := json.Unmarshal(d.Doc, &x); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	slices.SortStableFunc(out, func(a, b Run) int { return cmp.Compare(b.StartedAt, a.StartedAt) })
	return out, nil
}

// placed reads the runs the executor holds and puts them in the same shape.
// They are cloud runs by definition — something placed them, which is what the
// word means here — and this cloud adds nothing else to what the executor said.
//
// An executor that cannot answer is a failure and not an empty list: a roster
// that quietly dropped half of itself would tell a console this org has fewer
// runs than it has, which is a different claim from "we could not ask".
func placed(ctx context.Context, s *cloud.Service[state], org string) ([]view, error) {
	if s.State.runs == nil {
		return nil, nil
	}
	out, err := s.State.runs.Runs(ctx, org)
	if err != nil {
		s.Log.Error("read placed runs", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "the run executor did not answer")
	}
	rows := make([]view, 0, len(out))
	for _, r := range out {
		rows = append(rows, view{
			RunID: r.ID, Task: r.Task, Surface: r.Surface, Where: "cloud",
			Status: pick(r.Status, "running"), SessionURL: r.SessionURL,
			StartedAt: r.StartedAt,
		})
	}
	return rows, nil
}

// matches reports whether x answers the narrowing a list asked for. An absent
// parameter does not constrain. live selects what has neither ended nor parked
// — the console's default question, "what is going right now".
func matches(c *zip.Ctx, x view) bool {
	for _, want := range [][2]string{
		{"status", x.Status}, {"where", x.Where}, {"surface", x.Surface},
		{"host", x.Host}, {"project", x.Project},
	} {
		if q := clip(c.Query(want[0]), maxField); q != "" && q != want[1] {
			return false
		}
	}
	if c.Query("live") != "" && (over(x.Status) || x.Status == "suspended") {
		return false
	}
	return true
}

func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// storeFor opens one SQLite for a caller that has no *Call: the org's own file,
// where the roster lives, when run is empty, and a run's own file when it is
// not. It is Call.Store's counterpart on the REST half, and the only difference
// between the two is the shape of the refusal — a Fault there, an HTTP error
// here — so the failure is logged and answered in one place either way. What
// went wrong opening a file is this deployment's business; the caller is told
// that it did.
func storeFor(s *cloud.Service[state], org, run string) (*Store, error) {
	st, err := s.State.stores.For(org, run)
	if err != nil {
		s.Log.Error("open bot store", "org", org, "run", run, "err", err)
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

// bind reads the request body into v. Every field on every request here is
// optional, so a caller with nothing to add sends nothing, and no body is a
// body with no fields: the published stop takes no input at all, and its
// generated client sends none. It is also how the protocol door reads a frame,
// so a body is decoded one way on this surface and not two.
func bind(c *zip.Ctx, v any) error {
	raw := bytes.TrimSpace(c.Body())
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return zip.ErrBadRequest("body is not an object")
	}
	return nil
}

// missing turns an absent document into the answer a console reads.
func missing(err error) error {
	if errors.Is(err, ErrNoDoc) {
		return zip.ErrNotFound("run not found")
	}
	return err
}

// ---- handlers ----

// begin creates a run. A run that has already started says where it is and this
// records it — which is what a machine does for the loop it just started, and
// what a sandbox does once it is up. A caller that describes no run is instead
// asking this cloud to place one, and this cloud places none.
func begin(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}

	var fields map[string]json.RawMessage
	if err := bind(c, &fields); err != nil {
		return err
	}
	if placing(fields) {
		return unplaceable()
	}
	var req beginReq
	if err := bind(c, &req); err != nil {
		return err
	}

	id := strings.TrimSpace(req.RunID)
	if id == "" {
		id = mint("run")
	} else if !idRE.MatchString(id) {
		return zip.ErrBadRequest("bad runId")
	}
	where := strings.TrimSpace(req.Where)
	if !wheres[where] {
		return zip.ErrBadRequest("where must be local or cloud")
	}
	surface := strings.TrimSpace(req.Surface)
	if surface == "" {
		surface = "plain"
	}
	if !surfaces[surface] {
		return zip.ErrBadRequest("surface must be plain, computer or browser")
	}

	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	x := Run{
		ID:      id,
		Project: clip(req.Project, maxField),
		User:    clip(req.User, maxField),
		Name:    clip(req.Name, maxField),
		Task:    clip(req.Task, maxTask),
		Where:   where, Surface: surface,
		Model:      clip(req.Model, maxField),
		Host:       clip(req.Host, maxField),
		SessionURL: clip(req.SessionURL, maxURL),
		Status:     "starting",
		StartedAt:  now, UpdatedAt: now,
	}
	// Taking an id that is already in use would erase the run that holds it,
	// along with the token that brings it back, so the check and the write are
	// one act.
	err = st.Do(c.Context(), func(st *Store) error {
		if _, err := readRun(c.Context(), st, id); err == nil {
			return zip.Errorf(http.StatusConflict, "that runId names a run already")
		} else if !errors.Is(err, ErrNoDoc) {
			return err
		}
		return st.Put(c.Context(), colRun, id, x)
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, viewOf(x))
}

// placing reports whether the caller asked this cloud to place a run rather
// than describing one it already has. A run that exists says where it is; a
// request that names no place — nothing at all, or only the task to carry out
// and the surface to carry it out on — is asking for a machine, and finding one
// is the part that is missing.
func placing(fields map[string]json.RawMessage) bool {
	for k := range fields {
		if k != "task" && k != "surface" {
			return false
		}
	}
	return true
}

// unplaceable is the refusal to start a run, and it is total: no id is minted,
// no session is handed back and nothing is charged. Naming what is absent is
// the whole value of it — a plausible answer here would be a run that does not
// exist, pointed at a machine that was never found.
func unplaceable() error {
	return zip.Errorf(http.StatusNotImplemented,
		"nothing here places a run: this cloud hosts no sandbox, and an executor it is given (cloud.Deps.Runs) lists and stops runs but starts none. "+
			`A run that has already started announces itself instead: POST /v1/bot/runs {"where":"local", ...}.`)
}

// list is the roster: the runs that announced themselves here and the runs the
// executor holds, in one answer, newest first.
func list(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	xs, err := ours(c.Context(), st)
	if err != nil {
		return err
	}
	out := make([]view, 0, len(xs))
	for _, x := range xs {
		if v := viewOf(x); matches(c, v) {
			out = append(out, v)
		}
	}
	rows, err := placed(c.Context(), s, o)
	if err != nil {
		return err
	}
	for _, v := range rows {
		if matches(c, v) {
			out = append(out, v)
		}
	}
	slices.SortStableFunc(out, func(a, b view) int { return began(b.StartedAt).Compare(began(a.StartedAt)) })
	return c.JSON(http.StatusOK, runs{Bots: out})
}

// get reads one run: this registry's own, or one the executor holds. The
// executor serves no read of a single run, so that half is answered out of the
// roster it does serve — one run of a list is still what the list said.
func get(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	id := idParam(c)
	switch x, err := readRun(c.Context(), st, id); {
	case err == nil:
		return c.JSON(http.StatusOK, viewOf(x))
	case !errors.Is(err, ErrNoDoc):
		return err
	}
	rows, err := placed(c.Context(), s, o)
	if err != nil {
		return err
	}
	for _, v := range rows {
		if v.RunID == id {
			return c.JSON(http.StatusOK, v)
		}
	}
	return zip.ErrNotFound("run not found")
}

// update is the heartbeat. A run that says nothing but its own name still moves
// updatedAt, which is how "live" is answered without holding a connection open.
func update(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req updateReq
	if err := bind(c, &req); err != nil {
		return err
	}
	status := strings.TrimSpace(req.Status)
	if status != "" && !statuses[status] {
		return zip.ErrBadRequest("unknown status")
	}
	// Parking a run and stopping one are their own doors, because each has to
	// write more than a status: a token in one direction, a clearing in the
	// other, and the end of the work in flight in the third.
	switch status {
	case "suspended":
		return zip.ErrBadRequest("use POST /v1/bot/runs/:id/suspend")
	case "stopped":
		return zip.ErrBadRequest("use POST /v1/bot/runs/:id/stop")
	}

	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	var x Run
	err = st.Do(c.Context(), func(st *Store) error {
		id := idParam(c)
		cur, err := readRun(c.Context(), st, id)
		if err != nil {
			return missing(err)
		}
		now := time.Now().UnixMilli()
		// An empty field leaves what is there alone, so a heartbeat carrying
		// only a status does not blank the session URL a run published earlier.
		cur.Status = pick(status, cur.Status)
		cur.SessionURL = pick(clip(req.SessionURL, maxURL), cur.SessionURL)
		cur.Model = pick(clip(req.Model, maxField), cur.Model)
		cur.UpdatedAt = now
		if status == "error" {
			cur.StoppedAt = now
		}
		x = cur
		return st.Put(c.Context(), colRun, id, cur)
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

// stop ends a run. It is the only way to end one: the heartbeat refuses the
// status and names this door, suspension is a rest and not an end, and forget
// disposes of a run through the same halt rather than beside it.
//
// A run this registry holds is ended here. A run the executor holds is ended
// there, and only what the executor says is reported: answering "stopped"
// because a request was sent would be a stop that cannot fail.
func stop(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req stopReq
	if err := bind(c, &req); err != nil {
		return err
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	id := idParam(c)
	var x Run
	err = st.Do(c.Context(), func(st *Store) error {
		cur, err := readRun(c.Context(), st, id)
		if err != nil {
			return err
		}
		// A run that has already ended keeps the end it had: stopping a second
		// time changes nothing, and a run that failed should still read as
		// failed afterwards.
		if !over(cur.Status) {
			now := time.Now().UnixMilli()
			cur.Status, cur.StoppedAt, cur.UpdatedAt = "stopped", now, now
			if err := st.Put(c.Context(), colRun, id, cur); err != nil {
				return err
			}
			r := Report{ID: mint("report"), Kind: "stop", Message: clip(req.Message, maxMsg), At: now}
			if err := st.Put(c.Context(), reported(id), r.ID, r); err != nil {
				return err
			}
		}
		x = cur
		return nil
	})
	switch {
	case err == nil:
		halt(o, id)
		return c.JSON(http.StatusOK, stopped{RunID: id, Status: x.Status})
	case !errors.Is(err, ErrNoDoc):
		return err
	}
	return stopPlaced(s, c, o, id)
}

// stopPlaced ends a run the executor holds. Absence is honoured only when the
// executor says so; an executor that did not answer reports nothing about the
// run, and a run this cloud cannot reach is not a run it may call stopped.
//
// An id neither half holds is absent, which is the same answer an id belonging
// to another tenant gets — the org scopes the lookup, so this address tells
// nobody which ids anyone else holds.
func stopPlaced(s *cloud.Service[state], c *zip.Ctx, org, id string) error {
	if s.State.runs == nil {
		return zip.ErrNotFound("run not found")
	}
	switch err := s.State.runs.Stop(c.Context(), org, id); {
	case err == nil:
		return c.JSON(http.StatusOK, stopped{RunID: id, Status: "stopped"})
	case errors.Is(err, types.ErrNoRun):
		return zip.ErrNotFound("run not found")
	default:
		s.Log.Error("stop placed run", "org", org, "run", id, "err", err)
		return zip.Errorf(http.StatusBadGateway, "the run executor did not answer")
	}
}

// halt ends the work a run had in flight. It is the second half of stopping —
// the status is the record, this is the work — and forget reaches it through
// empty, so a run's work never ends two ways.
func halt(org, run string) { chatHaltBot(org, run) }

// forget removes a run and everything it holds. It is not a second stop: what
// it ends, it ends through the same halt, and what it adds is disposal — the
// row is gone afterwards and the id can be taken again, which stopping alone
// never does.
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
	if _, err := readRun(c.Context(), st, id); err != nil {
		return missing(err)
	}
	// A run has four homes and the row is only one of them: a file of its own,
	// coordinates in KMS, the turns going for it and the sockets bound to it.
	// All of that goes first (empty) and the row goes last, because the row is
	// what makes an id announceable again — a run that took the id while the
	// last one was still being cleared would have its own state cleared
	// instead. A step that fails answers a failure: a 204 over state still in
	// place is how a forgotten run comes back as somebody else's.
	if err := empty(c.Context(), s, o, id); err != nil {
		return err
	}
	// A run and everything it reported go together: a report of a run nobody
	// has is a row no console can ask about and nothing will ever delete.
	err = st.Do(c.Context(), func(st *Store) error {
		if err := st.Delete(c.Context(), colRun, id); err != nil {
			return err
		}
		return st.Drop(c.Context(), reported(id))
	})
	if err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// empty disposes of everything one run holds, leaving its partition as it was
// before the run announced itself.
//
// The turns stop first: a turn writes into the file this is about to empty and
// publishes onto the connections it is about to end. The sockets go next, for
// the same reason and one of its own — a socket resolves its run once, at the
// upgrade, so a client that bound to this run goes on reading and writing its
// file until the socket ends. The credentials go before the file, because the
// file is the only record of which coordinates were sealed. The file is emptied
// rather than removed: the handle stays good, so nothing has to notice.
func empty(ctx context.Context, s *cloud.Service[state], org, run string) error {
	halt(org, run)
	s.State.hub.closeBot(org, run, "run forgotten")
	st, err := storeFor(s, org, run)
	if err != nil {
		return err
	}
	if err := dropSecrets(ctx, s, org, run, st); err != nil {
		return err
	}
	return st.Empty(ctx)
}

// suspend parks a run. The resume token is whatever the run needs to come back
// as itself; the gateway holds it without reading it, so that what a run
// checkpoints is the run's business and not this package's.
//
// It is not a stop and never becomes one: a suspended run is expected back, and
// what it is holding is the reason it can come back somewhere else — a laptop
// that closes and a sandbox that opens are the same run either side of this.
func suspend(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req suspendReq
	if err := bind(c, &req); err != nil {
		return err
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	var x Run
	err = st.Do(c.Context(), func(st *Store) error {
		id := idParam(c)
		cur, err := readRun(c.Context(), st, id)
		if err != nil {
			return missing(err)
		}
		if over(cur.Status) {
			return zip.Errorf(http.StatusConflict, "run has stopped")
		}
		now := time.Now().UnixMilli()
		cur.Status = "suspended"
		cur.Resume = clip(req.Resume, maxResume)
		cur.SuspendedAt, cur.UpdatedAt = now, now
		x = cur
		if err := st.Put(c.Context(), colRun, id, cur); err != nil {
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

// resume hands a suspended run back its token and marks it going. The token
// leaves through this one door and no other, which is why it is not on view.
func resume(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req resumeReq
	if err := bind(c, &req); err != nil {
		return err
	}
	st, err := storeFor(s, o, "")
	if err != nil {
		return err
	}
	var x Run
	var token string
	err = st.Do(c.Context(), func(st *Store) error {
		id := idParam(c)
		cur, err := readRun(c.Context(), st, id)
		if err != nil {
			return missing(err)
		}
		if cur.Status != "suspended" {
			return zip.Errorf(http.StatusConflict, "run is not suspended")
		}
		now := time.Now().UnixMilli()
		token = cur.Resume
		cur.Status = "running"
		cur.Host = pick(clip(req.Host, maxField), cur.Host)
		cur.SessionURL = pick(clip(req.SessionURL, maxURL), cur.SessionURL)
		cur.UpdatedAt = now
		x = cur
		if err := st.Put(c.Context(), colRun, id, cur); err != nil {
			return err
		}
		r := Report{ID: mint("report"), Kind: "resume", At: now}
		return st.Put(c.Context(), reported(id), r.ID, r)
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, resumeView{Run: viewOf(x), Resume: token})
}

func record(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated org is required")
	}
	var req reportReq
	if err := bind(c, &req); err != nil {
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
	// A report of a run this org does not have is a row nothing would ever
	// read, so the run is checked and the report written as one act.
	err = st.Do(c.Context(), func(st *Store) error {
		id := idParam(c)
		if _, err := readRun(c.Context(), st, id); err != nil {
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
