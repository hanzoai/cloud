// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package meet

// record.go — recording a room, and who may.
//
//	POST   /v1/meet/record?room=…  start, or hand back the one already running
//	DELETE /v1/meet/record?room=…  stop it
//	GET    /v1/meet/record?room=…  what is running, and where the file goes
//
// ONE AUTHORIZATION, AND IT IS NOT A NEW ONE. A recording is a durable artifact of
// other people's conversation, so the question is not "may this caller record" but
// "is this caller IN this room" — and getToken already decides exactly that, in
// state.admits. It is asked again here rather than re-derived: a second rule about
// the same room is a rule that drifts, and the direction it drifts is somebody
// recording a meeting they cannot join.
//
// The same rule stops as well as starts, deliberately. Anyone the room admits may
// end its recording, including someone who did not begin it — a person who does
// not consent to being recorded has to be able to stop it, and "only the starter
// may stop" would make that impossible in the case it matters.
//
// NOTHING IS REMEMBERED HERE. Which room is being recorded is asked of LiveKit on
// every call (egress.go), so several replicas cannot hold different answers.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	accountapp "github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// recordIn is the input of all three operations, which take the same one thing:
// which room. zip binds it from the BODY on the POST and from the QUERY on the two
// bodyless methods, so one type is one wire on each of them.
type recordIn struct {
	// Room is the LiveKit room, named the way the office client names one
	// (`<workspace>_<name>_<id>`). Its leading segment is what binds the room to a
	// tenant, and it is the segment the caller's membership is checked against.
	Room string `json:"room" validate:"required"`
}

// recording is what a caller is told about a room's recording. It is the SAME
// answer from all three operations: starting one, stopping one and reading one all
// leave the caller holding the same fact, and a second shape would be a second
// thing for a client to parse.
type recording struct {
	// Room is the room this recording is of.
	Room string `json:"room"`
	// ID is the media server's egress id — the handle a later read names.
	ID string `json:"id"`
	// Status is the media server's own state name: EGRESS_STARTING, EGRESS_ACTIVE,
	// EGRESS_ENDING, EGRESS_COMPLETE, EGRESS_FAILED, EGRESS_ABORTED or
	// EGRESS_LIMIT_REACHED. It is passed through rather than folded into a
	// vocabulary of ours, so the answer cannot mean something the media server did
	// not say.
	Status string `json:"status"`
	// Bucket is the object store bucket the recording is written to. It is stated
	// beside the key rather than folded into one URI so a client reads two facts
	// instead of splitting a string.
	Bucket string `json:"bucket"`
	// Object is the key inside that bucket. Empty only while the media server has
	// not named a file yet.
	//
	// It says WHERE the recording is, not how to fetch it. Reading one back is a
	// separate decision this surface deliberately does not make: a link to a
	// private conversation needs its own answer about who may follow it and for
	// how long, and inventing a short one here would be worse than not having it.
	Object string `json:"object"`
	// Started is when the recording began, as the media server reports it: its
	// own `started_at`, verbatim and unconverted. LiveKit's egress service sets
	// that field from UnixNano, and a conversion this side cannot check against
	// the running server would be a number that looks right and is wrong by a
	// factor of a billion. 0 means it has not started.
	Started int64 `json:"started"`
	// Error is the media server's reason when a recording failed, and empty
	// otherwise.
	Error string `json:"error,omitempty"`
}

// none is the answer for a room that is not being recorded: the room, and nothing
// else. It is a 200 rather than a 404 because "this room has no recording" is a
// true answer to the question, not a missing resource — a client that polls this
// while nobody is recording is not asking about something that does not exist.
func none(name string) *recording { return &recording{Room: name, Bucket: bucket} }

// answer turns what the media server said into what the caller is told, over the
// key this process asked for. LiveKit's own filename wins when it has one, because
// a caller reading a recording this process did not start has no other way to learn
// where it went.
func answerOf(got info, name, key string) *recording {
	out := &recording{Room: name, Bucket: bucket, ID: got.ID, Status: got.Status, Object: key, Started: got.Started, Error: got.Error}
	if got.Object != "" {
		out.Object = got.Object
	}
	return out
}

// start begins recording a room, or hands back the recording already running.
//
// A recording is a durable artifact of a conversation, so only someone this room
// would admit may make one: the caller is authorized by the SAME decision
// /v1/meet/getToken makes about the same room, and refused with the same 401.
//
// A SECOND START RETURNS THE FIRST rather than refusing it. There is at most one
// recording per room and this operation's job is to establish that there is one —
// which is already true when a colleague, or the caller's own double-click,
// started it a moment ago. The answer is the same shape either way, naming the
// recording that is actually running, so a client never has to tell the two cases
// apart to find the id.
//
// A deployment with no media server address or no object store answers 503 naming
// which, because a recording that silently does not happen is worse than one that
// is refused. The reason reaches only a caller this room already admits.
func (o ops) start(ctx context.Context, in *recordIn) (*recording, error) {
	p, e, token, err := o.ready(ctx, in.Room, changes)
	if err != nil {
		return nil, err
	}
	seen, err := e.list(ctx, token, in.Room)
	if err != nil {
		return nil, o.upstream(err, "start")
	}
	if live := running(seen.all); len(live) > 0 {
		return answerOf(live[0], in.Room, ""), nil
	}
	// Nothing found is only "nothing here" when everything was legible. Starting on
	// an unproven-free room is how a second recorder lands beside a live one.
	if err := o.unjudged(seen, "start"); err != nil {
		return nil, err
	}
	// Money before the recording exists, so a caller who cannot cover one never
	// starts a worker nobody is paying for. After admission, so a caller who is
	// refused the room is refused for that reason and not handed a bill.
	ch, err := afford(o.s, ctx, record, recordFee())
	if err != nil {
		// cloud.Denied is the one refusal channel a typed op has, and the group's
		// DenyEnvelope writes its bytes back as the money wire's own — the same
		// 402 body the mint beside it answers with.
		return nil, cloud.Denied(err)
	}
	defer ch.Release()
	if err := e.ensure(ctx); err != nil {
		// The store's own error names the endpoint it could not reach, which is an
		// internal host; the operator reads it in the log, the caller reads what
		// failed.
		o.s.Log.Error("meet: the recordings bucket could not be reached, so no recording can be started",
			"bucket", bucket, "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "meet: the recording store did not answer")
	}
	key, err := object(p.Org, in.Room, time.Now())
	if err != nil {
		// No name means no unique object, and a recording written to a name that may
		// already exist would overwrite somebody's meeting. Refuse rather than guess.
		o.s.Log.Error("meet: cannot name a recording", "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "meet: cannot name a recording")
	}
	got, err := e.start(ctx, token, in.Room, key)
	if err != nil {
		return nil, o.upstream(err, "start")
	}
	charge(ch, record, recordFee())
	return answerOf(got, in.Room, key), nil
}

// stop ends a room's recording — EVERY one of them.
//
// Whoever the room admits may stop it, including someone who did not start it:
// a person being recorded has to be able to end it, and a rule that only the
// starter may stop would deny exactly that. Stopping is free — a caller made to
// pay to stop being recorded would be paying for the wrong thing.
//
// 200 MEANS THE ROOM IS NOT BEING RECORDED, and that is why this ends all of them
// rather than the first. "At most one per room" is an invariant this surface wants
// and cannot impose: reading the list and starting are two calls, and two replicas
// racing through that window both start. When the list comes back holding two, two
// is the truth — and ending one while answering 200 tells the person withdrawing
// consent that it stopped while a second worker keeps writing. A stop that cannot
// finish the job says so instead.
//
// Stopping a room that is not being recorded is not an error. The answer names the
// room with no recording on it, which is the state the caller asked for.
func (o ops) stop(ctx context.Context, in *recordIn) (*recording, error) {
	_, e, token, err := o.ready(ctx, in.Room, changes)
	if err != nil {
		return nil, err
	}
	// The id comes from the media server, never from the caller. An egress id names
	// a recording of SOME room, and a caller that could name one directly would stop
	// a recording in a room it was never admitted to.
	seen, err := e.list(ctx, token, in.Room)
	if err != nil {
		return nil, o.upstream(err, "stop")
	}
	live := running(seen.all)
	if len(live) == 0 {
		// The consent-critical direction: answering 200 here says the room is not
		// being recorded, and an entry this surface could not read may be exactly the
		// recording somebody is asking to end.
		if err := o.unjudged(seen, "stop"); err != nil {
			return nil, err
		}
		return none(in.Room), nil
	}
	if len(live) > 1 {
		// Two recorders on one room is the race above having landed. It is an
		// anomaly an operator should see; the caller just gets all of them stopped.
		o.s.Log.Warn("meet: a room was being recorded more than once; ending all of them",
			"recordings", len(live))
	}
	var first info
	for i, one := range live {
		got, err := e.stop(ctx, token, one.ID)
		if err != nil {
			// One failure and the room may still be being recorded, so this is not a
			// 200. The caller is told the stop did not complete rather than told it did.
			return nil, o.upstream(err, "stop")
		}
		if i == 0 {
			first = got
		}
	}
	return answerOf(first, in.Room, ""), nil
}

// read answers what is being recorded in a room, and where the file went.
//
// It reports the recording that is RUNNING, and once none is, the most recent one
// the media server still holds — with its final status and its object. That second
// case is the one that matters for finding a file: the answer to a start is the
// only other place the location appears, and a client that lost it, or a colleague
// who was not the one to press record, has nowhere else to look.
//
// It is behind the same check as starting one: where a recording of a private
// conversation is kept is a fact about that conversation, so it is told to the
// people the room admits and to nobody else.
func (o ops) read(ctx context.Context, in *recordIn) (*recording, error) {
	_, e, token, err := o.ready(ctx, in.Room, reads)
	if err != nil {
		return nil, err
	}
	seen, err := e.list(ctx, token, in.Room)
	if err != nil {
		return nil, o.upstream(err, "read")
	}
	got := latest(seen.all)
	if live := running(seen.all); len(live) > 0 {
		got = &live[0]
	}
	if got == nil {
		if err := o.unjudged(seen, "read"); err != nil {
			return nil, err
		}
		return none(in.Room), nil
	}
	return answerOf(*got, in.Room, ""), nil
}

// unjudged refuses when an empty answer cannot be trusted to mean an empty room.
//
// It is the same remedy the partial stop takes, for the same reason: a surface that
// cannot finish the job says so rather than reporting the state it wishes it had
// found. Nothing here is hypothetical about the peer being hostile — an entry with
// no room name is what a proxy, a version change or a partial write looks like, and
// the failure it produces is F2's exactly: a live recording nobody can see, stopped
// by nobody, with a second recorder started beside it on the next call.
func (o ops) unjudged(seen listing, act string) error {
	if seen.unknown == 0 {
		return nil
	}
	return o.upstream(refused{Code: "unattributable", Msg: strconv.Itoa(seen.unknown) +
		" egress entries named no room, no state or no id, so this room cannot be shown to be free"}, act)
}

// running is every recording of the room that is still going — usually one, and
// the plural is the point. "At most one" is OUR invariant rather than a flag the
// media server was asked to apply for us, so it is applied here, over everything
// the room has, where a second live recording is VISIBLE rather than filtered away
// upstream and silently left running.
func running(all []info) []info {
	var out []info
	for i := range all {
		if all[i].live() {
			out = append(out, all[i])
		}
	}
	return out
}

// latest is the room's most recent recording whatever state it is in, by the media
// server's own start time. It is what a read falls back to once nothing is running:
// a recording that has FINISHED is exactly the one whose location somebody wants,
// and it is the only way to learn that after the process that started it is gone.
func latest(all []info) *info {
	var out *info
	for i := range all {
		if out == nil || all[i].Started > out.Started {
			out = &all[i]
		}
	}
	return out
}

// changes and reads say which kind of operation is asking, so no call site below
// carries a bare boolean. The anti-CSRF gate is a control on a STATE CHANGE, and
// the distinction cannot be taken from the HTTP method here: over the MCP server
// every operation arrives as one POST, so the method says nothing about what the
// operation does.
const (
	changes = true
	reads   = false
)

// ready settles everything an operation needs before it can touch a room's
// recording: the caller ADMITTED, the recording plane CONFIGURED, and one
// credential for the media server minted for the person admits just named.
//
// All three operations open with it and none carries its own copy. Three copies
// of an endpoint is two chances to leave one open, and an endpoint is this file's whole
// subject.
//
// The ORDER is the point and it is the opposite of getToken's. Admission first,
// so the 503 below — which names the variable and the Secret an operator has to
// fix — is read by a member of the room and never by a stranger probing the
// surface. getToken has no such choice: it is reachable by an anonymous browser,
// so its refusal says only that the office is unconfigured.
func (o ops) ready(ctx context.Context, name string, act bool) (principal.Principal, egress, string, error) {
	no := func(err error) (principal.Principal, egress, string, error) {
		return principal.Principal{}, egress{}, "", err
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		// Off the HTTP path — the CLI's local invoke, which carries no attested
		// caller at all. A recording is not something an unattributable caller makes.
		return no(zip.Errorf(http.StatusUnauthorized, "not admitted to this room"))
	}
	// THE ANTI-CSRF GATE IS HERE, not on the route group, because a typed op has TWO
	// entry points and only one is a route. zip wraps the route's handler and calls
	// the op DIRECTLY over MCP, so no group middleware reaches a tools/call — while
	// the depth-0 identity middleware still authenticates the caller, which leaves a
	// signed-in tab's ambient cookie able to start a recording from any origin.
	// Asked inside the op, both pass it. The group still carries the same
	// predicate for /getToken, which has no preamble of its own; one rule, two call
	// sites, never two rules.
	if act == changes {
		if err := accountapp.CSRF(c); err != nil {
			return no(err)
		}
	}
	p, seat, err := o.admitted(c, name)
	if err != nil {
		return no(err)
	}
	e := o.s.State.egress
	if !e.ready() {
		return no(zip.Errorf(http.StatusServiceUnavailable,
			"meet: this deployment cannot record — %s", e.reason))
	}
	// The join key and the Egress key are the same LiveKit pair, so a deployment
	// that cannot sign cannot record either — and sign refuses an empty one rather
	// than minting a token that verifies under the empty key.
	token, err := o.s.State.token(name, seat.account, time.Now())
	if err != nil {
		return no(zip.Errorf(http.StatusServiceUnavailable, "meet: the office is not configured"))
	}
	return p, e, token, nil
}

// admitted is this surface's ONE authorization, and it delegates the whole
// decision to state.admits — the same function POST /v1/meet/getToken admits on.
//
// It takes the REQUEST because that decision reads the identity boundary's own
// attestation (principal.Minted), which is parked on the request and has no
// context-side twin: the facts a typed op can read from the context are derived
// from headers that nothing strips in a hand-written plugin main, which is exactly
// the forgeable signal admits was fixed to stop selecting on. Reaching for the
// weaker fact here would put a client-set header in front of a recording. ready
// resolves it once and hands it down, so this file reaches for the request in one
// place.
//
// It returns the principal as well as the seat because the two are different
// facts and both are needed downstream: the ORG scopes the object key (so one
// tenant's recordings are never written under another's prefix) and the ACCOUNT is
// the identity the media server records the request under. Both come off the same
// attestation, so they cannot disagree about who is asking.
//
// It cannot be reached off the HTTP path: ready refuses there before calling it,
// because there is no request and therefore no attested caller.
func (o ops) admitted(c *zip.Ctx, name string) (principal.Principal, joiner, error) {
	refuse := zip.Errorf(http.StatusUnauthorized, "not admitted to this room")
	seat, ok := o.s.State.admits(c, name)
	// An empty account is refused for the same reason getToken refuses it: it is a
	// caller the media server cannot seat, so it is not a caller who is in the room.
	if !ok || seat.account == "" {
		return principal.Principal{}, joiner{}, refuse
	}
	// Present by construction — admits refuses without it — and read here rather
	// than carried out of admits because the org scopes the OBJECT, which is this
	// file's concern and not the endpoint's.
	p, ok := principal.Minted(c)
	if !ok || p.Org == "" {
		return principal.Principal{}, joiner{}, refuse
	}
	return p, seat, nil
}

// object is where a recording lands: one bucket, one org-scoped prefix, one file.
//
//	<org>/<room>/<utc>-<random>.mp4
//
// THE ROOM NAME IS THE CALLER'S TEXT past its leading workspace segment, so it is
// folded to a single safe label before it becomes part of a key. A name carrying
// `/` or `..` would otherwise write outside its tenant's prefix — the room
// `<workspace>_../../other` is admitted by every rule above it, because only the
// segment before the first underscore is checked. Folding, rather than refusing,
// keeps a legal room name from becoming an unrecordable one.
//
// THE FOLD IS NOT UNIQUE and the timestamp does not rescue it, which is what the
// random tail is for. Folding maps `a/b` and `a-b` to one label and truncates past
// 96 characters, so distinct rooms share one; and the second of resolution rested
// on "at most one recording per room", which stop's own race disproves. Two
// recordings at one key is last-write-wins: one meeting gone, both callers billed.
// The timestamp stays because it sorts for a human reading the bucket; uniqueness
// is the tail's job.
func object(org, name string, at time.Time) (string, error) {
	// ENTROPY, not the clock. One-second resolution rested on "at most one recording
	// per room", which stop.go's own race disproves: two workers handed one key is
	// last-write-wins — one meeting gone, both callers billed. Folding also makes
	// distinct names equal (`a/b` and `a-b`, and anything past the truncation), so
	// the label is for a human reading the bucket and this is what makes the name
	// unique. crypto/rand, because a predictable suffix is a name an attacker who
	// can start a recording could aim at an existing object.
	var seed [6]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return "", err
	}
	return label(org) + "/" + label(name) + "/" +
		at.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(seed[:]) + ".mp4", nil
}

// label folds arbitrary text to one path segment: letters, digits, dash and
// underscore survive and everything else becomes a dash. Nothing it returns can
// contain a separator or a dot, so no output of it can climb out of the prefix it
// was placed in.
func label(s string) string {
	const max = 96
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= max {
			break
		}
	}
	if b.Len() == 0 {
		return "-"
	}
	return b.String()
}

// upstream turns the media server's refusal into this surface's: its CODE to the
// caller, its PROSE to the log.
//
// The prose used to go out verbatim, which made the peer an unbounded writer into
// our answer. The request this surface sends CONTAINS the object store's endpoint
// and access key, and a peer that echoes its input back in a validation error would
// reflect them to whoever asked. That is the same reason the transport error is
// already dropped — *url.Error names the internal media host — applied to the same
// hazard from the same direction. An operator reads the whole sentence in the log,
// where the estate's other secrets already are not.
//
// The code survives because it is a BOUNDED vocabulary (Twirp's own, narrowed at
// the decode boundary), and it is the part that says which kind of failure this
// was: "unavailable" is a deployment with no egress worker, and that is the fact
// somebody acts on.
//
// The STATUS is ours, because the caller did nothing wrong in any of these cases.
// A media server that has no recorder to give is a service that is unavailable, and
// one that refused us for any other reason is a bad gateway — never a 4xx, which
// would tell a client to change its request when the request was fine.
func (o ops) upstream(err error, act string) error {
	status, reason := http.StatusBadGateway, "unknown"
	if r, ok := err.(refused); ok {
		reason = r.Code
		if r.Code == "unavailable" {
			status = http.StatusServiceUnavailable
		}
	}
	o.s.Log.Error("meet: the media server would not serve a recording",
		"act", act, "reason", reason, "detail", err.Error())
	return zip.Errorf(status, "meet: the media server would not %s a recording (%s)", act, reason)
}
