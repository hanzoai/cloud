package team

// channel_members.go is who is IN a channel and what state the channel is in: direct
// messages opened by their member set, joining and leaving, adding and removing
// people, and renaming or archiving.
//
// Membership is the channel document's own `members` list — the attribute the Team
// client writes when somebody joins — so every change here is an ordinary update
// through the client's write path. The notify-context trigger reconciles the
// Inbox to the new list, and every open client sees the change live.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
)

// dmMax bounds a direct message's size. A conversation between more people than
// this is a private channel, and should be named as one.
const dmMax = 16

// teamDirectOpen names the people a direct message is between.
type teamDirectOpen struct {
	// Space is the space uuid. Optional for a caller in exactly one space.
	Space string `json:"space,omitempty"`
	// Members are the account uuids to talk to — people or agents of the space.
	// The caller is always in the conversation and need not name themselves; an
	// empty list is the caller's own notes-to-self conversation.
	Members []string `json:"members"`
}

// teamDirect is a direct message, and whether this call opened it.
type teamDirect struct {
	// Channel is the direct message, as the channel listing answers it.
	Channel teamChannel `json:"channel"`
	// Created is true when this call opened the conversation (201) and false
	// when it already existed (200).
	Created bool `json:"created"`
}

// StatusCode tells zip which of the two declared statuses this answer is.
func (d *teamDirect) StatusCode() int {
	if d.Created {
		return http.StatusCreated
	}
	return http.StatusOK
}

// teamChannelMembers is who is in one channel.
type teamChannelMembers struct {
	// Members are the channel's people and agents, as the roster describes them.
	// An account the space no longer knows is listed by id, with no role.
	Members []teamMember `json:"members"`
}

// teamChannelJoin adds people to a channel.
type teamChannelJoin struct {
	// ID is the channel, from the path.
	ID string `json:"id"`
	// Space is the space uuid holding it. Body-only.
	Space string `json:"space" url:"-"`
	// Members are the account uuids to add — people or agents of the space.
	// Naming only yourself joins a public channel.
	Members []string `json:"members" url:"-"`
}

// teamChannelLeave removes one person from a channel.
type teamChannelLeave struct {
	// ID is the channel, from the path.
	ID string `json:"id"`
	// Account is the account uuid to remove, from the path. Your own is leaving.
	Account string `json:"account"`
	// Space is the space uuid holding the channel.
	Space string `json:"space"`
}

// teamChannelEdit renames, re-topics, archives or reopens a channel. Every field is
// optional and an absent one is left alone.
type teamChannelEdit struct {
	// ID is the channel, from the path.
	ID string `json:"id"`
	// Space is the space uuid holding it. Body-only.
	Space string `json:"space" url:"-"`
	// Name renames a channel. A direct message has no name.
	Name *string `json:"name,omitempty" url:"-"`
	// Topic sets the channel's one-line subject; "" clears it.
	Topic *string `json:"topic,omitempty" url:"-"`
	// Archived closes the channel (true) or reopens it (false). An archived channel
	// keeps its history and takes no new messages.
	Archived *bool `json:"archived,omitempty" url:"-"`
}

// registerChannelMembers declares the membership and lifecycle ops. The group is
// built here so cmd/zipdoc can resolve the prefix from this file.
func (b *channelBridge) registerChannelMembers(app cloud.Router) {
	g := app.Group(teamPrefix)
	g.Post("/dms", b.openDirect, zip.WithStatus(http.StatusCreated, http.StatusOK))
	g.Patch("/channels/:id", b.editChannel)
	g.Get("/channels/:id/members", b.listChannelMembers)
	g.Post("/channels/:id/members", b.joinChannel)
	g.Delete("/channels/:id/members/:account", b.leaveChannel, zip.WithStatus(http.StatusNoContent))
}

// OpenDirect opens the direct message between the caller and the people named,
// or answers the one that already exists — there is one conversation per set of
// people, however many times it is asked for.
//
// Every person named must be a member of the space or one of the org's agents;
// a direct message with an agent is a conversation it answers every message in,
// and a guest may not open one.
// Answers 201 when this call opened it and 200 when it was already there.
//
// Example: {"members": ["9f2c…"]}
func (b *channelBridge) openDirect(ctx context.Context, in *teamDirectOpen) (*teamDirect, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	set := dedupe(append([]string{s.account}, in.Members...))
	if len(set) > dmMax {
		return nil, zip.ErrBadRequest("team: a direct message is between at most 16 people; open a private channel instead")
	}
	people, _, err := b.people(ctx, s)
	if err != nil {
		return nil, err
	}
	for _, m := range set {
		p, known := people[m]
		if !known {
			return nil, zip.ErrBadRequest("team: " + m + " is not a member of this space")
		}
		// A DM with an agent is a conversation it answers every line of, on the
		// org's balance; a guest's reach is what names them, not the org's wallet.
		if p.Bot && s.guest() {
			return nil, zip.ErrForbidden("team: a guest cannot open a direct message with an agent")
		}
	}
	sort.Strings(set)
	// The Team client opens DMs under ids of its own, so the set is found by
	// what the conversation IS — its members — before one is opened.
	dms, err := b.trans.store.byClasses(s.org, s.ws, []string{clDirectMessage})
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: direct messages: %v", err)
	}
	for _, dm := range dms {
		have := dedupe(toStringSlice(dm["members"]))
		sort.Strings(have)
		if slices.Equal(have, set) {
			return &teamDirect{Channel: channelOf(s.ws, dm)}, nil
		}
	}
	// Opened under an id derived from the set, so two first asks at once write
	// one conversation rather than two.
	sum := sha256.Sum256([]byte(strings.Join(set, ",")))
	id := "dm-" + hex.EncodeToString(sum[:16])
	create := createTx(id, clDirectMessage, spaceChannels, s.social(), map[string]any{
		"name": "", "description": "", "private": true, "archived": false,
		"members": set, "owners": []string{},
	})
	if _, err := apply(b.session(s), b.trans, s.ws, create); err != nil {
		return nil, err
	}
	after, err := b.trans.store.get(s.org, s.ws, id)
	if err != nil || after == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: direct message after open: %v", err)
	}
	return &teamDirect{Channel: channelOf(s.ws, after), Created: true}, nil
}

// ListChannelMembers returns the people and agents in one channel, as the roster
// describes them. The caller must be able to see the channel.
//
// Example: {"members": [{"id": "9f2…", "name": "Ada Lovelace", "bot": false, "role": "owner", "active": true, "presence": "online", "personRef": "person-9f2…"}]}
func (b *channelBridge) listChannelMembers(ctx context.Context, in *teamMessageRead) (*teamChannelMembers, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	channel, err := b.channel(s, in.ID)
	if err != nil {
		return nil, err
	}
	people, _, err := b.people(ctx, s)
	if err != nil {
		return nil, err
	}
	out := []teamMember{}
	for _, id := range dedupe(toStringSlice(channel["members"])) {
		m, known := people[id]
		if !known {
			m = teamMember{ID: id, Name: id, PersonRef: PersonRef(id), Presence: presenceOffline}
		}
		out = append(out, m)
	}
	return &teamChannelMembers{Members: out}, nil
}

// JoinChannel adds people to a channel and answers the channel as it now stands.
//
// Anyone in the space except a guest may join a public channel by naming
// themselves. Adding somebody else takes being in the channel already (or
// administering the space, for a channel they can see). A direct message's people
// are what it is, so none can be added — open another one. Everyone added must
// be a member of the space or one of the org's agents; an agent added to a channel
// answers when it is @-mentioned there.
//
// Example: {"space": "0e3c…", "members": ["9f2c…"]}
func (b *channelBridge) joinChannel(ctx context.Context, in *teamChannelJoin) (*teamChannelOne, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	channel, err := b.channel(s, in.ID)
	if err != nil {
		return nil, err
	}
	if str(channel["_class"]) == clDirectMessage {
		return nil, zip.Errorf(http.StatusConflict, "team: a direct message's people are fixed; open another one")
	}
	if truthy(channel["archived"]) {
		return nil, zip.Errorf(http.StatusConflict, "team: the channel is archived")
	}
	add := dedupe(in.Members)
	if len(add) == 0 {
		return nil, zip.ErrBadRequest("team: name who joins")
	}
	onlySelf := len(add) == 1 && add[0] == s.account
	if !onlySelf && !s.inChannel(channel) && !s.admin() {
		return nil, zip.ErrForbidden("team: only somebody in the channel can add people to it")
	}
	if s.guest() && !s.inChannel(channel) {
		return nil, zip.ErrNotFound("channel not found")
	}
	people, _, err := b.people(ctx, s)
	if err != nil {
		return nil, err
	}
	members := dedupe(toStringSlice(channel["members"]))
	for _, m := range add {
		if _, known := people[m]; !known {
			return nil, zip.ErrBadRequest("team: " + m + " is not a member of this space")
		}
		if !slices.Contains(members, m) {
			members = append(members, m)
		}
	}
	v, err := b.setMembers(s, channel, members)
	if err != nil {
		return nil, err
	}
	return &teamChannelOne{Channel: *v}, nil
}

// LeaveChannel takes one person out of a channel — the caller leaving, when the
// account is their own. Removing somebody else takes owning the channel or
// administering the space. Leaving a channel you are not in, or removing somebody
// who is not there, changes nothing. A direct message cannot be left: its people
// are what it is. Answers 204.
//
// Example: {"space": "0e3c…"}
func (b *channelBridge) leaveChannel(ctx context.Context, in *teamChannelLeave) (*cloud.Unit, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	channel, err := b.channel(s, in.ID)
	if err != nil {
		return nil, err
	}
	if str(channel["_class"]) == clDirectMessage {
		return nil, zip.Errorf(http.StatusConflict, "team: a direct message cannot be left")
	}
	who := strings.TrimSpace(in.Account)
	if who != s.account && !s.ownsChannel(channel) && !s.admin() {
		return nil, zip.ErrForbidden("team: only a channel owner or a space admin can remove somebody else")
	}
	members := dedupe(toStringSlice(channel["members"]))
	if !slices.Contains(members, who) {
		return nil, nil
	}
	members = slices.DeleteFunc(members, func(m string) bool { return m == who })
	if truthy(channel["private"]) && len(members) == 0 {
		return nil, zip.Errorf(http.StatusConflict, "team: the last member cannot leave a private channel; archive it instead")
	}
	if _, err := b.setMembers(s, channel, members); err != nil {
		return nil, err
	}
	return nil, nil
}

// EditChannel renames a channel, sets its topic, or archives or reopens it, and
// answers the channel as it now stands.
//
// Its owners and the space's admins may edit a channel; a channel nobody owns — one
// opened by an integration — may be edited by anyone in it, and a direct
// message by either person in it. Archiving withdraws a public channel from the
// cross-org directory in the same write; reopening lists it again.
//
// Example: {"space": "0e3c…", "archived": true}
func (b *channelBridge) editChannel(ctx context.Context, in *teamChannelEdit) (*teamChannelOne, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	channel, err := b.channel(s, in.ID)
	if err != nil {
		return nil, err
	}
	owners := toStringSlice(channel["owners"])
	if !s.ownsChannel(channel) && !s.admin() && (len(owners) > 0 || !s.inChannel(channel) || s.guest()) {
		return nil, zip.ErrForbidden("team: only a channel owner or a space admin can edit a channel")
	}
	direct := str(channel["_class"]) == clDirectMessage
	ops := map[string]any{}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if direct || name == "" {
			return nil, zip.ErrBadRequest("team: a channel's name is required, and a direct message has none")
		}
		channels, err := b.trans.store.byClasses(s.org, s.ws, []string{clChannel})
		if err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "team: channels: %v", err)
		}
		for _, other := range channels {
			if str(other["_id"]) != in.ID && strings.EqualFold(strings.TrimSpace(str(other["name"])), name) {
				return nil, zip.Errorf(http.StatusConflict, "team: a channel named %q is already open here", name)
			}
		}
		ops["name"] = name
	}
	if in.Topic != nil {
		if direct {
			return nil, zip.ErrBadRequest("team: a direct message has no topic")
		}
		ops["topic"] = strings.TrimSpace(*in.Topic)
		ops["description"] = strings.TrimSpace(*in.Topic)
	}
	if in.Archived != nil {
		ops["archived"] = *in.Archived
	}
	if len(ops) > 0 {
		if _, err := apply(b.session(s), b.trans, s.ws,
			updateTx(in.ID, str(channel["_class"]), str(channel["space"]), s.social(), ops)); err != nil {
			return nil, err
		}
	}
	after, err := b.trans.store.get(s.org, s.ws, in.ID)
	if err != nil || after == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: channel after edit: %v", err)
	}
	b.relist(s, after)
	v := channelOf(s.ws, after)
	return &teamChannelOne{Channel: v}, nil
}

// setMembers writes a channel's members list and answers the channel as it now
// stands, keeping the cross-org directory's member count honest.
func (b *channelBridge) setMembers(s seat, channel map[string]any, members []string) (*teamChannel, error) {
	if members == nil {
		members = []string{}
	}
	id := str(channel["_id"])
	if _, err := apply(b.session(s), b.trans, s.ws,
		updateTx(id, str(channel["_class"]), str(channel["space"]), s.social(), map[string]any{"members": members})); err != nil {
		return nil, err
	}
	after, err := b.trans.store.get(s.org, s.ws, id)
	if err != nil || after == nil {
		return nil, zip.Errorf(http.StatusBadGateway, "team: channel after membership: %v", err)
	}
	b.relist(s, after)
	v := channelOf(s.ws, after)
	return &v, nil
}

// relist keeps the cross-org directory in step with a channel that just changed:
// an open public channel is listed as it now reads, anything else is withdrawn.
func (b *channelBridge) relist(s seat, channel map[string]any) {
	id := str(channel["_id"])
	if publicChannel(channel) && !truthy(channel["archived"]) {
		b.publishChannel(s.org, s.ws, id, str(channel["name"]), str(channel["topic"]), len(toStringSlice(channel["members"])))
		return
	}
	if err := b.public.withdraw(s.org, s.ws, id); err != nil && b.trans.log != nil {
		b.trans.log.Warn("withdraw public channel", "org", s.org, "channel", id, "err", err)
	}
}

// ownsChannel reports whether the caller is one of a channel's owners.
func (s seat) ownsChannel(channel map[string]any) bool {
	return slices.Contains(toStringSlice(channel["owners"]), s.account)
}
