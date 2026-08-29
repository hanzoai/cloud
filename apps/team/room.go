package team

// room.go is the ROOM as a unit of work (HIP-0523) — the read surface a
// workspace view asks, and the one write that binds a room to what it is about.
//
// The word is ROOM, not channel, and HIP-0523 §1 is why: "channel" already means
// a connected transport (slack, telegram) in apps/channels and a connected social
// account in apps/social. Room is the wire word and the specification's;
// "#bugfix-1010" is what a person calls it on screen.
//
// A team room is a Chunter document: a row in the per-workspace `docs` store
// whose `_class` is chunter:class:Channel (a named room) or
// chunter:class:DirectMessage (a room between people). Until now the ONLY way to
// reach one was the transactor WebSocket — a client opens a socket, replays the
// model, and issues findAll. That is right for the SPA, which holds a live query
// open, and it is the wrong and only door for everything else: a second surface
// reading the same rooms had to speak the document protocol to list them.
//
// So these two ops are not a second store. They read the SAME documents the
// transactor serves, through the SAME docStore, under the SAME (org, workspace)
// isolation — and they add the one fact a document store cannot hold on its own.
//
// # The work facet, and why it is a MIXIN
//
// A room that is a unit of work carries two things Chunter has no attribute
// for: whether it is meant to outlive its task, and what it is ABOUT. The
// extension point for exactly this is the mixin — a namespaced sub-object on a
// document, written by TxMixin, which the platform is built to carry on classes
// it does not own. So the facet lives at `hanzo:mixin:Work` on the room
// document itself.
//
// Three alternatives were available and each is worse:
//
//   - a column on `docs`. The table is (id, class, space, json) and every row of
//     every class shares it; a work column would be null for all but a few.
//   - a table beside it. Then a room's identity lives in two places and a
//     delete has to remember both.
//   - new attributes at the document ROOT. They would persist — txUpdate MERGES
//     operations rather than replacing the document — but they would collide with
//     the model's own namespace the day Chunter adds a field of that name.
//
// # ARCHIVED IS NOT HERE, ON PURPOSE
//
// `archived` is already an attribute of core:class:Space, which the room classes
// inherit,
// and it is what the Team client itself sets when a person archives a room.
// Adding an `archived` to this mixin would be a SECOND home for one fact, and the
// two would disagree the first time a room was archived from the SPA. So the
// facet carries lifecycle INTENT (`life`) and the platform keeps lifecycle STATE.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
)

// mixinRoom is the namespaced key the work facet is stored under on a room
// document. The `hanzo:` prefix is ours; the `:mixin:` segment is the platform's
// convention for a sub-object, which is what makes the Team client carry it
// unchanged rather than treating it as an unknown attribute.
const mixinRoom = "hanzo:mixin:Room"

// Room lifecycle intent. These say what a room is FOR, which is a
// different question from whether it is open right now.
const (
	// lifeStanding is a room meant to outlive any one task — a team's
	// standing room. It is the default for a room that says nothing.
	lifeStanding = "standing"
	// lifeBound is a room opened for one piece of work and expected to be
	// archived when that work lands (#bugfix-1010). Nothing reaps it: the intent
	// is what an archive-on-merge rule would READ, and stating it is not the same
	// as acting on it.
	lifeBound = "bound"
)

// bindMax bounds how many things one room may be about. It is a sanity bound
// on a write, not a product limit — a room naming a hundred repositories is a
// mistake being persisted, not a use case.
const bindMax = 32

// roomBridge holds the stores the room ops read. degraded is the
// fail-closed posture Mount resolved (no HS256 secret); a typed op cannot be
// wrapped by Mount's guard, so it asks for itself — see typed.go.
type roomBridge struct {
	trans    *transServer
	accounts *accountStore
	degraded bool
}

// register declares the room surface as TYPED ops, so one registry entry
// yields the route, the OpenAPI operation, the MCP tool, the CLI command and the
// SDK method.
//
// The group is built HERE, from the one prefix constant, because a typed op's
// path is its group's prefix composed with its leaf and cmd/zipdoc resolves that
// prefix from the assignment in the SAME file — a group arriving as a parameter
// is one it cannot see, and it refuses rather than filing the prose under a path
// that does not exist. Same shape bots.go uses.
func (b *roomBridge) register(app cloud.Router) {
	g := app.Group(teamPrefix)
	zip.Get(g, "/rooms", b.listRooms)
	zip.Put(g, "/rooms/:id", b.bindRoom)
}

// teamRooms is the org's rooms, newest activity irrelevant — the order is
// by name, because a list a person reads is sorted by the thing they read.
type teamRooms struct {
	// Rooms is every room of every workspace the caller's org owns, each
	// with the work facet it carries.
	Rooms []teamRoom `json:"rooms"`
}

// teamRoom is one room, as a workspace view reads it.
type teamRoom struct {
	// ID is the room document's own id, and the value the bind op addresses.
	// It is unique within a workspace, not across the org.
	ID string `json:"id"`
	// Workspace is the workspace uuid holding this room. It is part of the
	// room's address: two workspaces of one org may each hold a room with
	// the same name, and only the pair identifies one.
	Workspace string `json:"workspace"`
	// Name is what a person sees in a sidebar. A direct message carries none, so
	// this is empty for one — the members are its name.
	Name string `json:"name"`
	// Topic is the room's own one-line subject, as the Team client sets it.
	Topic string `json:"topic,omitempty"`
	// Direct reports that this is a room between people rather than a named
	// room. It is derived from the document's class, so it cannot disagree
	// with what the client will render.
	Direct bool `json:"direct"`
	// Private reports that the room is restricted to its members.
	Private bool `json:"private"`
	// Archived reports that the room has been closed. It is the platform's own
	// Space attribute — the same one the Team client writes — and NOT a field of
	// the work facet, so there is exactly one answer to "is this room open".
	Archived bool `json:"archived"`
	// Members are the account uuids in the room, agents included: an agent
	// projects as a workspace member under a uuid derived from its id, so a
	// caller comparing this against GET /v1/team/bots learns which rooms an
	// agent is in.
	Members []string `json:"members"`
	// Life is the room's lifecycle INTENT — "standing" or "bound" (HIP-0523 §2).
	// Absent on the document it reads "standing": a room nobody classified is one
	// that persists.
	Life string `json:"life"`
	// Bindings are what this room is ABOUT, each a "<kind>:<ref>" string —
	// "project:acme/web", "repo:hanzoai/cloud", "issue:1010". One list rather
	// than one field per kind, because the next thing a room can be about should
	// not be a schema change; and a bound value is opaque here on purpose, since
	// the app that owns a project is the app that can resolve one. HIP-0523 §2:
	// a binding is a REFERENCE, never a copy — a room holding an issue's title or
	// status would be the parallel work-item store HIP-1160 §1 forbids.
	Bindings []string `json:"bindings"`
}

// teamRoomBind states what a room is for. Every field is optional and an
// absent one is LEFT ALONE, so a caller that knows only the bindings does not
// have to restate the kind to avoid resetting it.
type teamRoomBind struct {
	// ID is the room to bind, from the path. The URL is the authority; a body
	// carrying another id cannot redirect the write.
	ID string `json:"id"`
	// Workspace names the workspace holding the room. It is required, because
	// a room id is unique only within one and searching every workspace for a
	// matching id would make the write's target depend on iteration order.
	Workspace string `json:"workspace" url:"-"`
	// Life sets the lifecycle intent: "standing" or "bound". Any other
	// value is refused rather than stored, so a reader never has to interpret a
	// third one. Empty leaves the current intent unchanged.
	Life string `json:"life,omitempty" url:"-"`
	// Bindings REPLACES what the room is about, wholly. It is a replace and
	// not a merge because a caller that cannot remove a binding would have no way
	// to correct a wrong one, and an empty list sent explicitly is how a room
	// is unbound. Absent (null) leaves the existing list alone.
	Bindings []string `json:"bindings,omitempty" url:"-"`
}

// ListChannels returns every room of the caller's org, across the workspaces
// it owns, with the work facet each carries.
//
// It reads the SAME Chunter documents the transactor serves, so a room opened
// in the Team client appears here with no sync, and a facet written here is read
// by anything holding the document. Direct messages are included: a room between
// two people is a room with no name, not a different kind of thing.
//
// Example: {"rooms": [{"id": "6543", "name": "bugfix-1010", "life": "bound", "bindings": ["repo:hanzoai/cloud"]}]}
func (b *roomBridge) listRooms(ctx context.Context, _ *none) (*teamRooms, error) {
	if b.degraded {
		return nil, unavailable()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	wss, err := b.accounts.WorkspacesForOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "team: workspaces: %v", err)
	}
	out := make([]teamRoom, 0, len(wss))
	for _, ws := range wss {
		docs, err := b.trans.store.byClasses(org, ws.UUID, []string{clChannel, clDirectMessage})
		if err != nil {
			// One unreadable workspace must not blank the whole list, and it must
			// not be silent either: the caller is told which one, and the rest of
			// the org's rooms still answer.
			return nil, zip.Errorf(http.StatusBadGateway, "team: rooms of workspace %s: %v", ws.UUID, err)
		}
		for _, doc := range docs {
			out = append(out, roomOf(ws.UUID, doc))
		}
	}
	// A stable order, because a list that reshuffles between two identical reads
	// is one no client can diff. Workspace first, then name, then id — the last
	// being what separates two direct messages, which carry no name at all.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Workspace != out[j].Workspace {
			return out[i].Workspace < out[j].Workspace
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return &teamRooms{Rooms: out}, nil
}

// BindChannel states what a room is for: its lifecycle intent, and what it is
// about. It answers the room as it now stands.
//
// The write is a platform MIXIN on the room document, applied through the
// SAME applyTx path the Team client's own writes take and broadcast to every
// connected client — so a room bound here updates live in an open workspace
// rather than on the next reload.
//
// Example: {"workspace": "0e3c…", "life": "bound", "bindings": ["repo:hanzoai/cloud", "issue:1010"]}
func (b *roomBridge) bindRoom(ctx context.Context, in *teamRoomBind) (*teamRoom, error) {
	if b.degraded {
		return nil, unavailable()
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	ws := strings.TrimSpace(in.Workspace)
	if id == "" || ws == "" {
		return nil, zip.ErrBadRequest("room id and workspace are required")
	}
	// The workspace must be one the caller's ORG owns. Without this the id and
	// the workspace both come from the request and the org would bound nothing —
	// a caller could name any workspace uuid and write into another tenant's
	// documents. WorkspacesForOrg is the only thing that ties the pair to the
	// validated principal.
	owned, err := b.accounts.WorkspacesForOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "team: workspaces: %v", err)
	}
	if !ownsWorkspace(owned, ws) {
		// 404, not 403: a caller who may not touch this workspace must not learn
		// whether it exists.
		return nil, zip.ErrNotFound("room not found")
	}
	work, err := workOf(in)
	if err != nil {
		return nil, err
	}
	doc, err := b.trans.store.get(org, ws, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: room: %v", err)
	}
	if doc == nil || !isRoom(str(doc["_class"])) {
		return nil, zip.ErrNotFound("room not found")
	}
	// Written as a mixin tx rather than by mutating the document here, so the one
	// write path stays the one write path: applyTx persists it, the triggers and
	// notification projections run, and the applied tx is broadcast to every live
	// client of the workspace.
	tx := map[string]any{
		"_class":      clTxMixin,
		"objectId":    id,
		"objectClass": str(doc["_class"]),
		"objectSpace": str(doc["space"]),
		"mixin":       mixinRoom,
		"attributes":  work,
		"modifiedBy":  acctSystem,
		"modifiedOn":  time.Now().UnixMilli(),
	}
	raw, err := json.Marshal(tx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "team: bind: %v", err)
	}
	sess := &session{server: b.trans, store: b.trans.store, hier: b.trans.hier, org: org, workspace: ws, account: acctSystem}
	_, applied := sess.applyTx(raw)
	if len(applied) > 0 {
		b.trans.hub.broadcast(ws, applied)
	}
	// Re-read rather than projecting what we just sent: the answer is what the
	// store now HOLDS, which is the only thing a caller can rely on, and it is
	// how a partially-applied write shows up as one.
	after, err := b.trans.store.get(org, ws, id)
	if err != nil || after == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: room after bind: %v", err)
	}
	v := roomOf(ws, after)
	return &v, nil
}

// workOf builds the mixin attributes one bind states, refusing anything a reader
// would then have to interpret. A nil map means the request stated nothing,
// which is a no-op write rather than an error — re-binding a room to what it
// already is should not fail.
func workOf(in *teamRoomBind) (map[string]any, error) {
	work := map[string]any{}
	switch kind := strings.TrimSpace(in.Life); kind {
	case "":
		// unstated — leave whatever the room carries
	case lifeStanding, lifeBound:
		work["life"] = kind
	default:
		return nil, zip.ErrBadRequest(fmt.Sprintf("life must be %q or %q", lifeStanding, lifeBound))
	}
	if in.Bindings != nil {
		if len(in.Bindings) > bindMax {
			return nil, zip.ErrBadRequest(fmt.Sprintf("at most %d bindings", bindMax))
		}
		seen := map[string]bool{}
		binds := make([]any, 0, len(in.Bindings))
		for _, raw := range in.Bindings {
			bind := strings.TrimSpace(raw)
			if bind == "" {
				continue
			}
			// A binding is "<kind>:<ref>" and BOTH halves must be present. The
			// shape is checked and the value is not: the app that owns projects is
			// the app that can say whether a project exists, and resolving one here
			// would put team in the business of knowing every other plane's nouns.
			k, ref, ok := strings.Cut(bind, ":")
			if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(ref) == "" {
				return nil, zip.ErrBadRequest("each binding is \"<kind>:<ref>\", e.g. \"repo:hanzoai/cloud\"")
			}
			if seen[bind] {
				continue // a set, stated as a list
			}
			seen[bind] = true
			binds = append(binds, bind)
		}
		work["bindings"] = binds
	}
	return work, nil
}

// roomOf projects one stored room document to the published shape. It
// reads the work facet where the document carries one and defaults where it does
// not, so a room that predates the facet reads as an ordinary persistent
// room rather than as a hole.
func roomOf(workspace string, doc map[string]any) teamRoom {
	out := teamRoom{
		ID:        str(doc["_id"]),
		Workspace: workspace,
		Name:      str(doc["name"]),
		Topic:     str(doc["topic"]),
		Direct:    str(doc["_class"]) == clDirectMessage,
		Private:   truthy(doc["private"]),
		Archived:  truthy(doc["archived"]),
		Members:   toStringSlice(doc["members"]),
		Life:      lifeStanding,
		Bindings:  []string{},
	}
	if out.Members == nil {
		out.Members = []string{}
	}
	work, _ := doc[mixinRoom].(map[string]any)
	if work == nil {
		return out
	}
	if k := str(work["life"]); k == lifeBound || k == lifeStanding {
		out.Life = k
	}
	if b := toStringSlice(work["bindings"]); len(b) > 0 {
		out.Bindings = b
	}
	return out
}

// isRoom reports whether a document class is a room — a named room or a
// direct message. It is the ONE place the two classes are treated as one noun,
// which is what keeps "everything is a room" true of this surface.
func isRoom(class string) bool { return class == clChannel || class == clDirectMessage }

// ownsWorkspace reports whether uuid is one of the org's own workspaces.
func ownsWorkspace(owned []workspace, uuid string) bool {
	for _, ws := range owned {
		if ws.UUID == uuid {
			return true
		}
	}
	return false
}

// truthy coerces a stored JSON boolean. A missing or non-boolean value is false,
// which is the safe reading for both fields that use it: an unclassified room
// is neither private nor archived.
func truthy(v any) bool {
	b, _ := v.(bool)
	return b
}
