package coding

// fleet.go — the runs this pod flies, and the other pods' words to them.
//
// A run is one goroutine and one sandbox call in the pod that started it. What a
// person says to it — redirect it, give it more time — arrives at whichever pod took
// the request, and that is the pod flying the run one time in three. So a pod that
// does not fly the run asks the others (sdk/coord), and the one that does
// acts on it exactly as it would have at home and answers with what it did: the
// refusal of a run that is not the asker's is the same refusal from any pod.
//
// A run no pod flies is not found, and that is answered as soon as every pod has
// said so. A pod that could not be asked is a different answer, and says so: the
// run may be there.
//
// The harness in a run's sandbox calls cloud with the run's ticket and lands on any
// pod too, so the ticket's calls (a question to the person, a relayed MCP request)
// go the same way. They wait, on a person or a tool, so the asker's context goes
// with them: when the harness goes away, the pod flying the run stops waiting.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/coord"
)

// fleet is the flights of one pod, and its place among the pods.
type fleet struct {
	sync.Mutex
	m       map[string]*flight
	tickets map[string]*flight // by ticketKey

	bus   *coord.Coord                // the deployment's coord when it spans pods (Serve), else nil
	log   func(msg string, kv ...any) // how this pod says what a follow-up did
	leave func()

	// led is the requests and the inbox the deployment shares (Keep); nil where none
	// is kept, and then a request id is refused and an instruction for a run no pod
	// flies is not found.
	led *ledger

	// bell is the timer that wakes the runs parked for a limit (park.go).
	bell *alarm
	// tryMu serialises the tries made for one org or plan (park.go reconsider), and passes
	// is the passes asked for and not yet done, one per org or plan (queue).
	tryMu  map[string]*sync.Mutex
	passes map[string]*pass
}

func newFleet() *fleet {
	return &fleet{m: map[string]*flight{}, tickets: map[string]*flight{}}
}

// flights is the runs this process flies.
var flights = newFleet()

// The questions a fleet answers for the other pods.
const (
	fleetFollow  = "coding.follow"
	fleetExtend  = "coding.extend"
	fleetHolds   = "coding.holds"
	fleetCarried = "coding.carried"
	fleetAsk     = "coding.ask"
	fleetRelay   = "coding.relay"
)

// ErrPods is the other pods of the deployment not all having been asked whether they
// fly a run: it may be on one of them. It is not ErrNoTicket, which is every pod
// having said no.
var ErrPods = errors.New("coding: the other pods could not all be asked whether they fly that run")

var errUnreadable = errors.New("coding: the pod flying that run answered something unreadable")

// Serve makes this process answer the other pods about the runs it flies, or none:
// a coord that answers for this process alone leaves it as it was. It answers the
// function that stops. log is how a redirect asked of another pod is reported.
func Serve(c *coord.Coord, log func(msg string, kv ...any)) func() { return flights.serve(c, log) }

func (fl *fleet) serve(c *coord.Coord, log func(msg string, kv ...any)) func() {
	fl.Lock()
	leave := fl.leave
	fl.bus, fl.leave, fl.log = nil, nil, nil
	fl.Unlock()
	if leave != nil {
		leave()
	}
	if c == nil || !c.Shared() {
		return func() {}
	}
	stops := []func(){
		c.Serve(fleetFollow, fl.serveFollow),
		c.Serve(fleetExtend, fl.serveExtend),
		c.Serve(fleetHolds, fl.serveHolds),
		c.Serve(fleetCarried, fl.serveCarried),
		c.Serve(fleetAsk, fl.serveAsk),
		c.Serve(fleetRelay, fl.serveRelay),
	}
	stop := func() {
		for _, s := range stops {
			s()
		}
	}
	fl.Lock()
	fl.bus, fl.log, fl.leave = c, log, stop
	fl.Unlock()
	return func() { fl.serve(nil, nil) }
}

// ledger is the requests and the inbox this pod keeps, nil when it keeps none.
func (fl *fleet) ledger() *ledger {
	fl.Lock()
	defer fl.Unlock()
	return fl.led
}

// keep makes l the ledger this pod's requests and instructions are kept in.
func (fl *fleet) keep(l *ledger) {
	fl.Lock()
	fl.led = l
	fl.Unlock()
}

func (fl *fleet) coord() (*coord.Coord, func(msg string, kv ...any)) {
	fl.Lock()
	defer fl.Unlock()
	return fl.bus, fl.log
}

// followCall and extendCall are a word to a run as another pod is asked it, and
// ticketCall a call of the run's harness.
type followCall struct {
	Org     string               `json:"org"`
	Subject string               `json:"subject"`
	In      client.CodingSteerIn `json:"in"`
}

type extendCall struct {
	Org     string                `json:"org"`
	Subject string                `json:"subject"`
	In      client.CodingExtendIn `json:"in"`
}

type ticketCall struct {
	Ticket string          `json:"ticket"`
	Server string          `json:"server,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	In     client.AskIn    `json:"in,omitempty"`
}

// The refusals a ticket's calls give that a caller tells apart with errors.Is, by the
// name they cross pods under. Any other refusal crosses as its text.
var kinds = map[string]error{
	"noticket":       ErrNoTicket,
	"notcarried":     ErrNotCarried,
	"toolnotcarried": ErrToolNotCarried,
	"relaylimit":     ErrRelayLimit,
}

// said is a refusal another pod gave, as this pod's caller reads it: the same
// sentinel it would have had at home, and the same words.
type said struct {
	is  error
	msg string
}

func (e *said) Error() string { return e.msg }
func (e *said) Unwrap() error { return e.is }

// verdict is what the pod that flies a run answers: nothing, or the refusal it
// would have given at home, or the value it computed.
type verdict struct {
	Status int             `json:"status,omitempty"` // an HTTP refusal
	Kind   string          `json:"kind,omitempty"`   // a sentinel (kinds), or "other"
	Msg    string          `json:"msg,omitempty"`
	Out    json.RawMessage `json:"out,omitempty"`
}

func verdictOf(out any, err error) ([]byte, bool) {
	var v verdict
	var he *zip.HTTPError
	switch {
	case errors.As(err, &he):
		v = verdict{Status: he.Status, Msg: he.Msg}
	case err != nil:
		v = verdict{Kind: "other", Msg: err.Error()}
		for k, is := range kinds {
			if errors.Is(err, is) {
				v.Kind = k
			}
		}
	case out != nil:
		b, merr := json.Marshal(out)
		if merr != nil {
			v = verdict{Kind: "other", Msg: merr.Error()}
		} else {
			v.Out = b
		}
	}
	b, merr := json.Marshal(v)
	return b, merr == nil
}

// err is the refusal the verdict carries, if it carries one.
func (v verdict) err() error {
	switch {
	case v.Status != 0:
		return zip.Errorf(v.Status, "%s", v.Msg)
	case v.Kind != "":
		return &said{is: kinds[v.Kind], msg: v.Msg}
	}
	return nil
}

// serveFollow redirects a run this pod flies, for a pod that does not. It says no
// when this pod does not fly it either.
func (fl *fleet) serveFollow(ctx context.Context, _ string, body []byte) ([]byte, bool) {
	var c followCall
	if json.Unmarshal(body, &c) != nil {
		return nil, false
	}
	f := fl.find(c.Org, c.In)
	if f == nil {
		return nil, false
	}
	_, log := fl.coord()
	st, err := f.followed(ctx, c.Subject, c.In, log)
	return verdictOf(st, err)
}

// serveExtend gives a run this pod flies more time, for a pod that does not.
func (fl *fleet) serveExtend(ctx context.Context, _ string, body []byte) ([]byte, bool) {
	var c extendCall
	if json.Unmarshal(body, &c) != nil {
		return nil, false
	}
	f := fl.flying(c.Org, c.In.Session)
	if f == nil {
		return nil, false
	}
	out, err := f.extended(ctx, c.Subject, &c.In)
	if err != nil {
		return verdictOf(nil, err)
	}
	return verdictOf(out, nil)
}

// serveHolds says whether the ticket is of a run this pod flies.
func (fl *fleet) serveHolds(_ context.Context, _ string, ticket []byte) ([]byte, bool) {
	return nil, fl.held(string(ticket)) != nil
}

// serveCarried says whether a run this pod flies opens a server.
func (fl *fleet) serveCarried(_ context.Context, _ string, body []byte) ([]byte, bool) {
	var c ticketCall
	if json.Unmarshal(body, &c) != nil {
		return nil, false
	}
	f := fl.held(c.Ticket)
	if f == nil {
		return nil, false
	}
	_, err := f.carrying(c.Server)
	return verdictOf(nil, err)
}

// serveAsk puts a harness's question to the person of a run this pod flies, and
// waits for the answer until the pod that was asked stops waiting.
func (fl *fleet) serveAsk(ctx context.Context, _ string, body []byte) ([]byte, bool) {
	var c ticketCall
	if json.Unmarshal(body, &c) != nil {
		return nil, false
	}
	f := fl.held(c.Ticket)
	if f == nil {
		return nil, false
	}
	text, err := f.asked(ctx, c.In)
	if err != nil {
		return verdictOf(nil, err)
	}
	return verdictOf(text, nil)
}

// serveRelay relays a harness's MCP request for a run this pod flies.
func (fl *fleet) serveRelay(ctx context.Context, _ string, body []byte) ([]byte, bool) {
	var c ticketCall
	if json.Unmarshal(body, &c) != nil {
		return nil, false
	}
	f := fl.held(c.Ticket)
	if f == nil {
		return nil, false
	}
	out, err := f.relayed(ctx, c.Server, c.Method, c.Params)
	if err != nil {
		return verdictOf(nil, err)
	}
	return verdictOf(out, nil)
}

// followElsewhere is the word to a run this pod does not fly: said to the pod that
// does, or, when none does, kept in the run's inbox for the pod that will (inbox.go).
func (fl *fleet) followElsewhere(ctx context.Context, org, subject string, in client.CodingSteerIn) (steered, error) {
	v, found, err := fl.elsewhere(context.Background(), fleetFollow, followCall{Org: org, Subject: subject, In: in})
	switch {
	case err != nil:
		return steered{}, zip.Errorf(http.StatusServiceUnavailable, "%v; try again", err)
	case found:
		var st steered
		if err := v.err(); err != nil {
			return steered{}, err
		}
		if len(v.Out) > 0 {
			_ = json.Unmarshal(v.Out, &st) // a pod of an older version answers nothing
		}
		return st, nil
	}
	// A run parked for a limit is flown by no pod and is not running: Continue tries it now
	// and an instruction waits for it (park.go).
	if st, handled, err := fl.parkedWord(ctx, org, subject, in); handled {
		return st, err
	}
	if err := fl.queue(ctx, org, subject, in); err != nil {
		return steered{}, err
	}
	return steered{Queued: true}, nil
}

// extendElsewhere is more time for a run this pod does not fly.
func (fl *fleet) extendElsewhere(org, subject string, in *client.CodingExtendIn) (*client.CodingExtended, error) {
	v, found, err := fl.elsewhere(context.Background(), fleetExtend, extendCall{Org: org, Subject: subject, In: *in})
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v; try again", err)
	}
	if !found {
		// A run parked for a limit has no time running to extend; it is tried again, now,
		// by Continue.
		if l := fl.ledger(); l != nil {
			ctx, cancel := context.WithTimeout(context.Background(), writeWait)
			defer cancel()
			if p, ok, _ := l.parked(ctx, org, strings.TrimSpace(in.Session)); ok && p.subject == subject {
				return nil, zip.Errorf(http.StatusConflict, "coding: that run is parked; continue it instead")
			}
		}
		return nil, zip.ErrNotFound("coding: nothing of yours is running there")
	}
	if err := v.err(); err != nil {
		return nil, err
	}
	var out client.CodingExtended
	if json.Unmarshal(v.Out, &out) != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "%v", errUnreadable)
	}
	return &out, nil
}

// elsewhere asks the other pods which fly the run and answers the one that does. No
// pod flying it is found false, which every pod having said no makes true at once; a
// pod that could not be asked is ErrPods, because the run may be on it. The call
// ends when ctx does, and the pod flying the run is told.
func (fl *fleet) elsewhere(ctx context.Context, question string, call any) (verdict, bool, error) {
	c, _ := fl.coord()
	if c == nil {
		return verdict{}, false, nil
	}
	var body []byte
	switch x := call.(type) {
	case string:
		body = []byte(x)
	default:
		var err error
		if body, err = json.Marshal(call); err != nil {
			return verdict{}, false, err
		}
	}
	ans, ok, err := c.Call(ctx, question, body)
	if !ok {
		if err != nil {
			return verdict{}, false, fmt.Errorf("%w: %v", ErrPods, err)
		}
		return verdict{}, false, nil
	}
	if question == fleetHolds {
		return verdict{}, true, nil
	}
	var v verdict
	if json.Unmarshal(ans.Body, &v) != nil {
		return verdict{}, false, errUnreadable
	}
	return v, true, nil
}
