package channels

// routes.go — the /v1/channels HTTP surface. Every route is org-gated
// (principal.Org) and covered by cloud.Terminal: channels mounts after the
// commerce /v1 error-flattening filter, so Terminal writes the real 4xx in-band
// before that filter can rewrite it to 500 (service.go). The typed ops take it
// as subtree MIDDLEWARE, the raw send route as its per-route wrapper — one
// flattening, two ways of reaching the same handlers.
// Mutations (pairing approve, allowlist put) additionally require org admin.
// There are NO public routes here — platform webhooks stay in integrations.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/integrations"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// sendMaxBody bounds one POST /send body (attachments are URLs, not bytes).
const sendMaxBody = 1 << 20 // 1 MiB

// routes registers the /v1/channels surface. The :channel send route is LAST:
// zip matches in registration order, so the static paths above must win.
//
// Every route but send is a zip TYPED op — one declaration the router, the
// OpenAPI document, the MCP tool list and the CLI all read. Send stays raw
// because its body is decoded with DisallowUnknownFields under a 1 MiB cap,
// which is the identity guard C2-6 rests on and which the typed decoder has no
// vocabulary for.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	g := app.Group("/v1/channels")
	// Both middlewares FIRST: fiber runs them in registration order, so ones
	// installed after these leaves would never run.
	//
	//   - Bridge carries the request (and with it the validated org) across the
	//     typed seam, which hands a handler only its context and its input.
	//   - the SAME in-band flattening cloud.Terminal gives the raw handlers,
	//     applied here as MIDDLEWARE because zip registers a typed op's route
	//     itself and there is no per-route wrapper to compose. Without it a
	//     typed op's 403/404/409 would propagate into the commerce /v1
	//     error-flattening filter mounted ahead of channels and reach the client
	//     as 500.
	g.Use(cloud.Bridge(), cloud.Terminal(func(c *zip.Ctx) error { return c.Continue() }))
	zip.Get(z, "/v1/channels", o.list)
	zip.Get(z, "/v1/channels/inbox", o.inbox)
	zip.Get(z, "/v1/channels/pairing", o.pairing)
	zip.Post(z, "/v1/channels/pairing/approve", o.approve)
	zip.Get(z, "/v1/channels/allowlist", o.allowlist)
	zip.Put(z, "/v1/channels/allowlist", o.setAllowlist)
	g.Post("/:channel/send", cloud.Terminal(cloud.Handle(s, send)))
}

// ops binds the subsystem to its typed handlers: a TypedHandler takes only a
// context and its input, so the service arrives as a RECEIVER.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's principal: it
// takes nothing off the wire.
type noInput struct{}

// orgOf resolves the validated tenant a typed op is answering for. It is
// principal.Org read off the request the bridge parked, so a typed op and a raw
// handler refuse an unauthenticated caller identically.
func orgOf(ctx context.Context) (string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	org, ok := principal.Org(c)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
}

// requireOrgAdmin admits a SuperAdmin or an admin of the caller's OWN org — the
// gate every channels MUTATION carries, stated once.
func requireOrgAdmin(ctx context.Context, what string) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return zip.ErrForbidden(what)
	}
	if !(principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c)) {
		return zip.ErrForbidden(what)
	}
	return nil
}

// ── JSON projections (camelCase, closed shapes) ──────────────────────────────

type channelView struct {
	// ID is the transport id: discord, slack, teams or telegram.
	ID string `json:"id"`
	// Connected reports whether this org has an authenticated connection to it.
	Connected bool `json:"connected"`
	// Account is the connected account's external id, lower-cased.
	Account string `json:"account"`
	// AccountLabel is that account's human label — never swapped with Account.
	AccountLabel string `json:"accountLabel"`
	// Capabilities is what the transport renders natively.
	Capabilities capabilities `json:"capabilities"`
	// DMPolicy is who may DM the org on this channel: pairing, allowlist or open.
	DMPolicy DMPolicy `json:"dmPolicy"`
	// GroupPolicy governs group and thread surfaces: open, allowlist or disabled.
	GroupPolicy GroupPolicy `json:"groupPolicy"`
	// PendingPairing is how many unexpired pairing requests await approval here.
	PendingPairing int `json:"pendingPairing"`
}

// channelList is the deterministic transport listing.
type channelList struct {
	// Channels is every transport in the closed registry, in fixed order —
	// present whether or not the org has connected it.
	Channels []channelView `json:"channels"`
}

type inboxView struct {
	// ID is the row id, and the cursor to resume from.
	ID int64 `json:"id"`
	// Channel is the transport the message arrived on.
	Channel string `json:"channel"`
	// Account is the connected account that received it.
	Account string `json:"account"`
	// RoomID is the transport's room identifier.
	RoomID string `json:"roomId"`
	// RoomKind is dm, group or thread.
	RoomKind string `json:"roomKind"`
	// Sender is the transport-side sender identity.
	Sender string `json:"sender"`
	// SenderUser is the Hanzo user it maps to, when one is known.
	SenderUser string `json:"senderUser,omitempty"`
	// Text is the message body, downgraded to text.
	Text string `json:"text"`
	// ReplyTo is the message this one replied to, when any.
	ReplyTo string `json:"replyTo,omitempty"`
	// CreatedAt is unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// inboxQuery bounds one inbox read. The org is NOT here and can never be: it is
// the validated principal's tenant, and an input field is caller-supplied.
type inboxQuery struct {
	// Since is the exclusive row-id cursor to resume from; 0 starts at the oldest.
	Since int64 `json:"since"`
	// Limit caps the rows returned; 0 takes the store's own default.
	Limit int `json:"limit"`
}

// inboxPage is one page of stored inbound messages, oldest first.
type inboxPage struct {
	// Messages is the page, oldest first.
	Messages []inboxView `json:"messages"`
	// Cursor is the last row id in this page — pass it back as since.
	// An empty page returns the cursor it was given.
	Cursor int64 `json:"cursor"`
}

type pairingView struct {
	// Channel is the transport the request arrived on.
	Channel string `json:"channel"`
	// Sender is the transport-side identity asking to be paired.
	Sender string `json:"sender"`
	// Code is the capability string shown to org members for approval.
	Code string `json:"code"`
	// CreatedAt is unix seconds of the first sighting.
	CreatedAt int64 `json:"createdAt"`
	// LastSeen is unix seconds of the most recent sighting.
	LastSeen int64 `json:"lastSeen"`
}

// pairingPending is the org's unexpired pairing requests.
type pairingPending struct {
	// Pending is every unexpired request awaiting an admin decision.
	Pending []pairingView `json:"pending"`
}

// pairingApproval names the pending request to approve.
type pairingApproval struct {
	// Channel is the transport the request arrived on.
	Channel string `json:"channel"`
	// Code is the pairing code shown to the org, exactly as issued.
	Code string `json:"code"`
}

// pairingApproved is what an approval turns the code into.
type pairingApproved struct {
	// Sender is the transport-side identity now allowed to DM the org.
	Sender string `json:"sender"`
	// OwnerBootstrapped reports that this approval also seated the org's first
	// owner — the one-time bootstrap, false on every later approval.
	OwnerBootstrapped bool `json:"ownerBootstrapped"`
}

// channelRef names one channel of the closed transport registry.
type channelRef struct {
	// Channel is the transport id: discord, slack, teams or telegram.
	Channel string `json:"channel"`
}

// allowlistUpdate is a partial edit of one channel's access policy. Every field
// but Channel is optional — an omitted one is left exactly as it was.
type allowlistUpdate struct {
	// Channel is the transport id to edit: discord, slack, teams or telegram.
	Channel string `json:"channel"`
	// DMPolicy sets who may DM the org: pairing, allowlist or open.
	// Empty leaves it unchanged.
	DMPolicy string `json:"dmPolicy"`
	// GroupPolicy sets group and thread access: open, allowlist or disabled.
	// Empty leaves it unchanged.
	GroupPolicy string `json:"groupPolicy"`
	// DM REPLACES the config-source DM allowlist.
	// Null leaves it unchanged; approved pairings are a different source and are
	// never revoked by this edit.
	DM []string `json:"dm"`
	// Group REPLACES the config-source group allowlist. Null leaves it unchanged.
	Group []string `json:"group"`
	// AccessGroups REPLACES the org's named access groups, keyed group then
	// channel. Null leaves them unchanged.
	AccessGroups map[string]map[string][]string `json:"accessGroups"`
}

type allowlistView struct {
	// DMPolicy is who may DM the org on this channel.
	DMPolicy DMPolicy `json:"dmPolicy"`
	// GroupPolicy governs group and thread surfaces on this channel.
	GroupPolicy GroupPolicy `json:"groupPolicy"`
	// DM is the config-source DM allowlist — the entries this endpoint owns.
	DM []string `json:"dm"`
	// Group is the config-source group allowlist.
	Group []string `json:"group"`
	// Paired is the pairing-source entries, read-only here: they are minted by
	// approving a pairing and cannot be edited away by a policy write.
	Paired []string `json:"paired"`
	// AccessGroups is the org's named access groups, keyed group then channel.
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

// list returns every chat transport with the caller org's facts attached.
//
// The registry is closed and the order is fixed, so an unconnected transport is
// still listed — with connected:false, its default policy, and no account.
// Each row carries the org's connection, its DM and group policy, and how many
// pairing requests are waiting for an admin.
//
// Response: {"channels": [{"id": "slack", "connected": true, "account": "t01abc", "accountLabel": "Acme", "capabilities": {"dm": true, "group": true, "thread": true, "media": false, "actions": false}, "dmPolicy": "pairing", "groupPolicy": "open", "pendingPairing": 2}]}
func (o ops) list(ctx context.Context, _ *noInput) (*channelList, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	pending := map[string]int{}
	rows, err := listPairing(ctx, s.State.store, org, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "pairing: %v", err)
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
	return &channelList{Channels: out}, nil
}

// inbox returns the org's stored inbound messages, oldest first.
//
// It is a cursor read: pass the cursor a page returns back as since to get the
// next one, and an empty page returns the cursor it was given.
// Only the caller's own org's messages are ever visible.
//
// Example: {"since": 4210, "limit": 50}
// Response: {"messages": [{"id": 4211, "channel": "slack", "account": "t01abc", "roomId": "D01", "roomKind": "dm", "sender": "u01", "text": "ship it", "createdAt": 1780000000}], "cursor": 4211}
func (o ops) inbox(ctx context.Context, in *inboxQuery) (*inboxPage, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	since, limit := in.Since, in.Limit
	rows, err := s.State.store.listInbox(ctx, org, since, limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "inbox: %v", err)
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
	return &inboxPage{Messages: msgs, Cursor: cursor}, nil
}

// pairing returns the org's pending, unexpired pairing requests.
//
// A code is a capability string an org member shows an admin to be approved, so
// it is returned here and never logged.
// Expired requests are not listed.
//
// Response: {"pending": [{"channel": "telegram", "sender": "5551234", "code": "pair-7f3a", "createdAt": 1780000000, "lastSeen": 1780000600}]}
func (o ops) pairing(ctx context.Context, _ *noInput) (*pairingPending, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := listPairing(ctx, s.State.store, org, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "pairing: %v", err)
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
	return &pairingPending{Pending: out}, nil
}

// approve turns a pending pairing code into a standing allow entry.
//
// It is admin-gated: a SuperAdmin, or an admin of the caller's own org.
// The first approval an org ever makes also seats its owner, reported as
// ownerBootstrapped; every later one does not.
// An unknown or expired code is not found, and nothing is written.
//
// Example: {"channel": "telegram", "code": "pair-7f3a"}
// Response: {"sender": "5551234", "ownerBootstrapped": false}
func (o ops) approve(ctx context.Context, in *pairingApproval) (*pairingApproved, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, "approving a pairing requires org admin"); err != nil {
		return nil, err
	}
	channel := strings.TrimSpace(in.Channel)
	code := strings.TrimSpace(in.Code)
	if channel == "" || code == "" {
		return nil, zip.ErrBadRequest("channel and code are required")
	}
	sender, ownerBoot, ok, err := approvePairing(ctx, s.State.store, org, channel, code, time.Now().Unix())
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "approve: %v", err)
	}
	if !ok {
		return nil, zip.ErrNotFound("unknown or expired code")
	}
	return &pairingApproved{Sender: sender, OwnerBootstrapped: ownerBoot}, nil
}

// allowlist returns one channel's access policy for the caller's org.
//
// It answers with both allow sources: the config-source entries this surface
// owns, and the pairing-source entries approving a pairing minted, which are
// read-only here.
// A channel outside the closed transport registry is not found.
//
// Example: {"channel": "slack"}
// Response: {"dmPolicy": "pairing", "groupPolicy": "open", "dm": ["u01"], "group": [], "paired": ["u07"], "accessGroups": {}}
func (o ops) allowlist(ctx context.Context, in *channelRef) (*allowlistView, error) {
	s := o.s
	org, err := orgOf(ctx)
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
	v, err := allowlistFor(ctx, s.State.store, org, channel)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "allowlist: %v", err)
	}
	return &v, nil
}

// setAllowlist edits one channel's access policy and returns it as it now stands.
//
// It is admin-gated: a SuperAdmin, or an admin of the caller's own org.
// The edit is partial — an omitted policy or a null list is left exactly as it
// was — and it owns the CONFIG source only, so a policy write can never revoke
// an approved pairing.
// The answer is the same shape the read returns, so both verbs speak one shape.
//
// Example: {"channel": "slack", "dmPolicy": "allowlist", "dm": ["u01", "u02"]}
// Response: {"dmPolicy": "allowlist", "groupPolicy": "open", "dm": ["u01", "u02"], "group": [], "paired": ["u07"], "accessGroups": {}}
func (o ops) setAllowlist(ctx context.Context, in *allowlistUpdate) (*allowlistView, error) {
	s := o.s
	org, err := orgOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, "editing the allowlist requires org admin"); err != nil {
		return nil, err
	}
	channel := strings.TrimSpace(in.Channel)
	if channel == "" {
		return nil, zip.ErrBadRequest("channel is required")
	}
	if _, ok := transportFor(channel); !ok {
		return nil, zip.ErrNotFound("unknown channel")
	}
	dm, group := DMPolicy(in.DMPolicy), GroupPolicy(in.GroupPolicy)
	switch dm {
	case "", DMPairing, DMAllowlist, DMOpen:
	default:
		return nil, zip.ErrBadRequest("dmPolicy must be pairing, allowlist, or open")
	}
	switch group {
	case "", GroupOpen, GroupAllowlist, GroupDisabled:
	default:
		return nil, zip.ErrBadRequest("groupPolicy must be open, allowlist, or disabled")
	}
	st := s.State.store
	now := time.Now().Unix()
	if dm != "" || group != "" {
		p, err := policyFor(ctx, st, org, channel)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "policy: %v", err)
		}
		if dm != "" {
			p.DM = dm
		}
		if group != "" {
			p.Group = group
		}
		if err := setPolicy(ctx, st, org, channel, p, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "policy: %v", err)
		}
	}
	// channel_allow two-writer split: this PUT owns ONLY config-source rows
	// (putAllow); pairing-source rows belong to approvePairing — a policy edit
	// can never revoke an approved pairing.
	if in.DM != nil {
		if err := putAllow(ctx, st, org, channel, "dm", in.DM, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "allowlist: %v", err)
		}
	}
	if in.Group != nil {
		if err := putAllow(ctx, st, org, channel, "group", in.Group, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "allowlist: %v", err)
		}
	}
	if in.AccessGroups != nil {
		if err := putAccessGroups(ctx, st, org, in.AccessGroups); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "access groups: %v", err)
		}
	}
	v, err := allowlistFor(ctx, st, org, channel)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "allowlist: %v", err)
	}
	return &v, nil
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
