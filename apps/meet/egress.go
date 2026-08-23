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

// egress.go is the wire to LiveKit's Egress API — the ONE peer that can record a
// room. record.go decides who may; this decides nothing.
//
// LIVEKIT IS ALSO THE STATE. This binary keeps no row saying which room is being
// recorded, on purpose: cloud runs several replicas, a recording outlives the
// request that started it, and the process that actually owns a recording is the
// one running it. A local map would be one replica's opinion, wrong the moment a
// second pod answered — so "is this room being recorded" is ASKED (ListEgress),
// never remembered.
//
// The API is Twirp over JSON at /twirp/livekit.Egress/<Method>, which is what the
// media server already serves beside the WebSocket the browser dials. Speaking it
// directly rather than through livekit/server-sdk-go is the same choice meet.go
// makes for the token: the SDK's whole value is the protobuf types and the JWT,
// and this package already mints the JWT from stdlib because the LiveKit server is
// the only verifier. Four JSON messages is a smaller thing to own than a protobuf
// toolchain.
//
// TWO SPELLINGS ON THE WAY BACK. Twirp's response marshaler emits the PROTO field
// names (egress_id) by default and the JSON ones (egressId) when the server was
// built with camelCase, and a deployment picks which without telling its clients —
// so every multi-word field is read by both names (see answer). Requests need no
// such care: Twirp decodes with protojson, which accepts either.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/s3admin"
	s3 "github.com/hanzos3/go"
)

// twirp is the mount LiveKit's Egress service answers on. It is protocol, not
// deployment: livekit/protocol's generated EgressPathPrefix is this string.
const twirp = "/twirp/livekit.Egress/"

// bucket is where every recording in this deployment lands.
//
// ONE bucket with an org-scoped key prefix, not a bucket per tenant. That is the
// estate's existing shape for a store whose objects are small in number and
// server-named — team-blobs, org-db, hanzo-sites all do it — and it is the right
// one here for a reason particular to recordings: the writer is the LiveKit egress
// worker, which is handed a bucket at start and cannot create one, so a per-tenant
// bucket would make the FIRST recording in a new org fail unless something else
// had provisioned it first.
const bucket = "recordings"

// apiTTL is how long the token this binary presents to LiveKit's own API is good
// for. One minute: it is used for exactly one call, immediately, and LiveKit's
// verifier already allows a minute of clock skew either side. The join token's ten
// minutes exist for a browser that has to complete a handshake; nothing here has
// to survive a bad connection.
const apiTTL = time.Minute

// starting is EgressStatus's ZERO value, and naming it here is load-bearing rather
// than tidy: proto3's JSON mapping omits a field at its default, so an EgressInfo
// with no `status` at all is not an EgressInfo missing its state — it is one in
// this state. See answer.enum.
const starting = "EGRESS_STARTING"

// maxAnswer caps what is read back from the media server. An EgressInfo is a few
// hundred bytes and a full list of them a few thousand; a megabyte is far past any
// honest answer and stops a wedged peer from being an allocation.
const maxAnswer = 1 << 20

// egressHTTP is the one outbound client for this peer. The timeout is what makes
// a hung media server a refusal instead of a held request: starting an egress is a
// control call that either lands quickly or is not going to.
var egressHTTP = &http.Client{Timeout: 15 * time.Second}

// egress is the recording plane: where LiveKit's API is, and where a finished file
// lands. reason is the ONE flag, exactly as state.reason is — a non-empty reason IS
// "recording is not configured here", so the two cannot disagree.
type egress struct {
	api    string        // LiveKit's HTTP origin, derived from LIVEKIT_WS
	s3     s3admin.Admin // the object store a finished file lands in
	reason string        // why recording is unavailable; empty means available
}

// ready reports whether this deployment can record. Fail-closed, and the refusal
// upstream states the reason rather than pretending to record into nowhere.
func (e egress) ready() bool { return e.reason == "" }

// egressOf derives the recording plane from deployment facts that already exist.
//
// THE ADDRESS IS NOT A NEW KNOB. LIVEKIT_WS already names the media server whose
// tokens this binary signs, and the Egress API is that same server over HTTP —
// LiveKit serves both on one port, which is why every LiveKit client derives the
// API origin from the socket address instead of taking a second one. A separate
// variable could name a different server, and a token minted for one is refused by
// the other: the same silent failure the single key file exists to prevent.
func egressOf(ws string) egress {
	if ws == "" {
		return egress{reason: wsEnv + " is unset, so this deployment does not know where its media server is"}
	}
	api, err := origin(ws)
	if err != nil {
		return egress{reason: err.Error()}
	}
	store := s3admin.New()
	if !store.Configured() {
		return egress{reason: "no object store is configured (K8s Secret hanzo-s3: S3_ADMIN_ACCESS_KEY, S3_ADMIN_SECRET_KEY), so a recording would have nowhere to land"}
	}
	return egress{api: api, s3: store}
}

// ensure creates the recordings bucket if it is not there yet, so the FIRST
// recording a deployment ever makes does not upload into nothing.
//
// It is here rather than left to an operator because the failure it prevents is
// the worst shape this surface has: the egress worker is handed a bucket at start,
// discovers it is missing at UPLOAD, and by then the meeting has been recorded and
// the file is gone. A HEAD before the start turns that into an honest refusal.
func (e egress) ensure(ctx context.Context) error {
	cli, err := e.s3.Client()
	if err != nil {
		return err
	}
	ok, err := cli.BucketExists(ctx, bucket)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if err := cli.MakeBucket(ctx, bucket, s3.MakeBucketOptions{Region: e.s3.Region()}); err != nil {
		// Two replicas starting the first recording of a deployment race here, and
		// the loser of that race must not refuse a caller: re-ask, and only the
		// answer "still absent" is a failure.
		if exists, _ := cli.BucketExists(ctx, bucket); !exists {
			return err
		}
	}
	return nil
}

// origin turns the browser's signaling address into the origin the media server's
// HTTP API answers on: same host, same port, the scheme that pairs with the
// socket's.
func origin(ws string) (string, error) {
	u, err := url.Parse(ws)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%s is %q, which is not a URL; it must be the ws:// or wss:// address of the media server", wsEnv, ws)
	}
	switch u.Scheme {
	case "wss", "https":
		return "https://" + u.Host, nil
	case "ws", "http":
		return "http://" + u.Host, nil
	default:
		return "", fmt.Errorf("%s has scheme %q; it must be ws:// or wss://", wsEnv, u.Scheme)
	}
}

// recorder is LiveKit's VideoGrant narrowed to what an Egress API call needs, and
// it is a DIFFERENT type from video (the join grant) so that neither can grow the
// other's fields. A join token cannot express roomRecord and this one cannot
// express roomJoin, so neither can be replayed as the other.
//
// Room is named even though LiveKit's record permission is a bare boolean today —
// its egress service checks roomRecord and does not compare the room. So this
// token states its scope without that statement being enforced anywhere but here:
// what actually binds a recording to a room is admits, in record.go, on the way
// in. Naming it costs one field and bounds the token the day LiveKit reads it.
type recorder struct {
	RoomRecord bool   `json:"roomRecord"`
	Room       string `json:"room"`
}

// token mints the credential for one Egress API call, attributed to the person who
// asked for it: LiveKit records `sub` as the identity behind the request, so an
// egress in its logs names a human rather than the deployment.
func (s state) token(room, identity string, now time.Time) (string, error) {
	return s.sign(identity, "", recorder{RoomRecord: true, Room: room}, apiTTL, now)
}

// info is EgressInfo narrowed to what this surface reports: which recording, of
// what, how it is going, and where the file is.
type info struct {
	ID      string
	Room    string
	Status  string // LiveKit's own name: EGRESS_STARTING, EGRESS_ACTIVE, EGRESS_COMPLETE, …
	Started int64  // the media server's own started_at, verbatim (see recording.Started)
	Object  string // the key LiveKit reports for the file, empty until it names one
	Error   string
}

// live reports whether this recording is still running. LiveKit's terminal states
// are COMPLETE, FAILED, ABORTED and LIMIT_REACHED; anything else is in flight.
//
// A DENYLIST, not an allowlist of the running states, so a state LiveKit adds
// tomorrow reads as running. That is the safe side and it is not free, so the cost
// is worth naming: an unknown TERMINAL state WEDGES a room. start hands back the
// stale entry instead of recording, and stop asks the media server to end something
// already ended and answers its refusal — until this build learns the state. That is
// an availability fault, it is visible in the answer, and an operator can see it.
// The other direction is two recorders on one live conversation, which is a safety
// fault nobody sees.
func (i info) live() bool {
	switch i.Status {
	case "EGRESS_COMPLETE", "EGRESS_FAILED", "EGRESS_ABORTED", "EGRESS_LIMIT_REACHED":
		return false
	default:
		return true
	}
}

// start asks LiveKit to record room as one composed file at key.
func (e egress) start(ctx context.Context, token, room, key string) (info, error) {
	// Defense in depth at the boundary that hands out a credential, not only at the
	// gate: ready() has already refused an unconfigured deployment, and this refuses
	// again rather than posting an S3 block with empty keys — which LiveKit would
	// accept, and then fail to upload after the meeting had been recorded.
	up, ok := e.s3.Store()
	if !ok {
		return info{}, errors.New("no object store is configured")
	}
	got, err := e.one(ctx, token, "StartRoomCompositeEgress", map[string]any{
		"roomName": room,
		// ONE output, and it is a file rather than a stream: a recording is an
		// artifact somebody watches later, and a live stream is a different product
		// with a different consent story.
		"fileOutputs": []any{map[string]any{
			"fileType": "MP4",
			"filepath": key,
			// The sidecar manifest is a second object naming the same recording,
			// with no reader here. Not writing it keeps the bucket's contents equal
			// to what this surface reports.
			"disableManifest": true,
			// The egress worker uploads the file ITSELF, so it is handed the
			// store's credential rather than a URL to push through. This gateway
			// exposes no STS endpoint, so there is no scoped or expiring key to
			// hand over instead: what crosses here is the deployment's whole
			// object store, to an in-cluster worker we run.
			"s3": map[string]any{
				"accessKey":      up.AccessKey,
				"secret":         up.Secret,
				"region":         up.Region,
				"endpoint":       up.URL,
				"bucket":         bucket,
				"forcePathStyle": up.PathStyle,
			},
		}},
	})
	if err != nil {
		return info{}, err
	}
	// A 200 IS NOT A YES. Twirp answers 200 with whatever EgressInfo the service
	// produced, so a body naming a terminal state — or naming nothing at all — is a
	// recording that was never made. Reading one as success handed the caller an
	// object path for a file nobody would write, and billed them for it.
	if got.ID == "" || !got.live() {
		return info{}, refused{Code: "not_started", Msg: "the media server answered 200 with status " +
			quote(got.Status) + " and error " + quote(got.Error)}
	}
	return got, nil
}

// quote renders a possibly-empty peer string for a log line without letting an
// empty one read as a missing field.
func quote(s string) string {
	if s == "" {
		return `""`
	}
	return `"` + s + `"`
}

// stop ends one recording by id.
func (e egress) stop(ctx context.Context, token, id string) (info, error) {
	return e.one(ctx, token, "StopEgress", map[string]any{"egressId": id})
}

// listing is what the media server said about one room: the recordings it named as
// THAT room's, and how many entries it sent that could not be judged at all.
//
// The count is not bookkeeping. An entry naming no room, no state or no id might be
// this room's live recording, and dropping one silently means answering "this room
// is not being recorded" without having shown it — after which stop reports 200
// over a recording that is still writing, and the next start puts a second recorder
// beside it. Carried out, so the ops can refuse instead of reporting a clean room.
type listing struct {
	all     []info
	unknown int
}

// list answers every recording the media server holds for a room.
//
// The `active` filter LiveKit offers is deliberately NOT sent. Which of these is
// running is decided HERE, by live(), and asking the peer to pre-filter would put
// the same question in two places — where the answers differ the day LiveKit
// widens what "active" means. It is also the wrong question for a read: a
// recording that has FINISHED is exactly the one whose location somebody wants.
func (e egress) list(ctx context.Context, token, room string) (listing, error) {
	body, err := e.post(ctx, token, "ListEgress", map[string]any{"roomName": room})
	if err != nil {
		return listing{}, err
	}
	var out struct {
		Items []answer `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return listing{}, errors.New("the media server's answer was not the shape its API declares")
	}
	var got listing
	for _, item := range out.Items {
		one := item.info()
		// A recording that NAMES another room is not ours and is not a doubt: the
		// peer simply answered more widely than it was asked. Ignored, not counted.
		if one.Room != "" && one.Room != room {
			continue
		}
		// THE ROOM IS CHECKED ON THE WAY BACK, not merely asked for on the way out.
		// roomName is one argument to a remote peer, and a peer that does not honour
		// it — a proxy, a version change, a compromised one — would otherwise hand
		// this surface another tenant's recording to report and to stop.
		//
		// What is left here is an entry that MIGHT be this room's and cannot be shown
		// to be, or cannot be acted on if it is: no room to attribute it, no id to
		// stop it with. Each is counted rather than dropped, because "nothing found"
		// and "nothing I could read" are different answers and only the first one
		// means the room is free.
		//
		// A missing STATE is not in that set. An absent proto3 enum means its zero
		// value (see answer.enum), so an entry with no status field is a recording
		// that has just started — the most ordinary answer there is, and counting it
		// as unreadable made every start against such a peer refuse and every room it
		// touched unstoppable.
		if one.ID == "" || one.Room == "" {
			got.unknown++
			continue
		}
		got.all = append(got.all, one)
	}
	return got, nil
}

// one makes a call whose answer is a single EgressInfo.
func (e egress) one(ctx context.Context, token, method string, in any) (info, error) {
	body, err := e.post(ctx, token, method, in)
	if err != nil {
		return info{}, err
	}
	var a answer
	if err := json.Unmarshal(body, &a); err != nil {
		return info{}, errors.New("the media server's answer was not the shape its API declares")
	}
	return a.info(), nil
}

// post is the one exchange with the media server.
func (e egress) post(ctx context.Context, token, method string, in any) ([]byte, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.api+twirp+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := egressHTTP.Do(req)
	if err != nil {
		// The transport error is NOT carried out. *url.Error prints the URL it
		// failed on, and that URL names this deployment's internal media host in
		// an answer a caller reads.
		return nil, errors.New("the media server did not answer")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, errors.New("the media server's answer could not be read")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, refusal(raw)
	}
	return raw, nil
}

// refused is a Twirp error: the media server's own vocabulary for why it would
// not do this. Code is what record.go turns into a status; Msg is what it tells
// the caller, verbatim, because "no available egress instances" is exactly the
// plain answer an operator with no egress worker deployed needs to read.
type refused struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

func (r refused) Error() string {
	if r.Msg == "" {
		return r.Code
	}
	return r.Msg
}

// refusal decodes a Twirp error body, and names the shape when it cannot: a
// non-200 that is not a Twirp error is a proxy or an ingress answering in the
// media server's place, and saying "the media server refused" of it would be a
// diagnosis nobody can act on.
func refusal(raw []byte) error {
	var r refused
	if err := json.Unmarshal(raw, &r); err != nil || r.Code == "" {
		return refused{Code: "unknown", Msg: "the media server answered something other than its own API"}
	}
	// The code is the only part that reaches a caller, so it is bounded HERE, at the
	// one place a peer's bytes become a value: Twirp's codes are lowercase words, and
	// anything else is a peer writing something of its own choosing into our answer.
	r.Code = code(r.Code)
	return r
}

// code narrows a Twirp error code to the shape one has. Anything outside it is a
// peer saying something that is not a code, and is reported as exactly that.
func code(s string) string {
	const max = 32
	if len(s) > max {
		return "unknown"
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '_' {
			return "unknown"
		}
	}
	return s
}

// answer is one Twirp JSON object read by either spelling of each field (see the
// note at the top of this file). It exists instead of a struct with tags because
// a struct can carry only one of the two names.
type answer map[string]json.RawMessage

// raw is the first of these names the object actually carries.
func (a answer) raw(names ...string) json.RawMessage {
	for _, n := range names {
		if v, ok := a[n]; ok && string(v) != "null" {
			return v
		}
	}
	return nil
}

func (a answer) text(names ...string) string {
	var s string
	_ = json.Unmarshal(a.raw(names...), &s)
	return s
}

// enum reads a proto3 enum, where an ABSENT field MEANS its zero value.
//
// This is the same axis as number below and it was the half that got missed: the
// two ways a conforming proto3 JSON encoder may differ from the obvious reading
// are the NAMES it uses (handled by raw, which accepts both spellings) and whether
// it emits a field sitting at its default (handled here). Both are the encoder's
// promise rather than something this side can check, so both are read rather than
// assumed.
//
// The cost of getting this one wrong is total, which is why it is read even though
// stock Twirp does emit defaults. EgressStatus's zero value is EGRESS_STARTING, so
// a peer that omits it is describing a recording that is RUNNING — and treating
// that as an unreadable entry refuses the start while the worker runs, then makes
// the room permanently unattributable and the recording impossible to stop.
func (a answer) enum(name, zero string) string {
	if got := a.text(name); got != "" {
		return got
	}
	return zero
}

// number reads a 64-bit integer. proto3's JSON mapping carries one as a STRING,
// so that is read first; a bare number is accepted too, because that mapping is
// the encoder's promise rather than something this side can check.
func (a answer) number(names ...string) int64 {
	v := a.raw(names...)
	var s string
	if json.Unmarshal(v, &s) == nil {
		n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return n
	}
	var n int64
	_ = json.Unmarshal(v, &n)
	return n
}

// info lifts the fields this surface reports off one EgressInfo.
func (a answer) info() info {
	got := info{
		ID:      a.text("egress_id", "egressId"),
		Room:    a.text("room_name", "roomName"),
		Status:  a.enum("status", starting),
		Started: a.number("started_at", "startedAt"),
		Error:   a.text("error"),
	}
	// The file LiveKit says it is writing, which is authoritative over the key we
	// asked for: a caller reading a recording this process did not start has no
	// other way to learn it, since nothing here remembers one.
	var files []answer
	if err := json.Unmarshal(a.raw("file_results", "fileResults"), &files); err == nil {
		for _, f := range files {
			if name := f.text("filename"); name != "" {
				got.Object = name
				break
			}
		}
	}
	return got
}
