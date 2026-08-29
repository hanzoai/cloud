package meet

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// sep separates the space from the rest of a room name. It is ONE constant
// because the parse and the compose below are two halves of one rule, and a rule
// spelled twice is a rule that can disagree with itself: space() reads the
// segment membership is checked against, and roomName writes it.
const sep = "_"

// roomName is the media room a collaboration room's call happens in.
//
// A room in the conversation sense (HIP-0523) is addressed by the pair (space
// uuid, room id) — the id is unique within a space and not across an org — and
// a room in the media sense is a single opaque string the media server keys on. This
// is the one function that turns the first into the second, and it is a DERIVATION
// rather than a stored binding: a room's call is a fact about the room's identity,
// so writing it down somewhere would be a copy that can drift from the room it names
// (HIP-0523 §2, a binding is a reference and never a copy).
//
// The composition is exactly what space() parses back, which is what makes the
// membership check on a derived name the same check as on a client-minted one: the
// leading segment is the space, and it is the ONLY thing binding a room to a
// tenant. splits() is why that round trip is total rather than usually-true.
func roomName(space, room string) string { return space + sep + room }

// splits reports whether a space uuid survives the round trip — that is,
// whether space(roomName(ws, id)) is ws again.
//
// It is a refusal and not a fold. A space carrying the separator would parse
// back as a PREFIX of itself, so the membership check would run against a space
// that is not the one asked about — a room in space "a_b" would be checked
// against space "a", and a caller who is a member of "a" would be admitted to a
// room in a space they are not in. Folding the character would silently map two
// spaces onto one name, which is the same defect wearing a repair.
func splits(space string) bool {
	return space != "" && !strings.Contains(space, sep)
}

// callIn addresses a room by what a room IS — the (space, room) pair — and
// never by the composed media name.
//
// Taking the pair is the whole point of the operation: the composition is the
// thing being published, so a caller that supplied it would be spelling the rule
// itself, and a second surface spelling it differently is how the two come to
// disagree about which media room a channel's call is in.
type callIn struct {
	// Space is the space uuid holding the room, as GET /v1/team/rooms
	// reports it. It is the segment the caller's membership is checked against.
	Space string `json:"space" validate:"required"`
	// Room is the room's own id within that space, as GET /v1/team/rooms
	// reports it. It is opaque here: meet keeps no rooms and cannot say whether
	// one exists, only whether this caller may be seated in the space holding
	// it.
	Room string `json:"room" validate:"required"`
}

// call is where a room's conversation happens when it is spoken rather than typed.
//
// It carries no token. Minting one is a metered act with its own wire that must not
// change (POST /v1/meet/getToken answers a raw text/plain JWT the published office
// client reads with res.text()), so this operation answers the two facts a surface
// needs to RENDER a call — which media room, and whether this deployment can seat
// anyone in it — and the caller spends them on the existing mint. Resolving is a
// read and is free; the seat is what costs.
type venue struct {
	// Name is the media room to join: the value POST /v1/meet/getToken takes as
	// roomName, and the value the media server keys participants on.
	Name string `json:"name"`
	// WS is where the media plane is — the address a client opens its own
	// browser-to-server connection to. Empty when this deployment has not been
	// told where its media server lives, which is reported rather than refused:
	// a surface can say a call is unavailable without a second request.
	WS string `json:"ws"`
	// Ready reports that this deployment can mint a join token for this room. It
	// is false on a deployment holding no media-server key, where Name is still
	// correct — the name is a property of the room and the key is a property of
	// the deployment, so a caller learns the room's identity either way and
	// learns not to offer a join button.
	Ready bool `json:"ready"`
}

// resolve answers where a room's call happens, for a caller who may join it.
//
// It is the "resolved at render" half of HIP-0523 §12: a surface showing a channel
// asks for the room's call at the moment it draws one, rather than reading a media
// room name someone stored on the room. Nothing here is persisted and nothing is
// created — a media room begins existing when the first participant connects and
// stops when the last leaves, so there is no call to create and none to clean up.
//
// AUTHORIZATION IS THE JOIN DECISION, unchanged and shared. It delegates to
// state.admits, the same function POST /v1/meet/getToken and all three recording
// operations admit on, so a caller who is told where a call is, is a caller who
// could have joined it. Answering the address to someone who cannot join would make
// this a space-membership oracle for anyone who can guess a room id.
//
// It deliberately does NOT report whether a call is in progress. That is a fact the
// media server holds and this binary would have to ask for it over the network,
// which is a different decision with a different failure mode — and reporting
// "nobody is in this call" when the question could not be asked would be exactly the
// unknown-rendered-as-zero this surface refuses elsewhere.
func (o ops) resolve(ctx context.Context, in *callIn) (*venue, error) {
	refuse := zip.Errorf(http.StatusUnauthorized, "not admitted to this room")
	c, ok := cloud.Request(ctx)
	if !ok {
		// Off the HTTP path — the CLI's local invoke and the call plane carry no
		// attested caller, and admits reads the identity boundary's own
		// attestation. A caller that cannot be attested is not in the room.
		return nil, refuse
	}
	ws := strings.TrimSpace(in.Space)
	room := strings.TrimSpace(in.Room)
	if ws == "" || room == "" {
		return nil, zip.ErrBadRequest("space and room are required")
	}
	if !splits(ws) {
		return nil, zip.ErrBadRequest("space must not contain " + sep)
	}
	name := roomName(ws, room)
	if _, ok := o.s.State.admits(c, name); !ok {
		return nil, refuse
	}
	st := o.s.State
	return &venue{Name: name, WS: st.ws, Ready: st.ready()}, nil
}
