// Package tel is the telecommunications surface: phone numbers, calls and
// messages, on whatever carrier the deployment is configured for.
//
// It is white-label by construction. Nothing in this package names a carrier or a
// brand — `Carrier` (carrier.go) is the whole contract, the concrete one is built
// from configuration at mount, and a brand that terminates on a different network
// in a different jurisdiction changes an environment variable rather than a line
// of code. lux.tel is the first brand to run it; it is not the only one it can
// serve.
//
// Assistants are OURS. A call handed to an agent is answered by a Hanzo assistant
// on Hanzo inference (agent.go) — the carrier moves the audio and does not decide
// what is said. Which model answers is the catalog's decision behind the AI door,
// not a constant in a telecom package.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Acting — the value SanitizeIdentity minted from the VALIDATED
// bearer owner claim (HIP-0026) — and never a client-supplied header. Every store
// query filters on it, so one tenant can neither read nor mutate another's
// numbers, calls or messages.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/tel/numbers/available   search the carrier's inventory   -> {data:[…]}
//	GET    /v1/tel/numbers             numbers this org holds           -> {data:[…]}
//	POST   /v1/tel/numbers             buy one                          -> Number (201)
//	DELETE /v1/tel/numbers/:id         release one
//	GET    /v1/tel/calls               call records                     -> {data:[…]}
//	POST   /v1/tel/calls               place a call                     -> Call (201)
//	DELETE /v1/tel/calls/:id           hang up
//	GET    /v1/tel/messages            message records                  -> {data:[…]}
//	POST   /v1/tel/messages            send one                         -> SMS (201)
//	GET    /v1/tel/summary             per-org roll-up
//
// Every route is a TYPED op — one registry entry, which is what the OpenAPI
// operation, the MCP tool, the CLI command and every generated SDK method are
// projected from. That is also why an agent in a sandbox can drive this surface
// without anybody writing a tool by hand: the tools ARE these ops.
package tel

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

type state struct {
	store      *Store
	carrier    Carrier
	assistants Assistants
	// live says the carrier is the real one and therefore that its acts cost
	// money. The stub buys nothing, so it is never gated and never billed.
	live bool
}

var mounted *cloud.Service[state]

// Mount wires the telecom surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("tel.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("tel.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("tel.Mount: open store: %w", err)
	}

	// Configured, or the stub. A deployment with no carrier credential still
	// serves the whole surface against the stub rather than failing to start —
	// which is what makes the app runnable in a sandbox and in the suite.
	carrier := carrierFromEnv()
	live := carrier != nil
	if carrier == nil {
		carrier = newStub()
	}

	b := cloud.NewBase(deps, "tel")
	s := &cloud.Service[state]{Base: b, State: state{
		store: store, carrier: carrier, assistants: assistantsFromEnv(), live: live,
	}}
	mounted = s

	routes(app, s)

	b.Log.Info("tel mounted", "brand", deps.Brand, "carrier", live)
	return nil
}

// Shutdown releases the store.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	return mounted.State.store.Close()
}

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/tel")
	o := ops{s: s}

	zip.Get(g, "/summary", o.summary)

	zip.Get(g, "/numbers/available", o.searchNumbers)
	zip.Get(g, "/numbers", o.listNumbers)
	zip.Post(g, "/numbers", o.buyNumber, zip.WithStatus(http.StatusCreated))
	zip.Delete(g, "/numbers/:id", o.releaseNumber)

	zip.Get(g, "/calls", o.listCalls)
	zip.Post(g, "/calls", o.placeCall, zip.WithStatus(http.StatusCreated))
	zip.Delete(g, "/calls/:id", o.hangup)

	zip.Get(g, "/messages", o.listMessages)
	zip.Post(g, "/messages", o.sendMessage, zip.WithStatus(http.StatusCreated))
}

type ops struct{ s *cloud.Service[state] }

type noInput struct{}

type idInput struct {
	ID string `path:"id"`
}

// searchInput narrows a number search. Country is required: numbering is
// national, and a search without one is a question no carrier can answer.
type searchInput struct {
	Country string `query:"country"`
	Area    string `query:"area"`
	Type    string `query:"type"`
	Limit   int    `query:"limit"`
}

type numberList struct {
	Data []Number `json:"data"`
}

// searchNumbers asks the carrier what is available to buy. Nothing is recorded —
// a search is not a holding, and treating it as one is how inventory leaks.
func (o ops) searchNumbers(ctx context.Context, in *searchInput) (*numberList, error) {
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	if in.Country == "" {
		return nil, zip.Errorf(http.StatusBadRequest, "country is required")
	}
	found, err := o.s.State.carrier.Search(ctx, NumberQuery{
		Country: in.Country, Area: in.Area, Type: in.Type, Limit: in.Limit,
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "search: %v", err)
	}
	return &numberList{Data: found}, nil
}

// Lists the phone numbers this org HOLDS — the ones it has bought and not
// released. Distinct from the availability search one path down
// (`/numbers/available`), which asks the carrier what could be bought: this
// answers only from our own store, so it is what an org owns rather than what
// it could own.
func (o ops) listNumbers(ctx context.Context, _ *noInput) (*numberList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	held, err := o.s.State.store.Numbers(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &numberList{Data: held}, nil
}

type buyInput struct {
	E164 string `json:"e164"`
}

// buyNumber provisions with the carrier FIRST and records second. The other order
// records a holding that may not exist, and a number the platform believes it owns
// but cannot use is worse than one it failed to buy.
func (o ops) buyNumber(ctx context.Context, in *buyInput) (*Number, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if in.E164 == "" {
		return nil, zip.Errorf(http.StatusBadRequest, "e164 is required")
	}
	ch, err := o.afford(ctx, number)
	if err != nil {
		return nil, cloud.Denied(err)
	}
	defer ch.Release()
	n, err := o.s.State.carrier.Buy(ctx, in.E164)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "buy: %v", err)
	}
	o.charge(ch, number)
	n.Org = org
	if err := o.s.State.store.PutNumber(ctx, n); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "record: %v", err)
	}
	return &n, nil
}

// releaseNumber checks the holding is THIS org's before it reaches the carrier.
// Without that read, an id belonging to another tenant would be released by
// whoever guessed it.
func (o ops) releaseNumber(ctx context.Context, in *idInput) (*noInput, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	held, err := o.s.State.store.Number(ctx, org, in.ID)
	if err != nil || held.ID == "" {
		return nil, zip.Errorf(http.StatusNotFound, "no such number")
	}
	if err := o.s.State.carrier.Release(ctx, in.ID); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "release: %v", err)
	}
	if err := o.s.State.store.DeleteNumber(ctx, org, in.ID); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "record: %v", err)
	}
	return &noInput{}, nil
}

type callList struct {
	Data []Call `json:"data"`
}

type callInput struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Agent string `json:"agent,omitempty"`
	// Record is a per-call flag rather than a product. Where a recording lands and
	// how long it is kept is the org's retention policy, not this call's.
	Record  bool   `json:"record,omitempty"`
	Webhook string `json:"webhook,omitempty"`
}

// Lists the calls this org has placed or received, newest first. Like the
// message list beside it, these are our own records rather than the carrier's.
func (o ops) listCalls(ctx context.Context, _ *noInput) (*callList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.Calls(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &callList{Data: rows}, nil
}

// placeCall dials. An `agent` names a Hanzo assistant to answer it; the call is
// refused up front when no assistant plane is configured, because a call that
// connects to silence has already cost the person who answered it.
func (o ops) placeCall(ctx context.Context, in *callInput) (*Call, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if in.From == "" || in.To == "" {
		return nil, zip.Errorf(http.StatusBadRequest, "from and to are required")
	}
	if in.Agent != "" && o.s.State.assistants == nil {
		return nil, zip.Errorf(http.StatusFailedDependency, "no assistant plane is configured")
	}
	// The number must be this org's. Otherwise the caller ID is somebody else's,
	// which is the definition of spoofing and is refused at the carrier anyway —
	// better here, with a reason.
	if held, err := o.s.State.store.NumberByE164(ctx, org, in.From); err != nil || held.ID == "" {
		return nil, zip.Errorf(http.StatusForbidden, "from is not a number this org holds")
	}

	ch, err := o.afford(ctx, call)
	if err != nil {
		return nil, cloud.Denied(err)
	}
	defer ch.Release()
	placed, err := o.s.State.carrier.Call(ctx, CallRequest{
		From: in.From, To: in.To, Agent: in.Agent, Record: in.Record, Webhook: in.Webhook,
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "call: %v", err)
	}
	o.charge(ch, call)
	placed.Org = org
	if err := o.s.State.store.PutCall(ctx, placed); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "record: %v", err)
	}
	return &placed, nil
}

// Ends a call this org placed. The holding is read for THIS org before the
// carrier is asked, for the reason releaseNumber gives one surface up: an id
// belonging to another tenant would otherwise be hung up by whoever guessed it.
func (o ops) hangup(ctx context.Context, in *idInput) (*noInput, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	row, err := o.s.State.store.Call(ctx, org, in.ID)
	if err != nil || row.ID == "" {
		return nil, zip.Errorf(http.StatusNotFound, "no such call")
	}
	if err := o.s.State.carrier.Hangup(ctx, in.ID); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "hangup: %v", err)
	}
	return &noInput{}, nil
}

type messageList struct {
	Data []SMS `json:"data"`
}

type messageInput struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Text  string   `json:"text"`
	Media []string `json:"media,omitempty"`
}

// Lists the messages this org has sent or received, newest first. Records from
// our own store, not the carrier's — so it is what this platform did on the
// org's behalf, which is the set an audit or a bill has to agree with.
func (o ops) listMessages(ctx context.Context, _ *noInput) (*messageList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.Messages(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return &messageList{Data: rows}, nil
}

// Sends a message from one of this org's own numbers.
//
// `from` must be a number the org HOLDS, checked against the store rather than
// taken on trust — a caller that could send from any number could impersonate
// one, and the carrier would deliver it. `to` is required, and the body needs
// text or media, because a message with neither is delivered as nothing and
// billed as something.
func (o ops) sendMessage(ctx context.Context, in *messageInput) (*SMS, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if in.From == "" || in.To == "" {
		return nil, zip.Errorf(http.StatusBadRequest, "from and to are required")
	}
	if in.Text == "" && len(in.Media) == 0 {
		return nil, zip.Errorf(http.StatusBadRequest, "a message needs text or media")
	}
	if held, err := o.s.State.store.NumberByE164(ctx, org, in.From); err != nil || held.ID == "" {
		return nil, zip.Errorf(http.StatusForbidden, "from is not a number this org holds")
	}

	ch, err := o.afford(ctx, message)
	if err != nil {
		return nil, cloud.Denied(err)
	}
	defer ch.Release()
	m, err := o.s.State.carrier.Send(ctx, SMSRequest{
		From: in.From, To: in.To, Text: in.Text, Media: in.Media,
	})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "send: %v", err)
	}
	o.charge(ch, message)
	m.Org = org
	if err := o.s.State.store.PutMessage(ctx, m); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "record: %v", err)
	}
	return &m, nil
}

type summary struct {
	Numbers  int `json:"numbers"`
	Calls    int `json:"calls"`
	Messages int `json:"messages"`
}

// Counts what this org holds on the telephony plane: its numbers, its calls and
// its messages. The one read a dashboard makes before it asks for any list, so
// it answers three totals and no rows.
func (o ops) summary(ctx context.Context, _ *noInput) (*summary, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	n, c, m, err := o.s.State.store.Counts(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "summary: %v", err)
	}
	return &summary{Numbers: n, Calls: c, Messages: m}, nil
}
