package channels

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// agent.go — WHICH agent answers a channel, and it is the org's to say.
//
// A turn used to run one agent for every org on a transport, named by a
// deployment variable. That made "several agents in Slack" a deployment change
// rather than a setting, and put the same agent in every workspace on the
// fleet. The binding is a row now, keyed the way the rooms are: (org, channel,
// room). The empty room is the transport's default for the org; a room row wins
// over it; and with neither the org's built-in `hanzo` answers, as it always has.
//
// The room id is the platform's own — a Slack channel id, a Discord channel id,
// a Teams conversation id — the same value the envelope carries and the inbox
// stores, so a binding names exactly what a message arrives in.

// defaultAgent answers when the org has bound nothing.
const defaultAgent = "hanzo"

// agentFor resolves the agent a message runs: the room's binding, else the
// transport's default for the org, else defaultAgent. A store error falls to the
// default and is logged by the caller's run, never a refused turn — a missing
// binding is not a reason to go quiet.
func agentFor(ctx context.Context, st *store, org string, m Message) string {
	if st != nil {
		for _, room := range []string{m.Room.ID, ""} {
			var ref string
			err := st.db.QueryRowContext(ctx,
				`SELECT agent FROM channel_agent WHERE org = ? AND channel = ? AND room_id = ?`,
				org, m.Channel, room).Scan(&ref)
			if err == nil && ref != "" {
				return ref
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				break
			}
		}
	}
	return defaultAgent
}

// setAgent binds a room (or, with room "", the transport default) to an agent.
// Naming the built-in, or nothing, removes the row: a binding to `hanzo` and no
// binding answer the same, so one of them is not stored.
func setAgent(ctx context.Context, st *store, org, channel, room, agent string, now int64) error {
	if agent == "" || agent == defaultAgent {
		_, err := st.db.ExecContext(ctx, `DELETE FROM channel_agent WHERE org = ? AND channel = ? AND room_id = ?`, org, channel, room)
		return err
	}
	_, err := st.db.ExecContext(ctx, `INSERT INTO channel_agent (org, channel, room_id, agent, updated_at)
  VALUES (?,?,?,?,?)
  ON CONFLICT (org, channel, room_id) DO UPDATE SET agent = excluded.agent, updated_at = excluded.updated_at`,
		org, channel, room, agent, now)
	return err
}

// agentsFor reads every binding the org holds on a channel.
func agentsFor(ctx context.Context, st *store, org, channel string) (channelAgents, error) {
	v := channelAgents{Channel: channel, Default: defaultAgent, Rooms: map[string]string{}}
	rows, err := st.db.QueryContext(ctx, `SELECT room_id, agent FROM channel_agent WHERE org = ? AND channel = ? ORDER BY room_id`, org, channel)
	if err != nil {
		return v, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var room, agent string
		if err := rows.Scan(&room, &agent); err != nil {
			return v, err
		}
		if room == "" {
			v.Default = agent
		} else {
			v.Rooms[room] = agent
		}
	}
	return v, rows.Err()
}

// channelAgentRef names the transport to read.
type channelAgentRef struct {
	// Channel is the transport: discord, github, linear, slack, teams, telegram or whatsapp.
	// Required; an unknown value is a 404.
	Channel string `json:"channel"`
}

// channelAgents is which agent answers on a channel, as GET returns it and PUT echoes it.
type channelAgents struct {
	// Channel is the transport these bindings are for.
	Channel string `json:"channel"`
	// Default is the agent that answers any room without a binding of its own;
	// "hanzo" when the org has never set one.
	Default string `json:"default"`
	// Rooms maps a platform room id to the agent that answers there.
	Rooms map[string]string `json:"rooms"`
}

// channelAgentsPut is a PARTIAL edit: a field that is absent leaves what it names
// alone. Naming the built-in agent (`hanzo`) is how a binding is put back to the
// default, and Unbind is how a room's own binding is removed.
type channelAgentsPut struct {
	// Channel is the transport to edit. Required; an unknown value is a 404.
	Channel string `json:"channel" url:"-"`
	// Default sets the agent for rooms with no binding of their own; "hanzo"
	// restores the built-in. Empty or absent leaves it unchanged.
	Default string `json:"default" url:"-"`
	// Rooms binds platform room ids to agents; rooms not named are left alone.
	Rooms map[string]string `json:"rooms" url:"-"`
	// Unbind removes the bindings of these rooms, so they fall back to Default.
	Unbind []string `json:"unbind" url:"-"`
}

// agentGet returns which agent answers the caller org's channel: the default and
// every room bound to another agent.
//
// Response: {"channel":"slack","default":"eng","rooms":{"C024BE91L":"des"}}
func (o ops) agentGet(ctx context.Context, in *channelAgentRef) (*channelAgents, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	channel := strings.TrimSpace(in.Channel)
	if channel == "" {
		return nil, zip.ErrBadRequest("channel query parameter is required")
	}
	if _, ok := transportFor(channel); !ok {
		return nil, zip.ErrNotFound("unknown channel")
	}
	v, err := agentsFor(ctx, o.s.State.store, org, channel)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "agent: %v", err)
	}
	return &v, nil
}

// agentPut binds agents to the caller org's channel and answers the bindings as
// GET would. It requires ORG ADMIN. The agent is named by its ref — the name an
// org gave it at POST /v1/agent, or a built-in such as dev, des or vi.
//
// Example: {"channel":"slack","default":"eng","rooms":{"C024BE91L":"des"},"unbind":["C0OLD"]}
func (o ops) agentPut(ctx context.Context, in *channelAgentsPut) (*channelAgents, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx); err != nil {
		return nil, zip.ErrForbidden("binding an agent requires org admin")
	}
	channel := strings.TrimSpace(in.Channel)
	if channel == "" {
		return nil, zip.ErrBadRequest("channel is required")
	}
	if _, ok := transportFor(channel); !ok {
		return nil, zip.ErrNotFound("unknown channel")
	}
	st := o.s.State.store
	now := time.Now().Unix()
	if d := strings.TrimSpace(in.Default); d != "" {
		if err := setAgent(ctx, st, org, channel, "", d, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "agent: %v", err)
		}
	}
	for room, agent := range in.Rooms {
		room, agent = strings.TrimSpace(room), strings.TrimSpace(agent)
		if room == "" || agent == "" {
			return nil, zip.ErrBadRequest("rooms maps a room id to an agent; use unbind to remove one")
		}
		if err := setAgent(ctx, st, org, channel, room, agent, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "agent: %v", err)
		}
	}
	for _, room := range in.Unbind {
		if room = strings.TrimSpace(room); room == "" {
			continue
		}
		if err := setAgent(ctx, st, org, channel, room, "", now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "agent: %v", err)
		}
	}
	v, err := agentsFor(ctx, st, org, channel)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "agent: %v", err)
	}
	return &v, nil
}

// serveAgent publishes the binding on the plane. Mount calls it beside
// serveRecent, for the bridges that answer outside the channels turn.
func serveAgent() {
	zip.Post[client.AgentForIn, client.AgentFor](cloud.Plane(), "/channels/agent", planeAgentFor,
		zip.WithOperationID(client.ChannelsAgent),
		zip.WithSummary("The agent that answers one room"))
}

// planeAgentFor answers with the ref. The ORG is the caller's, from the plane
// context and never an argument, for the reason recent.go gives: an org a caller
// could pass is an org whose bindings any caller could read.
func planeAgentFor(ctx context.Context, in *client.AgentForIn) (*client.AgentFor, error) {
	if in == nil || in.Channel == "" {
		return nil, zip.ErrBadRequest("agent: channel is required")
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("agent: no org on the call")
	}
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(503, "agent: channels is not serving")
	}
	return &client.AgentFor{Ref: agentFor(ctx, s.State.store, org, Message{Channel: in.Channel, Room: Room{ID: in.Room}})}, nil
}
