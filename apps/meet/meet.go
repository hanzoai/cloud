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

// Package meet is the virtual office: it decides who may join a room and mints
// the short-lived token that lets them in.
//
//	POST /v1/meet/getToken  {roomName, _id, participantName}  ->  the token, as text
//
// The MEDIA plane is not here and never will be. Audio, video and screen share ride
// a direct browser↔LiveKit WebRTC connection (LIVEKIT_WS = wss://live.hanzo.bot);
// media is not a thing to proxy through an API binary. What moved into this binary
// is the ONE decision a server has to make about a call — may this caller join this
// room — which needs the LiveKit signing key and an answer from the process that
// owns the space rows, and needs no pod of its own to hold either.
//
// ONE KEY, and it signs the ANSWER only. It is read from the SAME keys.yaml file
// the LiveKit server itself validates against (Secret `livekit-keys`, mounted
// read-only). ONE representation of that material, so it cannot drift: a second
// copy projected into env would mint tokens that look perfect and are refused at
// the media edge, which is the silent failure this whole package is trying not to
// have.
//
// The CALLER is verified by IAM and by nothing else. meet holds no key that
// verifies a caller, so there is no second bearer authority here to disagree with
// the first: a request arrives with an attested principal or it is refused. What
// that principal MAY do is not read off the token either — it is asked of apps/team,
// which owns the membership rows (see roster).
//
// Missing or ambiguous material is a 503, LOUDLY: the reason names the file and the
// Secret in the log at boot, while the caller gets an unadorned "not configured" (an
// unauthenticated 503 is not the place to enumerate our secret plumbing).
package meet

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	// Aliased: this package's tests already bind `account` to a UUID literal, and
	// an import that shadows a package-level identifier is a compile error in the
	// test build only — green `go build`, red `go test`.
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
	"gopkg.in/yaml.v3"
)

// ttl is how long a minted join token is good for. Ten minutes: long enough to
// complete a join handshake on a bad connection, short enough that a leaked token
// is worthless before anyone can use it. The client re-mints per join, so this is
// not a session length — a call that has already started is a LiveKit connection
// and does not re-check the token.
const ttl = 10 * time.Minute

// keyFileEnv overrides where keys.yaml is read from. It exists because the LiveKit
// server takes the same knob (--key-file), so the path is a deployment fact on both
// sides of the pair rather than a constant on one — and because a test must be able
// to point at a temp file.
const keyFileEnv = "LIVEKIT_KEY_FILE"

// keyFile is where the manifest mounts Secret `livekit-keys`. Same file, same Secret,
// same content the LiveKit server reads.
const keyFile = "/etc/livekit-keys/keys.yaml"

// apiKeyEnv names WHICH api key in keys.yaml to sign with, for the case where the file
// declares more than one. Unset is correct and normal for a single-key file.
const apiKeyEnv = "LIVEKIT_API_KEY"

// wsEnv is where the browser opens its WebRTC signaling socket — the LiveKit
// server this binary's tokens are honoured by (wss://live.hanzo.bot).
//
// It is served to the client rather than baked into the bundle because it is a
// DEPLOYMENT fact and the bundle is a build artifact: a compiled-in address makes
// a dev cluster's UI dial production's media plane, and makes the address
// unchangeable without a rebuild.
//
// It is NOT part of ready(). A token is minted the same way whether or not this
// binary knows the address — the published office client supplies its own — so an
// unset value degrades the native UI and breaks nothing else. The native lobby is
// told plainly (ws: "") and refuses to dial rather than guessing a host.
const wsEnv = "LIVEKIT_WS"

// state is meet's own data: the caller-verifying key, the answer-signing pair, and
// the reason it is unusable when it is. reason is the ONE flag — a non-empty reason
// IS "not configured", so there is no way for the two to disagree.
type state struct {
	apiKey    string // LiveKit api key    — the `iss` LiveKit matches on
	apiSecret string // LiveKit api secret — signs the minted token
	ws        string // LIVEKIT_WS — where the browser dials; empty is legible, see wsEnv
	reason    string // why this is unusable; empty means usable
	authority roster // who owns the membership rows; nil means the real peer, see rows
	egress    egress // where a recording is made and where it lands, see egress.go
}

// roster is the space-membership authority — the process that OWNS the rows,
// asked across a process boundary.
//
// It is an interface for ONE reason, and the reason is a bug it already hid. The
// decisions above it are made HERE: whether a machine credential may take a seat,
// whether a guest may, which spaces to offer. But every one of them is reached
// only AFTER the peer answers, and no test had a peer — so each IAM-lane test
// stopped at an unanswerable ask and passed for the wrong reason. Mutation proved
// it: deleting the machine-credential exclusion (p.Subject != "") changed nothing
// the suite could see, because the machine got as far as the ask and was refused
// by the missing peer rather than by the rule. A decision that only exists past a
// boundary needs the boundary to be crossable in a test, or it is not tested.
//
// The org is NOT a parameter. It rides the context (cloud.As), exactly as it did
// when these were bare cloud.Ask calls, so a caller cannot ask about another
// tenant by naming one.
type roster interface {
	member(ctx context.Context, space, subject string) (*plane.Member, error)
	spaces(ctx context.Context, subject string) (*plane.Spaces, error)
}

// peer is the real authority: apps/team over the internal plane. Stateless, so
// the zero value is the whole implementation.
type peer struct{}

func (peer) member(ctx context.Context, space, subject string) (*plane.Member, error) {
	return cloud.Ask[plane.MemberIn, plane.Member](ctx, "team", plane.TeamMember,
		&plane.MemberIn{Space: space, Subject: subject})
}

func (peer) spaces(ctx context.Context, subject string) (*plane.Spaces, error) {
	return cloud.Ask[plane.SpacesIn, plane.Spaces](ctx, "team", plane.TeamSpaces,
		&plane.SpacesIn{Subject: subject})
}

// rows is the authority to ask. A state that names none asks the real peer — so
// this is nil-safe by construction rather than by discipline, and every load()
// failure path (which returns a state carrying only a reason) still answers a
// lobby read the same way production does instead of panicking on it.
func (s state) rows() roster {
	if s.authority == nil {
		return peer{}
	}
	return s.authority
}

// ready reports whether meet can mint. Fail-closed: an unconfigured deploy refuses
// every mint rather than issuing a token nobody can verify.
func (s state) ready() bool { return s.reason == "" }

// load assembles the signing material and, when it cannot, says exactly why. Every
// failure path produces a reason naming the file or env var an operator has to fix —
// this used to return a bare zero value, which made a misconfigured deploy an
// indistinguishable permanent 503 with nothing in the log to chase.
func load() state {
	path := environ.Or(keyFileEnv, "")
	if path == "" {
		path = keyFile
	}
	ws := environ.Or(wsEnv, "")
	key, apiSecret, err := readKeys(path)
	if err != nil {
		// The recording plane is still read on this path. It has its own reason and
		// its own routes, and an operator fixing one fault should be told about the
		// other in the same boot rather than after the first fix.
		return state{reason: err.Error(), egress: egressOf(ws)}
	}
	return state{apiKey: key, apiSecret: apiSecret, ws: ws, egress: egressOf(ws)}
}

// readKeys parses a LiveKit key file: a YAML map of apiKey -> apiSecret, which is the
// format the LiveKit server's --key-file takes. It returns the single pair, or an
// error naming the file and Secret.
//
// EXACTLY ONE entry is required. Zero is unconfigured. More than one is AMBIGUOUS,
// and ambiguity here is refused rather than resolved: Go map iteration is random, so
// "just take the first" would pick a different key per process start, and a token
// signed under a key the caller's room was not provisioned for fails at the media
// edge intermittently — the worst possible failure shape. If a deployment ever needs
// several api keys, the code that chooses between them has to be written on purpose.
func readKeys(path string) (string, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("cannot read the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml): %v", path, err)
	}
	// gopkg.in/yaml.v3 into map[string]string — the EXACT library and target type the
	// LiveKit server decodes this file with (livekit/pkg/config: `Keys
	// map[string]string`, yaml.v3), so we and the verifier read identical bytes to
	// identical strings by construction.
	//
	// This was sigs.k8s.io/yaml, which is NOT equivalent: it routes YAML through JSON
	// and coerces a scalar to the target type, so measured on real input it turned
	//   0123456789 -> "1.2345679e+08"      yes -> "true"      0x1f -> "31"
	//   1e5        -> "100000"             no  -> "false"     00   -> "0"
	// and silently took the LAST of a duplicated key. Any of those mints a token that
	// verifies nowhere while the log says "mounted" — the exact silent failure this
	// file claims to prevent. yaml.v3 preserves all of them verbatim and REFUSES a
	// duplicate key outright, which we get for free by using the right library.
	var keys map[string]string
	if err := yaml.Unmarshal(raw, &keys); err != nil {
		return "", "", fmt.Errorf("cannot parse the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) as a YAML apiKey->apiSecret map: %v", path, err)
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names) // stable messages; no decision depends on map order
	if len(names) == 0 {
		return "", "", fmt.Errorf("the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) declares no api key", path)
	}
	// A LiveKit key file is a MAP because a server may hold several keys. When it
	// does, LIVEKIT_API_KEY names which one this binary signs with. Selecting by name
	// is the only safe way to resolve the ambiguity: Go map order is random, so
	// "take the first" would pick differently per process start and produce tokens
	// that fail at the media edge intermittently.
	key := ""
	if want := environ.Or(apiKeyEnv, ""); want != "" {
		if _, found := keys[want]; !found {
			return "", "", fmt.Errorf("%s names api key %q, which the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) does not declare (it has: %s)", apiKeyEnv, want, path, strings.Join(names, ", "))
		}
		key = want
	} else if len(names) > 1 {
		return "", "", fmt.Errorf("the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) declares %d api keys (%s); set %s to name which one to sign with — refusing to pick", path, len(names), strings.Join(names, ", "), apiKeyEnv)
	} else {
		key = names[0]
	}
	apiSecret := keys[key]
	// Blank-ish is refused, but the values are returned BYTE-EXACT — deliberately not
	// trimmed. The only property that matters is that the pair we sign with is
	// identical to the pair the LiveKit server read from these same bytes. Trimming
	// would silently diverge from any reader that does not trim: the api key is the
	// `iss` LiveKit matches on, and the secret IS the signing key, so one stripped
	// space produces tokens that mint perfectly and verify nowhere. Consistency with
	// the other reader beats tidiness.
	if strings.TrimSpace(key) == "" || strings.TrimSpace(apiSecret) == "" {
		return "", "", fmt.Errorf("the LiveKit key file %s (K8s Secret livekit-keys, key keys.yaml) has an empty api key or secret", path)
	}
	return key, apiSecret, nil
}

// The mint and session routes are UNTYPED BY DESIGN — mint answers the raw token
// as text/plain, session answers a shape assembled per caller, and the comment at
// each registration says why zip cannot declare it. zipdoc lifts prose from typed
// ops and there is none to lift from either, so their prose is declared beside the
// route table instead and reaches the document, the generated SDKs and the
// spec-derived CLI unchanged.
func init() {
	openapi.Describe("/v1/meet/getToken", http.MethodPost,
		"Mint a join token for one video room",
		"Answers with a LiveKit join token for exactly the room named in the body. The body "+
			"is the RAW token as text/plain — one opaque string, not JSON and not wrapped in "+
			"an envelope, which is what the office client reads.\n\n"+
			"The caller presents its space session as a Bearer. Every clause is a "+
			"refusal: the session must verify, its SIGNED space claim must equal the "+
			"room's leading name segment — rooms are named `<space>_<room>_<id>`, and "+
			"that prefix is the only thing binding a room to a tenant — and the session must "+
			"carry a privileged space role, so a guest is refused rather than seated.\n\n"+
			"The participant identity is the SESSION'S, never the body's. `_id` is accepted "+
			"for compatibility with the published client bundle and deliberately ignored: "+
			"LiveKit treats the identity as unique and ejects a duplicate, so honouring a "+
			"caller-chosen one would let anyone in a space kick out a colleague and "+
			"impersonate them. `participantName` is a display name only.\n\n"+
			"An unconfigured deployment answers 503 under its own name rather than 404, and "+
			"the refusal states only that the office is unconfigured — the reason names key "+
			"material and stays in the boot log.")
	openapi.Describe("/v1/meet/session", http.MethodGet,
		"What this caller may open a room in",
		"Answers the three facts the native lobby cannot know on its own: the identity a "+
			"seat would be taken under, the LiveKit address the browser dials, and the "+
			"spaces this caller may open a room in.\n\n"+
			"It is the SAME decision getToken makes, asked before the room exists rather "+
			"than after it is named. A room is bound to its tenant by its name's leading "+
			"space segment, and only a space this answer lists will be admitted — "+
			"so the lobby offers exactly what the mint would grant, and a person is never "+
			"shown a room they would then be refused. Spaces the caller holds only a "+
			"guest role in are omitted for that reason.\n\n"+
			"An empty list is a real answer, not a fault: an IAM identity with no space "+
			"has no room to open, and the lobby says so instead of failing.\n\n"+
			"`ws` is empty when this deployment has not been told where its media plane is "+
			"(LIVEKIT_WS). Token minting is unaffected — the published office client "+
			"supplies its own address — so this is a degraded native UI, not a degraded "+
			"service, and the lobby refuses to dial rather than guessing a host.")
	// /v1/meet/health declares no prose here: it is a TYPED op now (see Mount), and
	// zipdoc lifts its summary and description from the op itself. Describing it in
	// both places is the drift this file already paid for once.
}

// Mount wires /v1/meet/* onto app. The route is registered even when unconfigured so
// the surface always answers under its OWN name with an honest 503, rather than
// falling through to some other subsystem's catch-all and reporting a 404 for a
// service that exists but has no keys.
func Use(app cloud.Router, deps cloud.Deps) error { return serve(app, deps, load()) }

// serve hangs the routes on app for a state already assembled. Reading the
// deployment (load) and hanging the routes are two jobs, and Mount is the one
// composition of them; a caller that already holds a state — this package's own
// tests, which must supply the space authority the routes ask, since it lives
// in another process — serves it directly rather than through the environment.
func serve(app cloud.Router, deps cloud.Deps, st state) error {
	if app == nil {
		return fmt.Errorf("meet.Use:  nil app")
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "meet"), State: st}

	// The path suffix is the CALLER's, not ours. The office client POSTs
	// concatLink(LOVE_ENDPOINT, '/getToken') from a published bundle, so with
	// LOVE_ENDPOINT=/v1/meet the wire lands here. Renaming it means shipping a new
	// front image, not editing a manifest.
	//
	// UNTYPED BY DESIGN — the response body is the raw token as text/plain
	// (c.String, see mint), which is the office client's contract: it reads the
	// answer with res.text(). A typed op always marshals its Out to JSON, so typing
	// this route turns `<token>` into `"<token>"` under application/json and breaks
	// every published bundle in the field. It stays the escape hatch until zip can
	// declare a non-JSON response.
	// THE GROUP EXISTS FOR THE GATE. Every /v1/meet route hangs off it, so the
	// anti-CSRF check is written ONCE and cannot be forgotten by the next route
	// added here — the same reason apps/todo puts it on its group rather than
	// on each of its six writes.
	// cloud.DenyEnvelope beside the CSRF gate and BEFORE the leaves, since fiber
	// runs middleware in registration order: starting a recording gates on the
	// caller's balance, and a typed op's only refusal channel is an error, so the
	// envelope is what turns cloud.Denied back into the same 402 body the mint
	// answers with rather than a second vocabulary for it.
	g := app.Group("/v1/meet", requireCSRFOnWrites(), cloud.DenyEnvelope())

	g.Post("/getToken", cloud.Handle(s, mint))

	// The lobby's read. UNTYPED for a reason that is this app's alone: the gate is
	// principal.Minted — the boundary's OWN attestation — and a typed op holds a
	// context, not a request, so it cannot read one. The context-side facts a typed
	// op CAN read (principal.OrgFrom / ValidatedFrom) are derived from headers that
	// nothing strips in a hand-written plugin main, which is exactly the forgeable
	// signal admits was fixed to stop selecting on. Typing this route would put the
	// weaker fact back in front of the same rows.
	g.Get("/session", cloud.Handle(s, session))

	// /v1/meet/health makes "the office is unconfigured" a SIGNAL rather than a grep.
	// A boot log line is invisible to a dashboard and rotates away; this is the same
	// contract every other subsystem exposes, so the existing probe/alerting surface
	// picks it up with no new machinery.
	//
	// A TYPED op, declaring BOTH statuses it answers with: the report's own
	// StatusCode picks 200 or 503, so the degraded answer keeps carrying the same
	// body the healthy one does — the pair that once kept this route raw, before
	// zip could declare a non-2xx with a typed body.
	o := ops{s}
	zip.Get(g, "/health", o.health,
		zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))

	// ONE PATH, three methods, one input and one answer (record.go). A recording is
	// a property of a room rather than a thing a caller names, so there is nothing
	// to put in the path after /record: the room is the input, and the method says
	// what to do about it.
	//
	// THE ANTI-CSRF GATE ON THESE IS NOT THE GROUP'S, and believing otherwise is how
	// they shipped reachable from any origin. A typed op is not one entry point: zip
	// records the route's handler and the op as two fields of one entry and wraps
	// only the handler, so the REST route is gated by the group above and the other
	// five entry points onto the same op — MCP, the call plane, GraphQL, the CLI,
	// Here — are not. The gate for these three lives in ops.ready, which every one
	// of them passes through. The group gate stays for /getToken, which is a raw
	// preamble of its own.
	zip.Post(g, "/record", o.start,
		zip.WithOperationID("meetRecordStart"),
		zip.WithSummary("Start recording a room, or return the recording already running"))
	zip.Delete(g, "/record", o.stop,
		zip.WithOperationID("meetRecordStop"),
		zip.WithSummary("Stop a room's recording"))
	zip.Get(g, "/record", o.read,
		zip.WithOperationID("meetRecordRead"),
		zip.WithSummary("What is being recorded in a room, and where the file goes"))

	// Where a collaboration room's call happens (room.go). It answers the media
	// room a (space, room) pair resolves to, so a surface rendering a channel
	// asks for the call instead of composing the name itself — one spelling of that
	// rule, here, rather than one per client.
	//
	// A READ, so it is free and carries no anti-CSRF gate: it changes nothing, and
	// its whole answer is already narrower than the mint it precedes. The seat is
	// what costs, and getToken still charges for it.
	zip.Get(g, "/call", o.resolve,
		zip.WithOperationID("meetCall"),
		zip.WithSummary("Where a room's call happens"))

	// THE CLIENT IS NOT HERE. It is its own image (ghcr.io/hanzoai/meet, built from
	// hanzoai/admin apps/meet at base '/') on its own host, meet.hanzo.ai — three
	// planes with three lifecycles: the bundle is static bytes, this binary mints
	// the join token, and the media is a direct browser↔LiveKit connection that
	// touches neither.
	//
	// It used to be //go:embed'd here and served at /meet/*, which put every UI
	// change behind a full Go release and put a client bundle on the API origin.
	// Nothing about the request path required it: the client reads /v1/meet/session
	// and posts /v1/meet/getToken, and it carries a bearer for both, so those two
	// are answered the same whether the bundle shares this origin or not.
	//
	// The bearer is the whole reason meet could move and tasks could not. A client
	// that holds one can name its API; a client that relies on a host-only cookie
	// cannot, which is why tasks.hanzo.ai is split at the edge instead.

	// The recording plane carries its own configuration and its own reason, so it
	// gets its own line in BOTH branches below: a deployment can mint join tokens
	// perfectly and still be unable to record, and an operator reading only
	// "mounted" would not know which. WARN rather than ERROR because calls
	// themselves are unaffected — only /v1/meet/record is.
	if !s.State.egress.ready() {
		s.Log.Warn("meet cannot record — POST /v1/meet/record will 503 on every call until this is fixed; joining calls is unaffected",
			"reason", s.State.egress.reason, "prefix", "/v1/meet")
	}
	if !s.State.ready() {
		// ERROR, not warn, and it names the file/Secret to fix. A subsystem that can
		// never serve a single request is not a warning — and the previous version of
		// this line said only "not all set", which is exactly why a Secret that was
		// empty in the cluster could have shipped as a permanent, silent 503.
		s.Log.Error("meet subsystem UNCONFIGURED — POST /v1/meet/getToken will 503 on every call until this is fixed; the office (video/audio rooms) is down",
			"reason", s.State.reason, "prefix", "/v1/meet")
		return nil
	}
	// The api key is half a credential pair, so it goes into the log the one way
	// this binary puts any credential into one: as a Fingerprint, the stable
	// per-process handle that confirms WHICH key pair is in play without printing
	// it. That it is also the public `iss` of every minted token is a reason this
	// was never an urgent disclosure, not a reason to print it — a log is read by
	// more people, kept longer, and shipped further than a token is, and nothing
	// downstream can tell a key from a secret by looking. The secret is never
	// logged, and never was.
	s.Log.Info("meet subsystem mounted", "prefix", "/v1/meet", "ttl", ttl.String(), "cred", cloud.Fingerprint(s.State.apiKey))
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the mounted Service so a typed op can be a method value — the only
// bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query. GET carries no request body, so this publishes nothing.
type noIn struct{}

// meetHealth is the probe's answer, the SAME shape at 200 and at 503 — ready is
// the whole dashboard fact, and the field order is the byte order the map it replaced
// marshaled (keys sorted).
type meetHealth struct {
	// Ready reports whether this deployment can mint join tokens. False is the 503.
	Ready bool `json:"ready"`
	// Service names the subsystem answering — always "meet".
	Service string `json:"service"`
	// Status is "ok" when tokens can be minted and "degraded" when they cannot.
	Status string `json:"status"`
}

// StatusCode is how the answer states which of the op's two declared statuses it
// carries: ready picks 200, degraded picks 503.
func (h *meetHealth) StatusCode() int {
	if h.Ready {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

// Health reports whether the office can mint join tokens.
//
// It reports whether this deployment holds the LiveKit key pair it needs:
// ready:true with 200 when tokens can be minted, the SAME body with ready:false,
// status "degraded" and 503 when they cannot — so a probe and a dashboard both
// read the degraded state instead of someone grepping a boot log.
//
// It takes no credential and is reachable on every public host, so it withholds
// both the reason and the signing key's name on purpose: ready is the whole
// dashboard fact, and the reason — which names the key file and the Secret — is
// written to the boot log where an operator already is.
func (o ops) health(context.Context, *noIn) (*meetHealth, error) {
	if !o.s.State.ready() {
		return &meetHealth{Ready: false, Service: "meet", Status: "degraded"}, nil
	}
	return &meetHealth{Ready: true, Service: "meet", Status: "ok"}, nil
}

// lobby is what the native client reads before it can name anything: who it would
// be seated as, where the media plane is, and which spaces it may open a room
// in.
//
// The space list is the POINT of this route. A room is bound to its tenant by
// the leading segment of its name (see space), so a client that does not know
// its own space cannot compose a room name that any lane would admit — and it
// has no way to learn one, because a uuid is not something a person types. The
// alternative was for the client to guess and be refused, which is a lobby that
// only works for someone who was sent a link.
type lobby struct {
	// Identity is the account a seat would be taken under — the SAME identity mint
	// puts in the token's `sub`, and not something the caller may choose.
	Identity string `json:"identity"`
	// Name is the display label to prefill, empty when this deployment holds none.
	Name string `json:"name"`
	// WS is the LiveKit address the browser dials, empty when unconfigured (wsEnv).
	WS string `json:"ws"`
	// Spaces is every space this caller may open a room in — already
	// narrowed to the roles mint would admit, so the offer and the grant agree.
	Spaces []plane.Space `json:"spaces"`
}

// session answers GET /v1/meet/session. It admits on the SAME two lanes as mint
// and refuses on the same terms, so a caller that could not join anything is told
// so up front rather than after composing a room name.
//
// It answers OUTSIDE ready(), deliberately, and for the same reason the bundle is
// served outside it: an unconfigured deployment should render a client that states
// the problem, not a 404 and a blank page. So a deploy whose key file is bad —
// which drops the whole state, leaving only a reason — still answers a lobby read
// while every mint is 503. The lobby never needed the signing key: it names
// spaces, and only the mint signs. That pair is honest rather than
// contradictory: the spaces someone belongs to do
// not stop being true because this binary cannot sign, and the refusal they get on
// joining names the real fault instead of hiding it behind an empty list.
func session(s *cloud.Service[state], c *zip.Ctx) error {
	sp, ok := s.State.spaces(c)
	if !ok {
		// Identical to mint's refusal in kind: the caller is not admitted, and the
		// answer says nothing about which lane failed or what exists.
		return zip.Errorf(http.StatusUnauthorized, "not signed in")
	}
	out := lobby{Identity: sp.Account, Name: sp.Name, WS: s.State.ws, Spaces: make([]plane.Space, 0, len(sp.Items))}
	// A space is only OFFERED if the caller could actually be seated in it, and
	// a seat needs the identity LiveKit takes it under — which is the account on the
	// row. Rows that name a space but no account would put a room in front of
	// someone that mint then refuses, which is the exact drift this route exists to
	// prevent. An empty lobby is the honest render, and it is the same answer a
	// caller with no rows at all gets, rather than a second kind of refusal.
	if sp.Account == "" {
		sp.Items = nil
	}
	for _, w := range sp.Items {
		// The SAME predicate mint admits on. Offering a space this caller holds
		// only a guest role in would put a room in front of them that getToken then
		// refuses — the two answers have to come from one rule or they drift.
		if privileged(w.Role) {
			out.Spaces = append(out.Spaces, w)
		}
	}
	return c.JSON(http.StatusOK, out)
}

// spaces reports which spaces the caller is in, on whichever lane it arrived —
// the same lane selection, in the same order and on the same attestation, as
// admits. It is deliberately a sibling of that function rather than a layer under
// it: admits answers "may this caller into THAT room" and this answers "what could
// this caller open", and folding them would make one of the two questions a
// special case of the other for no gain.
//
// IAM LANE, selected on principal.Minted for the reason admits documents at
// length: the org/user headers are the client's in a process where no boundary
// ran, and here they would decide whose spaces get listed.
func (s state) spaces(c *zip.Ctx) (plane.Spaces, bool) {
	if p, ok := principal.Minted(c); ok && p.Subject != "" && p.Org != "" {
		out, err := s.rows().spaces(cloud.As(c, p.Org), p.Subject)
		if err != nil || out == nil {
			// An unreachable authority is a refusal, never an assumption — the same
			// posture admitsMember takes when team cannot answer.
			return plane.Spaces{}, false
		}
		return *out, true
	}
	return plane.Spaces{}, false
}

// request is the office client's wire. `_id` is the SPA's person ref — accepted because
// the published bundle sends it, and IGNORED because the participant identity now comes
// from the signed token (see mint). participantName is a display name only.
type request struct {
	RoomName        string `json:"roomName"`
	ID              string `json:"_id"`
	ParticipantName string `json:"participantName"`
}

// mint answers POST /v1/meet/getToken: verify the caller belongs to the room's
// space, then hand back a join token for exactly that room.
//
// The response is the RAW token as text/plain, not JSON. That is the caller's
// contract — the office client reads it with res.text() — and it is also the honest
// shape: the body is one opaque string, so wrapping it in an object would add a
// envelope neither side needs.
func mint(s *cloud.Service[state], c *zip.Ctx) error {
	st := s.State
	if !st.ready() {
		// The CALLER gets the fact, not the plumbing: this 503 is reachable without
		// any credential, so it must not enumerate our file paths and Secret names.
		// The full reason went to the log at boot (Mount), which is where an operator
		// is looking.
		return zip.Errorf(http.StatusServiceUnavailable, "meet: the office is not configured")
	}
	var req request
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return zip.ErrBadRequest("malformed request body")
	}
	room := strings.TrimSpace(req.RoomName)
	if room == "" {
		return zip.ErrBadRequest("roomName required")
	}
	// Say what was actually checked, and it is now one of two things. On the HS256
	// arm meet performs no membership lookup: membership was decided upstream at
	// the login that minted the session and is signed into the token as
	// `space`, and all that happens here is a refusal to WIDEN it. On the IAM
	// lane there is no such claim, so the space rows are asked directly — and
	// then "not a member" IS the determination being made. One message covers both
	// because it names the fact, not the mechanism: this caller is not admitted to
	// this room.
	j, ok := st.admits(c, room)
	if !ok {
		return zip.Errorf(http.StatusUnauthorized, "not admitted to this room")
	}
	// THE IDENTITY IS THE TOKEN'S, NOT THE BODY'S. LiveKit uses `sub` as the
	// participant identity and EJECTS an existing participant on a duplicate — so
	// minting with a caller-supplied `_id` let any member of a space kick a
	// colleague out of a call by claiming their identity, and impersonate them to
	// everyone else in the room. Upstream did this too; it is still wrong. The signed
	// account is the one identity the caller cannot choose.
	//
	// The body's `_id` (the SPA's person ref) is deliberately ignored rather than
	// checked: verifying it belongs to the caller would need the person<->account
	// mapping, and the row admits already read carries the account, which IS that
	// mapping's answer. One fewer place to get it wrong.
	identity := j.account
	if identity == "" {
		return zip.Errorf(http.StatusUnauthorized, "the membership row names no account")
	}
	// A seat on the media server is the one thing here that costs money, and this
	// is where it is handed out. Authorized before the token exists, so a caller
	// who cannot cover it never holds credentials to the room. See meter.go.
	ch, err := afford(s, c.Context(), seat, fee())
	if err != nil {
		return cloud.DenyResource(c, err)
	}
	defer ch.Release()
	tok, err := st.grant(room, identity, strings.TrimSpace(req.ParticipantName), time.Now())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "meet: mint failed")
	}
	charge(ch, seat, fee())
	return c.String(http.StatusOK, tok)
}

// space is the space a room belongs to. Room names are minted client-side as
// "<spaceUuid>_<roomName>_<roomId>", so the space is the leading segment.
// This is the ONLY thing binding a room to a tenant, which is why admits compares it
// against the SIGNED space claim and not against anything in the body.
// It is the PARSING half of roomName (room.go), which composes the same shape from
// a collaboration room's own address; sep is shared so the two cannot disagree.
func space(room string) string {
	ws, _, _ := strings.Cut(room, sep)
	return ws
}

// joiner is who may join, once a lane has decided it: the account LiveKit takes as
// the participant identity, and nothing else. A lane that cannot fill it does not
// admit anyone.
type joiner struct{ account string }

// admits decides whether the caller may join room, on either of two lanes. Every
// clause is a refusal; there is no branch that admits by default.
//
// IAM LANE, selected on the boundary's OWN ATTESTATION (principal.Minted) and on
// nothing else. It used to select on `c.Org() != "" && c.User() != ""`, and both
// disjuncts are forgeable — the same defect agency.go names and fixed: X-Org-Id
// survives the boundary on the anonymous path by design, and in a process where
// the boundary is not installed at all (a hand-written plugin main, which is
// exactly what this app has) NOTHING strips either header, so both are the
// client's. Here that bought a LiveKit seat under a chosen identity, and LiveKit
// EVICTS an existing participant on a duplicate `sub` — so a forged header ejected
// a colleague from a live call and impersonated them to the room. The attestation
// is absent when no boundary ran, which falls through to the HS256 arm and refuses
// rather than admitting whatever was typed.
//
// The org and the SUBJECT are read from that attestation, never off c.Org()/
// c.User() and never off the body. p.Subject is the `sub` claim verbatim: p.User
// falls back to preferred_username, so two identities can present the same User and
// a lookup keyed on it can be handed one token and address another's row.
//
// A MACHINE CREDENTIAL IS NOT A PERSON, and the subject requirement is what
// excludes one. The boundary stamps an org and a user for an sk- API key too, so
// "has an org and a user" would have put a machine on the lane whose whole question
// is "which human is in this room" — and LiveKit would then seat it under whatever
// identity the account lookup returned. A key principal carries no `sub`, so
// requiring one refuses it structurally rather than by naming credential kinds.
//
// The verdict says nothing about a space, so the space ROWS decide: apps/team
// owns them and answers over the internal plane (plane.TeamMember) with the caller's
// role and the account id it joined the subject to. A caller with no row, or one
// whose role is not privileged, is refused, and so is a peer that cannot answer —
// an unreachable authority is a refusal, never an assumption.
func (s state) admits(c *zip.Ctx, room string) (joiner, bool) {
	p, ok := principal.Minted(c)
	if !ok || p.Subject == "" || p.Org == "" {
		return joiner{}, false
	}
	return s.admitsMember(c, room, p)
}

// admitsMember is the IAM lane's authorization: ask the process that owns the
// space rows. Both halves of the question come from the ATTESTED principal —
// the org rides the caller (never an argument, so a caller cannot ask about another
// tenant's space) and the subject is the attested `sub`.
func (s state) admitsMember(c *zip.Ctx, room string, p principal.Principal) (joiner, bool) {
	ws := space(room)
	if ws == "" {
		return joiner{}, false
	}
	m, err := s.rows().member(cloud.As(c, p.Org), ws, p.Subject)
	if err != nil || m == nil || !m.Member {
		return joiner{}, false
	}
	if !privileged(m.Role) {
		return joiner{}, false
	}
	return joiner{account: strings.TrimSpace(m.Account)}, true
}

// privileged is token.Privileged over a role the SERVER read rather than one a
// token signed. Same vocabulary, same fail-closed shape: an unknown or absent role
// is not privileged, so a role added to the invite set tomorrow starts without a
// seat in a colleague's meeting instead of silently holding one.
func privileged(role string) bool {
	switch strings.TrimSpace(role) {
	case token.RoleOwner, token.RoleAdmin, token.RoleMember:
		return true
	default:
		return false
	}
}

// video is LiveKit's VideoGrant, narrowed to the two fields a join token needs. The
// grant is deliberately minimal: roomJoin into ONE named room. Every capability
// LiveKit defaults on for a joiner (publish, subscribe) follows from that; every
// capability it does not (roomAdmin, roomCreate, roomList, recorder, ingressAdmin)
// stays off because it is not named here. A token that cannot express a privilege
// cannot leak it.
type video struct {
	RoomJoin bool   `json:"roomJoin"`
	Room     string `json:"room"`
}

// claims is the LiveKit access-token payload: RFC 7519 registered claims plus
// LiveKit's grant object. The shape is LiveKit's, not ours — it must match what the
// media server verifies, so the field set here mirrors livekit/protocol's
// auth.tokenClaims (iss=apiKey, sub=identity, iat/nbf/exp, name, video).
//
// Video is `any` because the GRANT differs by what the token is for and the
// narrowing is the whole point: a browser's token names roomJoin and one room
// (video), and the token this binary presents to LiveKit's own API names
// roomRecord (recorder, egress.go). Widening one struct to cover both would put
// every field in every token, and a token that can express a privilege can leak
// it. The registered claims are identical for both, so they are written once.
type claims struct {
	Iss   string `json:"iss"`
	Sub   string `json:"sub"`
	Iat   int64  `json:"iat"`
	Nbf   int64  `json:"nbf"`
	Exp   int64  `json:"exp"`
	Name  string `json:"name,omitempty"`
	Video any    `json:"video"`
}

// header is the fixed JOSE header for every token this package mints. HS256 is not a
// choice here — it is what LiveKit verifies a shared-secret token with. It is a
// constant rather than a field precisely so no request can influence the algorithm:
// there is no code path that could be talked into `alg: none`.
var header = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// grant mints the join token: roomJoin into exactly one room, for ttl.
func (s state) grant(room, identity, name string, now time.Time) (string, error) {
	return s.sign(identity, name, video{RoomJoin: true, Room: room}, ttl, now)
}

// sign mints a LiveKit token over one grant: a compact HS256 JWT.
//
// This composes stdlib crypto/hmac + crypto/sha256 rather than taking a JWT library,
// for two reasons. First, apps/team/token already establishes this exact idiom for
// the platform's own HS256 tokens, and a second way to make a JWT in one binary is a
// second way to get it wrong. Second, this side only ever SIGNS: the verifier is the
// LiveKit server, so the whole class of bugs a JWT library earns its keep against —
// alg confusion, `alg: none`, non-constant-time comparison — has no code path here.
// The primitives themselves are stdlib; nothing cryptographic is hand-rolled.
//
// It is ONE signer for both tokens this package mints because the difference
// between them is the GRANT and nothing else — same key, same algorithm, same
// registered claims. Two copies of this would be two places for the empty-key
// refusal below to be forgotten.
func (s state) sign(identity, name string, grant any, life time.Duration, now time.Time) (string, error) {
	// Defense in depth at the CRYPTO boundary, not just at the gate. crypto/hmac
	// accepts an empty key and returns a perfectly well-formed MAC, so an empty
	// signing key does not fail — it silently produces a token that verifies under
	// the empty key and under nothing the LiveKit server holds. Refusing here means
	// removing the ready() check upstream still cannot mint an unverifiable token.
	if s.apiKey == "" || s.apiSecret == "" {
		return "", errors.New("meet: refusing to sign with an empty LiveKit api key or secret")
	}
	payload, err := json.Marshal(claims{
		Iss:   s.apiKey,
		Sub:   identity,
		Iat:   now.Unix(),
		Nbf:   now.Unix(),
		Exp:   now.Add(life).Unix(),
		Name:  name,
		Video: grant,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(s.apiSecret))
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// unsafe is the set of methods that CHANGE something. Everything else is a read.
var unsafe = map[string]bool{
	http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true,
}

// requireCSRFOnWrites is the anti-CSRF gate for meet's one write, installed on
// the GROUP rather than on the route.
//
// THREAT, and it is not hypothetical for this endpoint. A browser authenticates
// this surface from an httpOnly session COOKIE, which is AMBIENT: any page that
// can reach us sends it. callerToken (middleware_identity.go) accepts five cookie
// names, validatedPrincipal mints from it, and admits selects the IAM lane on
// principal.Minted FIRST — so a visitor holding only __Host-hanzo_iam_token, with
// no Authorization header at all, can mint a LiveKit join token. The deployment's
// CORS policy reflects *.hanzo.ai with credentials, and that wildcard covers hosts
// serving arbitrary user content, so a page there could put a stranger into a
// colleague's call. Minting a room token IS a write: it hands out a credential to
// a live conversation.
//
// The positive control is a token the caller can only obtain by READING a
// same-origin response (GET /v1/csrf; the Same-Origin Policy stops a cross-site
// page reading it) and must echo in a CUSTOM header, which a simple form POST
// cannot set without a preflight we do not grant.
//
// This is apps/todo's gate verbatim, over apps/account's one implementation —
// the estate has ONE anti-CSRF token, minted by GET /v1/csrf and bound to the
// caller's validated identity. A second answer to a solved question is how two
// surfaces end up disagreeing about what a valid token is.
//
// It costs an API client nothing: account's gate is a no-op whenever Authorization
// is present, which covers every Bearer and gateway caller — including the native
// SPA at meet.hanzo.ai, which is cross-origin and bearer-only and sends no cookie
// at all.
func requireCSRFOnWrites() zip.Handler {
	gate := cloud.RequireCSRF()
	return func(c *zip.Ctx) error {
		if !unsafe[c.Method()] {
			return c.Next()
		}
		return gate(c)
	}
}
