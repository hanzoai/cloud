package team

// access.go is the ONE answer to "may this caller touch this channel, message or
// document", asked by every typed op that addresses one.
//
// Two facts decide it and they live in two places. WHETHER A PERSON IS IN A SPACE
// is IAM's (a membership row, read by identity.seat). WHETHER THEY ARE IN A CHANNEL
// OR TEAMSPACE is the document's own `members` list — the same attribute the Team
// client writes when somebody joins, so the web front and this surface cannot
// disagree about who is in #general.
//
// The rule, stated once:
//
//   - the caller must hold a member row in the space (identity.seat);
//   - a PUBLIC channel is open to every non-guest member of the space;
//   - a private channel, a direct message and a private teamspace are open to
//     the account uuids in their `members` list and to nobody else;
//   - a guest reaches only what names them.
//
// Every refusal is a 404, so a probe cannot tell "no such channel" from "not yours".

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk/principal"
)

// Space roles, as IAM files them on a space membership.
const (
	roleOwner  = "owner"
	roleAdmin  = "admin"
	roleMember = "member"
)

// seat is a verified caller who has entered one space: who they are, which
// space, and the role IAM gives them there.
type seat struct {
	caller
	// ws is the space uuid the caller entered.
	ws string
	// role is the caller's IAM role in ws.
	role string
}

// admin reports that the caller administers the space — the role that may
// remove another person's message or another member from a channel. It is the
// SPACE's role, never platform authority: an org admin running their own space
// is self-service, and nothing here reads the platform SuperAdmin bit.
func (s seat) admin() bool { return s.role == roleOwner || s.role == roleAdmin }

// guest reports that the caller is a guest of the space.
func (s seat) guest() bool { return s.role == roleGuest }

// social is the caller's platform person id, the value `createdBy` carries.
func (s seat) social() string { return "hanzo:" + s.account }

// seat authorizes cl for one space and answers its row AND the caller's role.
// It is admit with the role kept, and admit is written in terms of it, so there
// is one membership rule however a surface asks.
func (id *identity) seat(ctx context.Context, cl caller, wsUUID string) (space, string, error) {
	if id == nil || id.accounts == nil {
		return space{}, "", errNoSpace
	}
	// A caller with no tenant, or none with an account, names nothing to be a member
	// of — and an empty org is a value the owner_org scoping would happily match a
	// row against. Refused here, once, so every surface inherits the same floor.
	if cl.org == "" || cl.account == "" {
		return space{}, "", errNoSpace
	}
	w, err := id.accounts.SpaceByUUID(ctx, cl.org, strings.TrimSpace(wsUUID))
	if err != nil {
		return space{}, "", errNoSpace
	}
	role, ok := id.accounts.Membership(ctx, cl.org, w.UUID, cl.account)
	if !ok {
		return space{}, "", errNoSpace
	}
	return w, role, nil
}

// enter resolves the verified caller of a typed op and admits them to the
// space they named — or, when they named none, to the only space they are a
// member of. A caller in several spaces must name one: picking for them would
// make where a write lands depend on iteration order.
//
// An HS256 space token pins its space, and a request naming another one is
// refused as though that space did not exist.
func enter(ctx context.Context, id *identity, named string) (seat, error) {
	cl, err := callerOf(ctx, id)
	if err != nil || cl.org == "" || cl.account == "" {
		return seat{}, zip.ErrUnauthorized("team: a signed-in team member is required")
	}
	return enterAs(ctx, id, cl, named)
}

// enterAs is enter for a caller already resolved — the event stream's, which
// is an untyped handler holding its request rather than a context.
func enterAs(ctx context.Context, id *identity, cl caller, named string) (seat, error) {
	ws := strings.TrimSpace(named)
	if ws == "" {
		ws = cl.space
	}
	if ws == "" {
		mine, err := id.accounts.SpacesOf(ctx, cl.org, cl.account)
		if err != nil {
			return seat{}, zip.Errorf(http.StatusBadGateway, "team: spaces: %v", err)
		}
		switch len(mine) {
		case 0:
			return seat{}, zip.ErrNotFound("space not found")
		case 1:
			ws = mine[0].UUID
		default:
			return seat{}, zip.ErrBadRequest("you are in more than one space: name the one this is about")
		}
	}
	if cl.space != "" && cl.space != ws {
		return seat{}, zip.ErrNotFound("space not found")
	}
	w, role, err := id.seat(ctx, cl, ws)
	if err != nil {
		return seat{}, zip.ErrNotFound("space not found")
	}
	return seat{caller: cl, ws: w.UUID, role: role}, nil
}

// teamCaller reports whether the request carries a verified TEAM credential —
// one that resolves to an account in an org. It decides which lane a read that
// also serves org principals takes, and nothing else: a credential that does not
// verify is not a team caller, it is not a second chance at being one.
func teamCaller(ctx context.Context, id *identity) (caller, bool) {
	cl, err := callerOf(ctx, id)
	if err != nil || cl.org == "" || cl.account == "" {
		return caller{}, false
	}
	return cl, true
}

// orgLane answers the org an APPLICATION principal acts for — the only caller
// the channel surface serves without a team session. Anybody else with no team
// credential is refused: a person reaches channels as a member or not at all.
func orgLane(ctx context.Context) (string, error) {
	if !application(ctx) {
		return "", zip.ErrUnauthorized("team: a signed-in team member is required")
	}
	return principal.Acting(ctx)
}

// seatsOf admits the caller to the space they named — or, naming none, to every
// space they are a member of. A space token pins one space, and that is the
// only one it reaches.
func seatsOf(ctx context.Context, id *identity, named string) ([]seat, error) {
	cl, ok := teamCaller(ctx, id)
	if !ok {
		return nil, zip.ErrUnauthorized("team: a signed-in team member is required")
	}
	return seatsFor(ctx, id, cl, named)
}

// seatsFor is seatsOf for a caller already resolved.
func seatsFor(ctx context.Context, id *identity, cl caller, named string) ([]seat, error) {
	if strings.TrimSpace(named) != "" || cl.space != "" {
		s, err := enterAs(ctx, id, cl, named)
		if err != nil {
			return nil, err
		}
		return []seat{s}, nil
	}
	mine, err := id.accounts.SpacesOf(ctx, cl.org, cl.account)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: spaces: %v", err)
	}
	out := make([]seat, 0, len(mine))
	for _, w := range mine {
		// Re-asked per space rather than trusted from the list: the list says a row
		// exists, the seat says what it grants, and one rule decides both.
		_, role, err := id.seat(ctx, cl, w.UUID)
		if err != nil {
			continue
		}
		out = append(out, seat{caller: cl, ws: w.UUID, role: role})
	}
	return out, nil
}

// openTo is THE visibility rule, for a person holding `role` in the space: a
// public channel or teamspace — and anything that is simply the space's — is
// open to every non-guest member; a private one, a direct message, and anything
// at all for a guest, only to the account uuids in its members list. The typed
// ops ask it of their caller (seat), the notification trigger of each recipient
// (audience), and the socket of its session — one rule, three askers.
func openTo(role, account string, container map[string]any) bool {
	if container == nil {
		return false
	}
	if str(container["_class"]) != clDirectMessage && !truthy(container["private"]) && role != roleGuest {
		return true
	}
	return slices.Contains(toStringSlice(container["members"]), account)
}

// canSee reports whether the caller may read a channel (a Channel or a
// DirectMessage document).
func (s seat) canSee(channel map[string]any) bool { return openTo(s.role, s.account, channel) }

// inChannel reports whether the caller's account is in a channel's (or teamspace's)
// own members list.
func (s seat) inChannel(channel map[string]any) bool {
	return slices.Contains(toStringSlice(channel["members"]), s.account)
}

// mayWrite reports whether the caller may say something in a channel: exactly who
// may read it, and only while it is open. An archived channel is read-only for
// everyone, which is what archiving it means.
func (s seat) mayWrite(channel map[string]any) error {
	if !s.canSee(channel) {
		return zip.ErrNotFound("channel not found")
	}
	if truthy(channel["archived"]) {
		return zip.Errorf(http.StatusConflict, "team: the channel is archived")
	}
	return nil
}

// visible reports whether the caller may read what a message lives in: a channel,
// or a document (whose teamspace decides).
func (b *channelBridge) visible(s seat, where map[string]any) bool {
	switch cls := str(where["_class"]); {
	case isChannel(cls):
		return s.canSee(where)
	case cls == clDocument:
		_, err := b.teamspace(s, str(where["space"]))
		return err == nil
	}
	return false
}

// writable reports whether the caller may add to what a message lives in —
// visible, and not archived.
func (b *channelBridge) writable(s seat, where map[string]any) error {
	switch cls := str(where["_class"]); {
	case isChannel(cls):
		return s.mayWrite(where)
	case cls == clDocument:
		ts, err := b.teamspace(s, str(where["space"]))
		if err != nil {
			return err
		}
		if truthy(ts["archived"]) {
			return zip.Errorf(http.StatusConflict, "team: the teamspace is archived")
		}
		return nil
	}
	return zip.ErrNotFound("not found")
}

// channel resolves a channel document in the caller's space that the caller may
// read, answering 404 for anything else.
func (b *channelBridge) channel(s seat, id string) (map[string]any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, zip.ErrBadRequest("channel id is required")
	}
	doc, err := b.trans.store.get(s.org, s.ws, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: channel: %v", err)
	}
	if doc == nil || !isChannel(str(doc["_class"])) || !s.canSee(doc) {
		return nil, zip.ErrNotFound("channel not found")
	}
	return doc, nil
}

// session is a write session in the caller's space, attributed to the caller.
func (b *channelBridge) session(s seat) *session {
	return &session{server: b.trans, store: b.trans.store, hier: b.trans.hier, org: s.org, space: s.ws, account: s.account}
}
