package team

// channel.go is the CHANNEL as a unit of work (HIP-0523) — the read surface a
// space view asks, and the one write that binds a channel to what it is about.
//
// The word is CHANNEL, from the screen to the store (HIP-0523 §1): the Team
// client draws channels and the store's class is chunter:class:Channel. The
// network a message travels over is a TRANSPORT (apps/transport), so the one word
// never means two things. "#bugfix-1010" is what a person calls one on screen.
//
// A team channel is a Chunter document: a row in the per-space `docs` store
// whose `_class` is chunter:class:Channel (a named channel) or
// chunter:class:DirectMessage (a channel between people). Until now the ONLY way to
// reach one was the transactor WebSocket — a client opens a socket, replays the
// model, and issues findAll. That is right for the SPA, which holds a live query
// open, and it is the wrong and only way in for everything else: a second surface
// reading the same channels had to speak the document protocol to list them.
//
// So these two ops are not a second store. They read the SAME documents the
// transactor serves, through the SAME docStore, under the SAME (org, space)
// isolation — and they add the one fact a document store cannot hold on its own.
//
// # The work facet, and why it is a MIXIN
//
// A channel that is a unit of work carries two things Chunter has no attribute
// for: whether it is meant to outlive its task, and what it is ABOUT. The
// extension point for exactly this is the mixin — a namespaced sub-object on a
// document, written by TxMixin, which the platform is built to carry on classes
// it does not own. So the facet lives at `hanzo:mixin:Channel` on the channel
// document itself.
//
// Three alternatives were available and each is worse:
//
//   - a column on `docs`. The table is (id, class, space, json) and every row of
//     every class shares it; a work column would be null for all but a few.
//   - a table beside it. Then a channel's identity lives in two places and a
//     delete has to remember both.
//   - new attributes at the document ROOT. They would persist — txUpdate MERGES
//     operations rather than replacing the document — but they would collide with
//     the model's own namespace the day Chunter adds a field of that name.
//
// # ARCHIVED IS NOT HERE, ON PURPOSE
//
// `archived` is already an attribute of core:class:Space, which the channel classes
// inherit,
// and it is what the Team client itself sets when a person archives a channel.
// Adding an `archived` to this mixin would be a SECOND home for one fact, and the
// two would disagree the first time a channel was archived from the SPA. So the
// facet carries lifecycle INTENT (`life`) and the platform keeps lifecycle STATE.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
)

// mixinChannel is the namespaced key the work facet is stored under on a channel
// document. The `hanzo:` prefix is ours; the `:mixin:` segment is the platform's
// convention for a sub-object, which is what makes the Team client carry it
// unchanged rather than treating it as an unknown attribute.
const mixinChannel = "hanzo:mixin:Channel"

// mixinRenamed is where the facet lived before HIP-0523 §2.1 named the
// conversation a channel. docStore.db rewrites it to mixinChannel the first time
// a process opens a space, so no reader here ever meets it.
const mixinRenamed = "hanzo:mixin:Room"

// spaceChannels is the model space a channel DOCUMENT lives in. A channel is itself a
// Space in this model, and every Space is filed under core's own — so this is
// the document's home, not the tenant space the channel belongs to. That second
// one is the (org, space) pair every read here is already scoped by, and
// conflating the two is how a channel would be written into the wrong tenant.
const spaceChannels = "core:space:Space"

// Channel lifecycle intent. These say what a channel is FOR, which is a
// different question from whether it is open right now.
const (
	// lifeStanding is a channel meant to outlive any one task — a team's
	// standing channel. It is the default for a channel that says nothing.
	lifeStanding = "standing"
	// lifeBound is a channel opened for one provider of work and expected to be
	// archived when that work lands (#bugfix-1010). Nothing reaps it: the intent
	// is what an archive-on-merge rule would READ, and stating it is not the same
	// as acting on it.
	lifeBound = "bound"
)

// bindMax bounds how many things one channel may be about. It is a sanity bound
// on a write, not a product limit — a channel naming a hundred repositories is a
// mistake being persisted, not a use case.
const bindMax = 32

// channelBridge holds the stores the channel ops read. degraded is the
// fail-closed posture Mount resolved (no HS256 secret); a typed op cannot be
// wrapped by Mount's guard, so it asks for itself — see typed.go.
type channelBridge struct {
	trans    *transServer
	accounts *accountStore
	// ident resolves a verified credential to the team account a WRITE is
	// attributed to. The reads here scope by org alone; a message carries an
	// author, and that is the one thing a caller may not choose. See message.go.
	ident *identity
	// public is the cross-org directory (public.go). A channel is published to it
	// when it is opened public; a private one is never written there at all.
	public   *publicIndex
	degraded bool
}

// register declares the channel surface as TYPED ops, so one registry entry
// yields the route, the OpenAPI operation, the MCP tool, the CLI command and the
// SDK method.
//
// The group is built HERE, from the one prefix constant, because a typed op's
// path is its group's prefix composed with its leaf and cmd/zipdoc resolves that
// prefix from the assignment in the SAME file — a group arriving as a parameter
// is one it cannot see, and it refuses rather than filing the prose under a path
// that does not exist. Same shape bots.go uses.
func (b *channelBridge) register(app cloud.Router) {
	g := app.Group(teamPrefix)
	g.Get("/channels", b.listChannels)
	g.Post("/channels", b.openChannel, zip.WithStatus(http.StatusCreated))
	g.Put("/channels/:id", b.bindChannel)
}

// teamChannels is the org's channels, newest activity irrelevant — the order is
// by name, because a list a person reads is sorted by the thing they read.
type teamChannels struct {
	// Channels is every channel of every space the caller's org owns, each
	// with the work facet it carries.
	Channels []teamChannel `json:"channels"`
}

// teamChannelOne is the answer of every op that answers ONE channel: opening,
// binding, editing and joining it. A list is {"channels": […]} and one is
// {"channel": {…}} (HIP-0523 §2.1), so a caller never tells the two apart by
// sniffing for an id.
type teamChannelOne struct {
	// Channel is the channel as it now stands, in the listing's shape.
	Channel teamChannel `json:"channel"`
}

// teamChannel is one channel, as a space view reads it.
type teamChannel struct {
	// ID is the channel document's own id, and the value the bind op addresses.
	// It is unique within a space, not across the org.
	ID string `json:"id"`
	// Space is the space uuid holding this channel. It is part of the
	// channel's address: two spaces of one org may each hold a channel with
	// the same name, and only the pair identifies one.
	Space string `json:"space"`
	// Name is what a person sees in a sidebar. A direct message carries none, so
	// this is empty for one — the members are its name.
	Name string `json:"name"`
	// Topic is the channel's own one-line subject, as the Team client sets it.
	Topic string `json:"topic,omitempty"`
	// Kind is what sort of channel this is: "public" (open to everyone in the
	// space), "private" (open to its members) or "dm" (a conversation between
	// the people in it). It is derived from the class and the private flag, so it
	// cannot disagree with Direct and Private beside it.
	Kind string `json:"kind"`
	// Direct reports that this is a channel between people rather than a named
	// channel. It is derived from the document's class, so it cannot disagree
	// with what the client will render.
	Direct bool `json:"direct"`
	// Private reports that the channel is restricted to its members.
	Private bool `json:"private"`
	// Archived reports that the channel has been closed. It is the platform's own
	// Space attribute — the same one the Team client writes — and NOT a field of
	// the work facet, so there is exactly one answer to "is this channel open".
	Archived bool `json:"archived"`
	// Members are the account uuids in the channel, agents included: an agent
	// projects as a space member under a uuid derived from its id, so a
	// caller comparing this against GET /v1/bot/members learns which channels an
	// agent is in.
	Members []string `json:"members"`
	// Life is the channel's lifecycle INTENT — "standing" or "bound" (HIP-0523 §2).
	// Absent on the document it reads "standing": a channel nobody classified is one
	// that persists.
	Life string `json:"life"`
	// Bindings are what this channel is ABOUT, each a "<kind>:<ref>" string —
	// "project:acme/web", "repo:hanzoai/cloud", "issue:1010". One list rather
	// than one field per kind, because the next thing a channel can be about should
	// not be a schema change; and a bound value is opaque here on purpose, since
	// the app that owns a project is the app that can resolve one. HIP-0523 §2:
	// a binding is a REFERENCE, never a copy — a channel holding an issue's title or
	// status would be the parallel work-item store HIP-1160 §1 forbids.
	Bindings []string `json:"bindings"`
}

// teamChannelBind states what a channel is for. Every field is optional and an
// absent one is LEFT ALONE, so a caller that knows only the bindings does not
// have to restate the kind to avoid resetting it.
type teamChannelBind struct {
	// ID is the channel to bind, from the path. The URL is the authority; a body
	// carrying another id cannot redirect the write.
	ID string `json:"id"`
	// Space names the space holding the channel. It is required, because
	// a channel id is unique only within one and searching every space for a
	// matching id would make the write's target depend on iteration order.
	Space string `json:"space" url:"-"`
	// Life sets the lifecycle intent: "standing" or "bound". Any other
	// value is refused rather than stored, so a reader never has to interpret a
	// third one. Empty leaves the current intent unchanged.
	Life string `json:"life,omitempty" url:"-"`
	// Bindings REPLACES what the channel is about, wholly. It is a replace and
	// not a merge because a caller that cannot remove a binding would have no way
	// to correct a wrong one, and an empty list sent explicitly is how a channel
	// is unbound. Absent (null) leaves the existing list alone.
	Bindings []string `json:"bindings,omitempty" url:"-"`
}

// ListChannels returns the channels the caller may see, with the kind and work
// facet each carries.
//
// A signed-in TEAM MEMBER reads every channel of every space they are in that is
// open to them: public channels, and the private channels and direct messages
// that name them. An APPLICATION of the org (its own machine credential) reads
// the public channels of the org's spaces and nothing that belongs to
// particular people. Anybody else without a team session is 401.
//
// It reads the SAME Chunter documents the transactor serves, so a channel opened
// in the Team client appears here with no sync, and a facet written here is read
// by anything holding the document.
//
// Example: {"channels": [{"id": "6543", "name": "bugfix-1010", "kind": "public", "life": "bound", "bindings": ["repo:hanzoai/cloud"]}]}
func (b *channelBridge) listChannels(ctx context.Context, _ *cloud.Unit) (*teamChannels, error) {
	if b.degraded {
		return nil, unavailable()
	}
	out, err := b.visibleChannels(ctx)
	if err != nil {
		return nil, err
	}
	// A stable order, because a list that reshuffles between two identical reads
	// is one no client can diff. Space first, then name, then id — the last
	// being what separates two direct messages, which carry no name at all.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Space != out[j].Space {
			return out[i].Space < out[j].Space
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return &teamChannels{Channels: out}, nil
}

// visibleChannels is the channels listChannels answers, lane by lane.
func (b *channelBridge) visibleChannels(ctx context.Context) ([]teamChannel, error) {
	out := []teamChannel{}
	if _, ok := teamCaller(ctx, b.ident); ok {
		seats, err := seatsOf(ctx, b.ident, "")
		if err != nil {
			return nil, err
		}
		for _, s := range seats {
			docs, err := b.trans.store.byClasses(s.org, s.ws, []string{clChannel, clDirectMessage})
			if err != nil {
				// One unreadable space must not blank the whole list, and it must
				// not be silent either: the caller is told which one.
				return nil, zip.Errorf(http.StatusBadGateway, "team: channels of space %s: %v", s.ws, err)
			}
			for _, doc := range docs {
				if s.canSee(doc) {
					out = append(out, channelOf(s.ws, doc))
				}
			}
		}
		return out, nil
	}
	org, err := orgLane(ctx)
	if err != nil {
		return nil, err
	}
	wss, err := b.accounts.SpacesForOrg(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "team: spaces: %v", err)
	}
	for _, ws := range wss {
		docs, err := b.trans.store.byClasses(org, ws.UUID, []string{clChannel})
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "team: channels of space %s: %v", ws.UUID, err)
		}
		for _, doc := range docs {
			if publicChannel(doc) {
				out = append(out, channelOf(ws.UUID, doc))
			}
		}
	}
	return out, nil
}

// BindChannel states what a channel is for: its lifecycle intent, and what it is
// about. It answers the channel as it now stands.
//
// The write is a platform MIXIN on the channel document, applied through the
// SAME applyTx path the Team client's own writes take and broadcast to every
// connected client — so a channel bound here updates live in an open space
// rather than on the next reload. A team member binds a channel they can see; an
// application of the org binds its public channels only; anybody else without
// a team session is 401.
//
// Example: {"space": "0e3c…", "life": "bound", "bindings": ["repo:hanzoai/cloud", "issue:1010"]}
func (b *channelBridge) bindChannel(ctx context.Context, in *teamChannelBind) (*teamChannelOne, error) {
	if b.degraded {
		return nil, unavailable()
	}
	id := strings.TrimSpace(in.ID)
	ws := strings.TrimSpace(in.Space)
	if id == "" || ws == "" {
		return nil, zip.ErrBadRequest("channel id and space are required")
	}
	work, err := workOf(in)
	if err != nil {
		return nil, err
	}
	// Two lanes, each gated BEFORE a store is opened, so a refused pair never
	// mints a per-space database. A team member binds a channel they can see. An
	// application binds its org's PUBLIC channels only: a private channel or a DM
	// belongs to its people, and neither its facet nor its member list is a
	// machine's to write or read back.
	var org string
	var doc map[string]any
	if _, ok := teamCaller(ctx, b.ident); ok {
		s, err := enter(ctx, b.ident, ws)
		if err != nil {
			return nil, zip.ErrNotFound("channel not found")
		}
		if doc, err = b.channel(s, id); err != nil {
			return nil, err
		}
		org = s.org
	} else {
		if org, err = orgLane(ctx); err != nil {
			return nil, err
		}
		// The space must be one the caller's ORG owns. Without this the id and
		// the space both come from the request and the org would bound nothing —
		// a caller could name any space uuid and write into another tenant's
		// documents. SpacesForOrg is the only thing that ties the pair to the
		// validated principal.
		owned, err := b.accounts.SpacesForOrg(ctx, org)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "team: spaces: %v", err)
		}
		if !ownsSpace(owned, ws) {
			// 404, not 403: a caller who may not touch this space must not learn
			// whether it exists.
			return nil, zip.ErrNotFound("channel not found")
		}
		if doc, err = b.trans.store.get(org, ws, id); err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "team: channel: %v", err)
		}
		if doc == nil || !publicChannel(doc) {
			return nil, zip.ErrNotFound("channel not found")
		}
	}
	// Written as a mixin tx rather than by mutating the document here, so the one
	// write path stays the one write path: applyTx persists it, the triggers and
	// notification projections run, and the applied tx is broadcast to every live
	// client of the space.
	sess := &session{server: b.trans, store: b.trans.store, hier: b.trans.hier, org: org, space: ws, account: acctSystem}
	if _, err := apply(sess, b.trans, ws, mixinTx(id, str(doc["_class"]), str(doc["space"]), mixinChannel, acctSystem, work)); err != nil {
		return nil, err
	}
	// Re-read rather than projecting what we just sent: the answer is what the
	// store now HOLDS, which is the only thing a caller can rely on, and it is
	// how a partially-applied write shows up as one.
	after, err := b.trans.store.get(org, ws, id)
	if err != nil || after == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: channel after bind: %v", err)
	}
	v := channelOf(ws, after)
	return &teamChannelOne{Channel: v}, nil
}

// workOf builds the mixin attributes one bind states, refusing anything a reader
// would then have to interpret. A nil map means the request stated nothing,
// which is a no-op write rather than an error — re-binding a channel to what it
// already is should not fail.
func workOf(in *teamChannelBind) (map[string]any, error) {
	work := map[string]any{}
	switch kind := strings.TrimSpace(in.Life); kind {
	case "":
		// unstated — leave whatever the channel carries
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

// channelOf projects one stored channel document to the published shape. It
// reads the work facet where the document carries one and defaults where it does
// not, so a channel that predates the facet reads as an ordinary persistent
// channel rather than as a hole.
func channelOf(space string, doc map[string]any) teamChannel {
	out := teamChannel{
		ID:       str(doc["_id"]),
		Space:    space,
		Name:     str(doc["name"]),
		Topic:    str(doc["topic"]),
		Kind:     channelKind(doc),
		Direct:   str(doc["_class"]) == clDirectMessage,
		Private:  truthy(doc["private"]),
		Archived: truthy(doc["archived"]),
		Members:  toStringSlice(doc["members"]),
		Life:     lifeStanding,
		Bindings: []string{},
	}
	if out.Members == nil {
		out.Members = []string{}
	}
	work, _ := doc[mixinChannel].(map[string]any)
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

// Channel kinds, as teamChannel.Kind publishes them.
const (
	kindPublic  = "public"
	kindPrivate = "private"
	kindDM      = "dm"
)

// channelKind names what sort of channel a document is.
func channelKind(doc map[string]any) string {
	switch {
	case str(doc["_class"]) == clDirectMessage:
		return kindDM
	case truthy(doc["private"]):
		return kindPrivate
	default:
		return kindPublic
	}
}

// publicChannel reports whether a channel is a channel open to the whole space — the
// only kind the org lane, which names no person, may read.
func publicChannel(doc map[string]any) bool {
	return str(doc["_class"]) == clChannel && !truthy(doc["private"])
}

// isChannel reports whether a document class is a channel — a named channel or a
// direct message. It is the ONE place the two classes are treated as one noun,
// which is what keeps "everything is a channel" true of this surface.
func isChannel(class string) bool { return class == clChannel || class == clDirectMessage }

// ownsSpace reports whether uuid is one of the org's own spaces.
func ownsSpace(owned []space, uuid string) bool {
	for _, ws := range owned {
		if ws.UUID == uuid {
			return true
		}
	}
	return false
}

// truthy coerces a stored JSON boolean. A missing or non-boolean value is false,
// which is the safe reading for both fields that use it: an unclassified channel
// is neither private nor archived.
func truthy(v any) bool {
	b, _ := v.(bool)
	return b
}

// teamChannelNew opens a channel. A name and nothing else is a complete request:
// everything here except the name has a defensible default, and a caller that
// states only what it cares about gets the rest.
type teamChannelNew struct {
	// Name is what a person sees in a sidebar — "bugfix-1010", not "#bugfix-1010".
	// The sigil is how a client DRAWS a channel, and storing it would put it in the
	// name twice the first time a client added its own.
	Name string `json:"name"`
	// Space is where the channel is opened. Optional: an org with one space has no
	// choice to make, so it does not have to state one. An org with several must,
	// because picking for it would make the channel's home depend on iteration order.
	Space string `json:"space,omitempty"`
	// Topic is the channel's one-line subject.
	Topic string `json:"topic,omitempty"`
	// Private restricts the channel to its members. Public is the default because a
	// channel nobody can find is the more surprising of the two.
	Private bool `json:"private,omitempty"`
	// Members are the account uuids in the channel. A public channel may open empty —
	// anyone in the org can find it — and a private one that names nobody is
	// refused rather than created unreachable.
	Members []string `json:"members,omitempty"`
	// Life is the lifecycle intent, "standing" or "bound"; empty reads standing.
	Life string `json:"life,omitempty"`
	// Bindings are what the channel is about, each "<kind>:<ref>".
	Bindings []string `json:"bindings,omitempty"`
}

// openChannel opens a named channel and answers it as the store now holds it.
//
// It writes through the SAME applyTx path the Team client uses, so a channel opened
// here is broadcast to every live client of the space and appears in an open
// sidebar without a reload — the same property listChannels rests on, read from the
// write side.
//
// A SIGNED-IN TEAM MEMBER opens it as themselves: they are its first member and
// its owner, every other member named must be somebody the space knows (a person
// in it, or one of the org's agents), and a guest may not open channels at all. An
// application of the org opens it as the org, with exactly the members it names.
// Anybody else without a team session is 401.
//
// TWO TRANSACTIONS, NOT ONE, when the request states a facet. The document and
// its mixin are separate writes in this model (bindChannel writes only the second),
// and composing them here rather than inventing a combined tx keeps one write
// path for each. A create that lands and a facet that does not is visible as a
// channel with default intent, which is the honest partial state.
//
// Example: {"name": "bugfix-1010", "life": "bound", "bindings": ["issue:1010"]}
func (b *channelBridge) openChannel(ctx context.Context, in *teamChannelNew) (*teamChannelOne, error) {
	if b.degraded {
		return nil, unavailable()
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("channel name is required")
	}
	members := dedupe(in.Members)
	author, owners := acctSystem, []string{}
	var org, ws string
	if _, ok := teamCaller(ctx, b.ident); ok {
		s, err := enter(ctx, b.ident, in.Space)
		if err != nil {
			return nil, err
		}
		if s.guest() {
			return nil, zip.ErrForbidden("team: a guest cannot open channels")
		}
		people, _, err := b.people(ctx, s)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if _, known := people[m]; !known {
				return nil, zip.ErrBadRequest("team: " + m + " is not a member of this space")
			}
		}
		org, ws, author, owners = s.org, s.ws, s.social(), []string{s.account}
		members = dedupe(append([]string{s.account}, members...))
	} else {
		acting, err := orgLane(ctx)
		if err != nil {
			return nil, err
		}
		owned, err := b.accounts.SpacesForOrg(ctx, acting)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "team: spaces: %v", err)
		}
		if ws, err = spaceFor(owned, strings.TrimSpace(in.Space)); err != nil {
			return nil, err
		}
		org = acting
	}
	// A private channel nobody is in cannot be entered by anyone, including the
	// caller: it would be a document only an admin could reach. Refused at the
	// way in rather than created and then explained.
	if in.Private && len(members) == 0 {
		return nil, zip.ErrBadRequest("a private channel needs at least one member")
	}
	// A second #general in one space is a mistake being persisted: two channels that
	// read identically in a sidebar, and no way to tell which one a message landed
	// in. Direct messages carry no name and are not compared.
	channels, err := b.trans.store.byClasses(org, ws, []string{clChannel})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: channels of space %s: %v", ws, err)
	}
	for _, doc := range channels {
		if strings.EqualFold(strings.TrimSpace(str(doc["name"])), name) {
			return nil, zip.Errorf(http.StatusConflict, "team: a channel named %q is already open here", name)
		}
	}
	id := newMsgID()
	topic := strings.TrimSpace(in.Topic)
	create := createTx(id, clChannel, spaceChannels, author, map[string]any{
		"name":        name,
		"description": topic,
		"topic":       topic,
		"private":     in.Private,
		"archived":    false,
		"members":     members,
		"owners":      owners,
	})
	sess := &session{server: b.trans, store: b.trans.store, hier: b.trans.hier, org: org, space: ws, account: stripHanzo(author)}
	if _, err := apply(sess, b.trans, ws, create); err != nil {
		return nil, err
	}
	// The facet, when the request states one. workOf is the same reader bindChannel
	// uses, so "life" and "bindings" mean here exactly what they mean there.
	work, err := workOf(&teamChannelBind{Life: in.Life, Bindings: in.Bindings})
	if err != nil {
		return nil, err
	}
	if len(work) > 0 {
		if _, err := apply(sess, b.trans, ws, mixinTx(id, clChannel, spaceChannels, mixinChannel, author, work)); err != nil {
			return nil, err
		}
	}
	after, err := b.trans.store.get(org, ws, id)
	if err != nil || after == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: channel after open: %v", err)
	}
	v := channelOf(ws, after)
	// A public channel becomes findable from outside this org (HIP-1327). A private
	// one is never written to the directory at all. Best-effort: the channel IS
	// open, and failing the create because a projection row did not land would
	// lose the thing that matters in order to keep the index tidy.
	if !in.Private {
		b.publishChannel(org, ws, id, name, topic, len(members))
	}
	return &teamChannelOne{Channel: v}, nil
}

// dedupe drops blanks and repeats from a list of ids, keeping first-seen order,
// and never answers nil — a members list is written as [] rather than null.
func dedupe(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// spaceFor resolves which space a channel is opened in. Naming one is only required
// where there is a choice — the answer is otherwise the org's single space, and
// asking a caller to state the only possible value is asking it to know a uuid
// for nothing.
func spaceFor(owned []space, named string) (string, error) {
	if named != "" {
		if !ownsSpace(owned, named) {
			// 404, not 403: a caller who may not write here must not learn whether
			// the space exists.
			return "", zip.ErrNotFound("space not found")
		}
		return named, nil
	}
	switch len(owned) {
	case 0:
		return "", zip.ErrNotFound("this org has no space to open a channel in")
	case 1:
		return owned[0].UUID, nil
	default:
		return "", zip.ErrBadRequest("this org has more than one space: name the one to open the channel in")
	}
}

// apply marshals txes, writes each through the session and broadcasts what
// landed — the SAME sequence the socket's tx RPC runs (session.tx), so a write
// made here and one made in the Team client are one write to every observer.
//
// The DERIVED txes go out with the applied ones. The triggers the write path
// runs (notify contexts, inbox rows, collection counters) record what they
// minted on the session, and a live client or the event stream that saw the
// message but not the inbox row it produced would be showing half a write. Every
// writer here would otherwise repeat the marshal, the apply, the drain and the
// broadcast, and the one that forgot a step is a channel that exists but does not
// appear until a reload.
//
// It answers what landed, and REFUSES (409) when a tx did not land — the
// store failed, or the document an update, mixin or remove names is gone — so
// a caller is never told a write succeeded that the store never recorded. What
// did land before the refusal is still broadcast: it is in the store, and a
// live client that did not hear about it would be showing a stale space.
func apply(sess *session, srv *transServer, ws string, txes ...map[string]any) ([]json.RawMessage, error) {
	var applied []json.RawMessage
	var refused error
	for _, tx := range txes {
		raw, err := json.Marshal(tx)
		if err != nil {
			refused = zip.Errorf(http.StatusInternalServerError, "team: tx: %v", err)
			break
		}
		_, a := sess.applyTx(raw)
		if len(a) == 0 {
			refused = zip.Errorf(http.StatusConflict, "team: the write did not land: %s changed or is gone", str(tx["objectId"]))
			break
		}
		applied = append(applied, a...)
	}
	if len(sess.derived) > 0 {
		applied = append(applied, sess.derived...)
		sess.derived = nil
	}
	if len(applied) > 0 {
		srv.hub.broadcast(ws, applied)
	}
	return applied, refused
}
