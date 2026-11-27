package team

// members.go is the space ROSTER: who a client can draw, mention and message.
//
// A person's membership and name are IAM's; an agent's are the agents
// registry's; how a person LOOKS (an uploaded avatar) is the Person document the
// Team client edits. This op reads each from its owner and answers one list, so
// a client never has to show an 8-character uuid for somebody the space knows.

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
)

// Presence values, as teamMember.Presence publishes them.
const (
	presenceOnline  = "online"
	presenceOffline = "offline"
)

// teamMembersRead names the space whose roster to read.
type teamMembersRead struct {
	// Space is the space uuid. Optional for a caller in exactly one space.
	Space string `json:"space,omitempty"`
}

// teamMembers is one space's roster.
type teamMembers struct {
	// Members are the space's people and the org's agents, people first, each
	// group by name.
	Members []teamMember `json:"members"`
	// Degraded names a roster source that did not answer — "agents" — so an
	// empty agent list reads as an outage rather than as an org with no agents.
	// Absent when every source answered.
	Degraded []string `json:"degraded,omitempty"`
}

// teamMember is one person or agent a client can show, mention and message.
type teamMember struct {
	// ID is the account uuid — the value every message's author, every channel's
	// members list and every `<@id>` mention token names.
	ID string `json:"id"`
	// Name is what to call them: the name IAM holds for a person, the agent's
	// own name for an agent. Falls back to the id when neither has one.
	Name string `json:"name"`
	// Avatar is the blob id of an uploaded picture, readable at
	// GET /v1/team/files/{space}/avatar?file={avatar}. Empty when the person has
	// none and the client draws initials.
	Avatar string `json:"avatar,omitempty"`
	// Bot reports that this member is one of the org's agents rather than a
	// person. An agent answers when it is @-mentioned or messaged directly.
	Bot bool `json:"bot"`
	// Role is the member's role in the space — owner, admin, member or guest.
	// Agents read "member".
	Role string `json:"role"`
	// Active is false for an agent the org has retired: it stays in the roster so
	// its past messages keep an author, and it no longer answers.
	Active bool `json:"active"`
	// Presence is "online" while the member holds a live connection to this
	// space (a Team client or an event stream), "offline" otherwise.
	Presence string `json:"presence"`
	// PersonRef is the member's Person document id, the reference a mention
	// stores.
	PersonRef string `json:"personRef"`
}

// registerMembers declares the roster read. The group is built here so
// cmd/zipdoc can resolve the prefix from this file.
func (b *channelBridge) registerMembers(app cloud.Router) {
	g := app.Group(teamPrefix)
	g.Get("/members", b.listMembers)
}

// ListMembers returns the people and agents in a space, with the name, avatar,
// role and presence a client draws them with.
//
// People come from IAM, the membership authority; agents from the org's agent
// registry; avatars from the Person documents the Team client edits. An IAM that
// does not answer is a 502 rather than an empty roster, and an agent registry
// that does not answer is named in `degraded` beside the people who did load.
// The caller must be a member of the space.
//
// Example: {"members": [{"id": "9f2…", "name": "Ada Lovelace", "bot": false, "role": "owner", "active": true, "presence": "online", "personRef": "person-9f2…"}]}
func (b *channelBridge) listMembers(ctx context.Context, in *teamMembersRead) (*teamMembers, error) {
	if b.degraded {
		return nil, unavailable()
	}
	s, err := enter(ctx, b.ident, in.Space)
	if err != nil {
		return nil, err
	}
	people, degraded, err := b.people(ctx, s)
	if err != nil {
		return nil, err
	}
	out := make([]teamMember, 0, len(people))
	for _, m := range people {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bot != out[j].Bot {
			return !out[i].Bot
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return &teamMembers{Members: out, Degraded: degraded}, nil
}

// people is everyone a space can name, keyed by account uuid: its IAM members
// and the org's agents, each with avatar and presence filled in.
//
// It is ONE roster for every use — the list a client draws, the names a mention
// is labelled with, the set a channel member must come from — so a person who can
// be drawn is exactly a person who can be mentioned and added.
func (b *channelBridge) people(ctx context.Context, s seat) (map[string]teamMember, []string, error) {
	rows, err := b.accounts.rosterOf(ctx, s.org, s.ws)
	if err != nil {
		return nil, nil, zip.Errorf(http.StatusBadGateway, "team: roster unavailable: %v", err)
	}
	here := b.trans.hub.present(ctx, s.ws)
	avatars := b.avatars(s)
	presence := func(id string) string {
		if here[id] {
			return presenceOnline
		}
		return presenceOffline
	}
	out := make(map[string]teamMember, len(rows))
	for _, m := range rows {
		if m.User == "" {
			continue
		}
		out[m.User] = teamMember{
			ID: m.User, Name: pick(m.Name, m.User), Avatar: avatars[m.User],
			Role: pick(m.Role, roleMember), Active: true,
			Presence: presence(m.User), PersonRef: PersonRef(m.User),
		}
	}
	var degraded []string
	if b.trans.bots != nil {
		bots, err := b.trans.bots(ctx, s.org)
		switch {
		case errors.Is(err, cloud.ErrNoPeer):
			// A deployment that runs no agents subsystem has no agents.
		case err != nil:
			degraded = append(degraded, "agents")
		default:
			for _, bt := range bots {
				uid := botUserID(bt.ID)
				if uid == "" {
					continue
				}
				out[uid] = teamMember{
					ID: uid, Name: pick(bt.Name, bt.ID), Avatar: avatars[uid], Bot: true,
					Role: roleMember, Active: bt.Active,
					Presence: presence(uid), PersonRef: PersonRef(uid),
				}
			}
		}
	}
	return out, degraded, nil
}

// avatars maps an account to the blob id of the picture its Person document
// carries, for the people who uploaded one.
func (b *channelBridge) avatars(s seat) map[string]string {
	out := map[string]string{}
	docs, err := b.trans.store.byClasses(s.org, s.ws, []string{clPerson})
	if err != nil {
		return out
	}
	for _, doc := range docs {
		uid, blob := str(doc["personUuid"]), str(doc["avatar"])
		if uid != "" && blob != "" && str(doc["avatarType"]) == "image" {
			out[uid] = blob
		}
	}
	return out
}

// names is the roster reduced to what a mention needs: account → name.
func names(people map[string]teamMember) map[string]string {
	out := make(map[string]string, len(people))
	for id, m := range people {
		out[id] = m.Name
	}
	return out
}
