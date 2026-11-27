package transport

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// agent.go — WHICH agent answers a transport, and it is the org's to say.
//
// A turn used to run one agent for every org on a transport, named by a
// deployment variable. That made "several agents in Slack" a deployment change
// rather than a setting, and put the same agent in every workspace on the
// fleet. The binding is a row now, keyed the way the channels are: (org, transport,
// channel). The empty channel is the transport's default for the org; a channel row wins
// over it; and with neither the org's built-in `hanzo` answers, as it always has.
//
// The channel id is the platform's own — a Slack channel id, a Discord channel id,
// a Teams conversation id — the same value the envelope carries and the inbox
// stores, so a binding names exactly what a message arrives in.

// defaultAgent answers when the org has bound nothing.
const defaultAgent = "hanzo"

// agentFor resolves the agent a message runs: the channel's binding, else the
// transport's default for the org, else defaultAgent. A store error falls to the
// default and is logged by the caller's run, never a refused turn — a missing
// binding is not a reason to go quiet.
func agentFor(ctx context.Context, st *store, org string, m Message) string {
	if st != nil {
		for _, channel := range []string{m.Channel.ID, ""} {
			var ref string
			err := st.db.QueryRowContext(ctx,
				`SELECT agent FROM channel_agent WHERE org = $1 AND channel = $2 AND room_id = $3`,
				org, m.Transport, channel).Scan(&ref)
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

// setAgent binds a channel (or, with channel "", the transport default) to an agent.
// Naming the built-in, or nothing, removes the row: a binding to `hanzo` and no
// binding answer the same, so one of them is not stored.
func setAgent(ctx context.Context, st *store, org, transport, channel, agent string, now int64) error {
	if agent == "" || agent == defaultAgent {
		_, err := st.db.ExecContext(ctx, `DELETE FROM channel_agent WHERE org = $1 AND channel = $2 AND room_id = $3`, org, transport, channel)
		return err
	}
	_, err := st.db.ExecContext(ctx, `INSERT INTO channel_agent (org, channel, room_id, agent, updated_at)
  VALUES ($1,$2,$3,$4,$5)
  ON CONFLICT (org, channel, room_id) DO UPDATE SET agent = excluded.agent, updated_at = excluded.updated_at`,
		org, transport, channel, agent, now)
	return err
}

// agentsFor reads every binding the org holds on a transport.
func agentsFor(ctx context.Context, st *store, org, transport string) (transportAgents, error) {
	v := transportAgents{Transport: transport, Default: defaultAgent, Channels: map[string]string{}}
	rows, err := st.db.QueryContext(ctx, `SELECT room_id, agent FROM channel_agent WHERE org = $1 AND channel = $2 ORDER BY room_id`, org, transport)
	if err != nil {
		return v, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var channel, agent string
		if err := rows.Scan(&channel, &agent); err != nil {
			return v, err
		}
		if channel == "" {
			v.Default = agent
		} else {
			v.Channels[channel] = agent
		}
	}
	return v, rows.Err()
}

// transportAgentRef names the transport to read.
type transportAgentRef struct {
	// Transport is the transport: discord, github, linear, slack, teams, telegram or whatsapp.
	// Required; an unknown value is a 404.
	Transport string `json:"transport"`
}

// transportAgents is which agent answers on a transport, as GET returns it and PUT echoes it.
type transportAgents struct {
	// Transport is the transport these bindings are for.
	Transport string `json:"transport"`
	// Default is the agent that answers any channel without a binding of its own;
	// "hanzo" when the org has never set one.
	Default string `json:"default"`
	// Channels maps a platform channel id to the agent that answers there.
	Channels map[string]string `json:"channels"`
}

// transportAgentsPut is a PARTIAL edit: a field that is absent leaves what it names
// alone. Naming the built-in agent (`hanzo`) is how a binding is put back to the
// default, and Unbind is how a channel's own binding is removed.
type transportAgentsPut struct {
	// Transport is the transport to edit. Required; an unknown value is a 404.
	Transport string `json:"transport" url:"-"`
	// Default sets the agent for channels with no binding of their own; "hanzo"
	// restores the built-in. Empty or absent leaves it unchanged.
	Default string `json:"default" url:"-"`
	// Channels binds platform channel ids to agents; channels not named are left alone.
	Channels map[string]string `json:"channels" url:"-"`
	// Unbind removes the bindings of these channels, so they fall back to Default.
	Unbind []string `json:"unbind" url:"-"`
}

// agentGet returns which agent answers the caller org's transport: the default and
// every channel bound to another agent.
//
// Response: {"transport":"slack","default":"eng","channels":{"C024BE91L":"des"}}
func (o ops) agentGet(ctx context.Context, in *transportAgentRef) (*transportAgents, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	transport := strings.TrimSpace(in.Transport)
	if transport == "" {
		return nil, zip.ErrBadRequest("transport query parameter is required")
	}
	if _, ok := transportFor(transport); !ok {
		return nil, zip.ErrNotFound("unknown transport")
	}
	v, err := agentsFor(ctx, o.s.State.store, org, transport)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "agent: %v", err)
	}
	return &v, nil
}

// agentPut binds agents to the caller org's transport and answers the bindings as
// GET would. It requires ORG ADMIN. The agent is named by its ref — the name an
// org gave it at POST /v1/agent, or a built-in such as dev, des or vi.
//
// Example: {"transport":"slack","default":"eng","channels":{"C024BE91L":"des"},"unbind":["C0OLD"]}
func (o ops) agentPut(ctx context.Context, in *transportAgentsPut) (*transportAgents, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx); err != nil {
		return nil, zip.ErrForbidden("binding an agent requires org admin")
	}
	transport := strings.TrimSpace(in.Transport)
	if transport == "" {
		return nil, zip.ErrBadRequest("transport is required")
	}
	if _, ok := transportFor(transport); !ok {
		return nil, zip.ErrNotFound("unknown transport")
	}
	st := o.s.State.store
	now := time.Now().Unix()
	if d := strings.TrimSpace(in.Default); d != "" {
		if err := setAgent(ctx, st, org, transport, "", d, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "agent: %v", err)
		}
	}
	for channel, agent := range in.Channels {
		channel, agent = strings.TrimSpace(channel), strings.TrimSpace(agent)
		if channel == "" || agent == "" {
			return nil, zip.ErrBadRequest("channels maps a channel id to an agent; use unbind to remove one")
		}
		if err := setAgent(ctx, st, org, transport, channel, agent, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "agent: %v", err)
		}
	}
	for _, channel := range in.Unbind {
		if channel = strings.TrimSpace(channel); channel == "" {
			continue
		}
		if err := setAgent(ctx, st, org, transport, channel, "", now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "agent: %v", err)
		}
	}
	v, err := agentsFor(ctx, st, org, transport)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "agent: %v", err)
	}
	return &v, nil
}

// serveAgent publishes the binding on the plane. Mount calls it beside
// serveRecent, for the bridges that answer outside the transport turn.
func serveAgent() {
	cloud.Plane().Post("/transport/agent", planeAgentFor,
		zip.WithOperationID(client.TransportAgent),
		zip.WithSummary("The agent that answers one channel"))
}

// planeAgentFor answers with the ref. The ORG is the caller's, from the plane
// context and never an argument, for the reason recent.go gives: an org a caller
// could pass is an org whose bindings any caller could read.
func planeAgentFor(ctx context.Context, in *client.AgentForIn) (*client.AgentFor, error) {
	if in == nil || in.Transport == "" {
		return nil, zip.ErrBadRequest("agent: transport is required")
	}
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("agent: no org on the call")
	}
	s := mounted.Load()
	if s == nil {
		return nil, zip.Errorf(503, "agent: transport is not serving")
	}
	return &client.AgentFor{Ref: agentFor(ctx, s.State.store, org, Message{Transport: in.Transport, Channel: Channel{ID: in.Channel}})}, nil
}
