package channels

// routes.go — the /v1/channels HTTP surface. Every route is org-gated
// (principal.Org) and wrapped cloud.Terminal(cloud.Handle(...)): channels
// mounts after the commerce /v1 error-flattening filter, so Terminal writes
// the real 4xx in-band before that filter can rewrite it to 500 (service.go).
// Mutations (pairing approve, allowlist put) additionally require org admin.
// There are NO public routes here — platform webhooks stay in integrations.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/integrations"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// sendMaxBody bounds one POST /send body (attachments are URLs, not bytes).
const sendMaxBody = 1 << 20 // 1 MiB

// routes registers the /v1/channels surface. The :channel send route is LAST:
// zip matches in registration order, so the static paths above must win.
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/channels")
	app.Get("/v1/channels", cloud.Terminal(cloud.Handle(s, list)))
	g.Get("/inbox", cloud.Terminal(cloud.Handle(s, inbox)))
	g.Get("/pairing", cloud.Terminal(cloud.Handle(s, pairingList)))
	g.Post("/pairing/approve", cloud.Terminal(cloud.Handle(s, pairingApprove)))
	g.Get("/allowlist", cloud.Terminal(cloud.Handle(s, allowlistGet)))
	g.Put("/allowlist", cloud.Terminal(cloud.Handle(s, allowlistPut)))
	g.Post("/:channel/send", cloud.Terminal(cloud.Handle(s, send)))
}

// ── JSON projections (camelCase, closed shapes) ──────────────────────────────

type channelView struct {
	ID             string       `json:"id"`
	Connected      bool         `json:"connected"`
	Account        string       `json:"account"`
	AccountLabel   string       `json:"accountLabel"`
	Capabilities   capabilities `json:"capabilities"`
	DMPolicy       DMPolicy     `json:"dmPolicy"`
	GroupPolicy    GroupPolicy  `json:"groupPolicy"`
	PendingPairing int          `json:"pendingPairing"`
}

type inboxView struct {
	ID         int64  `json:"id"`
	Channel    string `json:"channel"`
	Account    string `json:"account"`
	RoomID     string `json:"roomId"`
	RoomKind   string `json:"roomKind"`
	Sender     string `json:"sender"`
	SenderUser string `json:"senderUser,omitempty"`
	Text       string `json:"text"`
	ReplyTo    string `json:"replyTo,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
}

type pairingView struct {
	Channel   string `json:"channel"`
	Sender    string `json:"sender"`
	Code      string `json:"code"`
	CreatedAt int64  `json:"createdAt"`
	LastSeen  int64  `json:"lastSeen"`
}

type allowlistView struct {
	DMPolicy     DMPolicy                       `json:"dmPolicy"`
	GroupPolicy  GroupPolicy                    `json:"groupPolicy"`
	DM           []string                       `json:"dm"`
	Group        []string                       `json:"group"`
	Paired       []string                       `json:"paired"`
	AccessGroups map[string]map[string][]string `json:"accessGroups"`
}

// nonNil keeps list fields JSON arrays ([] not null) — integrations idiom.
func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ── handlers ─────────────────────────────────────────────────────────────────

// list is GET /v1/channels — the deterministic transport listing (registry
// order) with the org's connection, policy, and pending-pairing facts.
func list(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	ctx := c.Context()
	pending := map[string]int{}
	rows, err := listPairing(ctx, s.State.store, org, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "pairing: %v", err)
	}
	for _, r := range rows {
		pending[r.Channel]++
	}
	out := make([]channelView, 0, len(transports))
	for _, tr := range transports {
		conn, connected := integrations.ConnectionFor(org, tr.id)
		// Absent row ⇒ defaults (policyFor); a read error leaves zero policy
		// fields rather than failing the whole listing.
		p, _ := policyFor(ctx, s.State.store, org, tr.id)
		out = append(out, channelView{
			ID:        tr.id,
			Connected: connected,
			// C2-7: account is the id-shaped fact (lowercased external id),
			// accountLabel the human label — never swapped, on any surface.
			Account:        strings.ToLower(conn.ExternalID),
			AccountLabel:   conn.AccountLabel,
			Capabilities:   tr.caps,
			DMPolicy:       p.DM,
			GroupPolicy:    p.Group,
			PendingPairing: pending[tr.id],
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"channels": out})
}

// inbox is GET /v1/channels/inbox?since=&limit= — the org's stored inbound
// messages, oldest first; cursor is the last row id (or since when empty).
func inbox(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var since int64
	if q := c.Query("since"); q != "" {
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			return zip.ErrBadRequest("since must be an integer cursor")
		}
		since = v
	}
	var limit int
	if q := c.Query("limit"); q != "" {
		v, err := strconv.Atoi(q)
		if err != nil {
			return zip.ErrBadRequest("limit must be an integer")
		}
		limit = v
	}
	rows, err := s.State.store.listInbox(c.Context(), org, since, limit)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "inbox: %v", err)
	}
	msgs := make([]inboxView, 0, len(rows))
	cursor := since
	for _, r := range rows {
		msgs = append(msgs, inboxView{
			ID:         r.ID,
			Channel:    r.Channel,
			Account:    r.Account,
			RoomID:     r.RoomID,
			RoomKind:   string(r.RoomKind),
			Sender:     r.Sender,
			SenderUser: r.SenderUser,
			Text:       r.Text,
			ReplyTo:    r.ReplyTo,
			CreatedAt:  r.CreatedAt,
		})
		cursor = r.ID
	}
	return c.JSON(http.StatusOK, map[string]any{"messages": msgs, "cursor": cursor})
}

// pairingList is GET /v1/channels/pairing — the org's pending (unexpired)
// pairing requests. Codes are capability strings shown to org members for
// admin approval; they are returned here, never logged.
func pairingList(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	rows, err := listPairing(c.Context(), s.State.store, org, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "pairing: %v", err)
	}
	out := make([]pairingView, 0, len(rows))
	for _, r := range rows {
		out = append(out, pairingView{
			Channel:   r.Channel,
			Sender:    r.Sender,
			Code:      r.Code,
			CreatedAt: r.CreatedAt,
			LastSeen:  r.LastSeen,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"pending": out})
}

// pairingApprove is POST /v1/channels/pairing/approve — admin-gated; turns a
// pending code into a pairing-source allow entry (approvePairing owns the
// write and the one-time owner bootstrap).
func pairingApprove(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !(principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c)) {
		return zip.ErrForbidden("approving a pairing requires org admin")
	}
	var body struct {
		Channel string `json:"channel"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	channel := strings.TrimSpace(body.Channel)
	code := strings.TrimSpace(body.Code)
	if channel == "" || code == "" {
		return zip.ErrBadRequest("channel and code are required")
	}
	sender, ownerBoot, ok, err := approvePairing(c.Context(), s.State.store, org, channel, code, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "approve: %v", err)
	}
	if !ok {
		return zip.ErrNotFound("unknown or expired code")
	}
	return c.JSON(http.StatusOK, map[string]any{"sender": sender, "ownerBootstrapped": ownerBoot})
}

// allowlistGet is GET /v1/channels/allowlist?channel=.
func allowlistGet(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	channel := strings.TrimSpace(c.Query("channel"))
	if channel == "" {
		return zip.ErrBadRequest("channel query parameter is required")
	}
	if _, ok := transportFor(channel); !ok {
		return zip.ErrNotFound("unknown channel")
	}
	v, err := allowlistFor(c.Context(), s.State.store, org, channel)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "allowlist: %v", err)
	}
	return c.JSON(http.StatusOK, v)
}

// allowlistPut is PUT /v1/channels/allowlist — admin-gated; each body field is
// applied only when provided (nil slice / empty string = untouched), then the
// GET payload is echoed so both verbs return ONE shape.
func allowlistPut(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !(principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c)) {
		return zip.ErrForbidden("editing the allowlist requires org admin")
	}
	var body struct {
		Channel      string                         `json:"channel"`
		DMPolicy     string                         `json:"dmPolicy"`
		GroupPolicy  string                         `json:"groupPolicy"`
		DM           []string                       `json:"dm"`
		Group        []string                       `json:"group"`
		AccessGroups map[string]map[string][]string `json:"accessGroups"`
	}
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	channel := strings.TrimSpace(body.Channel)
	if channel == "" {
		return zip.ErrBadRequest("channel is required")
	}
	if _, ok := transportFor(channel); !ok {
		return zip.ErrNotFound("unknown channel")
	}
	dm, group := DMPolicy(body.DMPolicy), GroupPolicy(body.GroupPolicy)
	switch dm {
	case "", DMPairing, DMAllowlist, DMOpen:
	default:
		return zip.ErrBadRequest("dmPolicy must be pairing, allowlist, or open")
	}
	switch group {
	case "", GroupOpen, GroupAllowlist, GroupDisabled:
	default:
		return zip.ErrBadRequest("groupPolicy must be open, allowlist, or disabled")
	}
	ctx := c.Context()
	st := s.State.store
	now := time.Now().Unix()
	if dm != "" || group != "" {
		p, err := policyFor(ctx, st, org, channel)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "policy: %v", err)
		}
		if dm != "" {
			p.DM = dm
		}
		if group != "" {
			p.Group = group
		}
		if err := setPolicy(ctx, st, org, channel, p, now); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "policy: %v", err)
		}
	}
	// channel_allow two-writer split: this PUT owns ONLY config-source rows
	// (putAllow); pairing-source rows belong to approvePairing — a policy edit
	// can never revoke an approved pairing.
	if body.DM != nil {
		if err := putAllow(ctx, st, org, channel, "dm", body.DM, now); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "allowlist: %v", err)
		}
	}
	if body.Group != nil {
		if err := putAllow(ctx, st, org, channel, "group", body.Group, now); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "allowlist: %v", err)
		}
	}
	if body.AccessGroups != nil {
		if err := putAccessGroups(ctx, st, org, body.AccessGroups); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "access groups: %v", err)
		}
	}
	v, err := allowlistFor(ctx, st, org, channel)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "allowlist: %v", err)
	}
	return c.JSON(http.StatusOK, v)
}

// allowlistFor builds the allowlist payload GET returns and PUT echoes.
func allowlistFor(ctx context.Context, st *store, org, channel string) (allowlistView, error) {
	p, err := policyFor(ctx, st, org, channel)
	if err != nil {
		return allowlistView{}, err
	}
	// paired = pairing-source rows, minted only by approvePairing (dm scope —
	// pairing is a DM concept); surfaced read-only so admins see who is paired.
	dm, paired, err := allowEntries(ctx, st, org, channel, "dm")
	if err != nil {
		return allowlistView{}, err
	}
	group, _, err := allowEntries(ctx, st, org, channel, "group")
	if err != nil {
		return allowlistView{}, err
	}
	groups, err := listAccessGroups(ctx, st, org)
	if err != nil {
		return allowlistView{}, err
	}
	return allowlistView{
		DMPolicy:     p.DM,
		GroupPolicy:  p.Group,
		DM:           nonNil(dm),
		Group:        nonNil(group),
		Paired:       nonNil(paired),
		AccessGroups: groups,
	}, nil
}

// listAccessGroups reads the org's access groups (name → channel → entries) —
// the read mirror of putAccessGroups (policy.go), for the allowlist payload.
func listAccessGroups(ctx context.Context, st *store, org string) (map[string]map[string][]string, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT name, channel, entry FROM channel_access_group
  WHERE org = ? ORDER BY name, channel, entry`, org)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]map[string][]string{}
	for rows.Next() {
		var name, channel, entry string
		if err := rows.Scan(&name, &channel, &entry); err != nil {
			return nil, err
		}
		if out[name] == nil {
			out[name] = map[string][]string{}
		}
		out[name][channel] = append(out[name][channel], entry)
	}
	return out, rows.Err()
}

// send is POST /v1/channels/:channel/send — the ONE egress door. The body is
// the envelope's narrow outbound projection (C2-6): identity fields (sender,
// account, channel) are not decodable — DisallowUnknownFields rejects them
// loudly instead of silently dropping them.
func send(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	raw := c.Body()
	if len(raw) > sendMaxBody {
		return zip.ErrBadRequest("body exceeds 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var r SendRequest
	if err := dec.Decode(&r); err != nil {
		return zip.ErrBadRequest("invalid body: " + err.Error())
	}
	if err := r.validate(); err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	channel := strings.TrimSpace(c.Param("channel"))
	tr, ok := transportFor(channel)
	if !ok {
		return zip.ErrNotFound("unknown channel")
	}
	m := Message{
		Channel:     channel,
		Room:        r.Room,
		Text:        r.Text,
		Attachments: r.Attachments,
		Actions:     r.Actions,
		ReplyTo:     r.ReplyTo,
		Idempotency: r.Idempotency,
	}
	ctx := c.Context()
	st := s.State.store
	if r.Idempotency != "" {
		fresh, prior, err := st.markSend(ctx, org, channel, r.Idempotency, time.Now().Unix())
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "idempotency: %v", err)
		}
		if !fresh {
			return c.JSON(http.StatusOK, prior)
		}
	}
	d, err := tr.send(ctx, s, org, m)
	if err != nil {
		// C2-1: release the claimed key in the SAME error path so the caller
		// can re-attempt; only a completed send replays a receipt.
		if r.Idempotency != "" {
			_ = st.unmarkSend(ctx, org, channel, r.Idempotency)
		}
		// The transports' typed refusals (errRoomNotBound telegram.go,
		// errNoRoute discord.go) map to a status HERE, in one place: 403 —
		// the room is not org-bound; 409 — no inbound-learned route yet.
		switch {
		case errors.Is(err, errRoomNotBound):
			return zip.ErrForbidden("room is not bound to this org")
		case errors.Is(err, errNoRoute):
			return zip.ErrConflict("no inbound route for this room; the bot must be messaged there first")
		}
		// Door errors carry status/shape only — never tokens (SendSlack /
		// SendDiscord contract, integrations/ingress.go).
		return zip.Errorf(http.StatusBadGateway, "%s: %v", tr.id, err)
	}
	if r.Idempotency != "" {
		// Best-effort: the message is delivered; a lost receipt only degrades
		// a later replay to an empty Delivery (documented markSend tradeoff).
		if err := st.finishSend(ctx, org, channel, r.Idempotency, d.MessageID); err != nil {
			s.Log.Warn("channels: send receipt", "channel", channel, "err", err)
		}
	}
	return c.JSON(http.StatusOK, d)
}
