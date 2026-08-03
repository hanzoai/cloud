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
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/integrations"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// sendMaxBody bounds one POST /send body (attachments are URLs, not bytes).
const sendMaxBody = 1 << 20 // 1 MiB

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/channels openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes registers the /v1/channels surface. The :channel send route is LAST:
// zip matches in registration order, so the static paths above must win.
//
// Six of the seven are TYPED ops — one registry entry each, which is what the
// OpenAPI operation, the MCP tool, the CLI command and every generated SDK
// method are all projected from. The seventh is named at its registration.
//
// The typed ops keep the cloud.Terminal wrapper the untyped ones carried:
// channels mounts after the commerce /v1 error-flattening filter, so Terminal is
// what writes the real 4xx in-band before that filter can rewrite it to 500. It
// composes at registration time through zip's With, so a typed op is gated
// exactly as the untyped route beside it.
func routes(app cloud.Router, s *cloud.Service[state]) error {
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("channels.Mount: router exposes no zip.App, so no typed op could be registered")
	}
	g := app.Group("/v1/channels")
	// The Bridge FIRST, bounded to the subtree channels owns: a typed op receives
	// only a context, so the validated org has to be parked there, and fiber runs
	// middleware in registration order — one installed after these leaves would
	// never run. cloud.Listen installs one app-wide too; nesting is harmless, and
	// having it here is what makes this package's own tests — which mount on a
	// bare app — exercise the same tenancy the binary does.
	g.Use(cloud.Bridge())

	o := ops{s: s}
	// cloud.Terminal is func(func(*zip.Ctx) error) func(*zip.Ctx) error; zip.Middleware
	// names the same shape over the defined Handler type, so it is adapted here
	// rather than by widening either signature.
	//
	// The two groups exist so the COLLECTION ROOT can be declared as a non-empty
	// leaf of its parent: joinPath normalises an empty leaf to "/", so
	// `zip.Get(ch, "", …)` would name /v1/channels/ — a path this API has never
	// served — and op.Path is the identity every projection keys on.
	terminal := func(next zip.Handler) zip.Handler { return cloud.Terminal(next) }
	v1 := zapp.With(terminal).Group("/v1")
	ch := v1.Group("/channels")
	zip.Get(v1, "/channels", o.list)
	zip.Get(ch, "/inbox", o.inbox)
	zip.Get(ch, "/pairing", o.pairingList)
	zip.Post(ch, "/pairing/approve", o.pairingApprove)
	zip.Get(ch, "/allowlist", o.allowlistGet)
	zip.Put(ch, "/allowlist", o.allowlistPut)

	// UNTYPED BY DESIGN — send has TWO independent blockers, both wire facts.
	//
	//  1. A PACKAGE-LOCAL body cap. It reads c.Body() and refuses anything over
	//     sendMaxBody (1 MiB) with 400 "body exceeds 1 MiB". A typed op never sees
	//     the raw bytes — zip decodes first — and cloud's global zip BodyLimit is
	//     far larger, so the cap would silently vanish.
	//  2. DisallowUnknownFields. The outbound body is the envelope's NARROW
	//     projection (C2-6): identity fields (sender, account, channel) are
	//     deliberately NOT decodable, and a request carrying one is refused LOUDLY
	//     with 400 rather than having it silently dropped. zip's decode is
	//     jsonenc.Unmarshal with no strictness option, so every one of those
	//     requests would start succeeding with the field ignored — the silent
	//     acceptance this route exists to prevent.
	//
	// A third fact would have to move too: an idempotent replay answers 200 with
	// the PRIOR Delivery receipt rather than re-sending. apps/channels/typed_wire_test.go
	// holds this route as a CLOSED list and pins both refusals.
	g.Post("/:channel/send", cloud.Terminal(cloud.Handle(s, send)))
	return nil
}

// ops binds the store to the typed channels ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value, which is also the only
// bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op addressed entirely by the caller's own validated
// principal: it takes nothing off the wire.
type noInput struct{}

// tenant is the VALIDATED org for a typed op — the one the gateway asserted and
// cloud.Bridge parked on the context, never a field of In. An In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself. Fails closed off the HTTP path.
func tenant(ctx context.Context) (string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return org, nil
}

// requireOrgAdmin is the mutation gate, and it is the ONE reason this package
// reaches for the REQUEST. Approving a pairing or editing an allowlist decides
// who may talk to the org's bots, so it takes admin of the org — which is
// X-User-IsSuperAdmin / X-User-IsOrgAdmin, two claims principal.OrgFrom does not
// carry and that must never become In fields a caller could assert for itself.
// Fails closed off the HTTP path: no request, no attested admin, no mutation.
func requireOrgAdmin(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if !(principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c)) {
		return zip.ErrForbidden("org admin required")
	}
	return nil
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

// chatChannels is the transport listing as one read answers it.
//
// The name is QUALIFIED with the product because the schema namespace is FLAT
// across the whole fleet: apps/content already publishes a `channelList` for its
// SOCIAL channels, and openapi.Weave refuses one name with two shapes ("every
// generated SDK would bind whichever it read last"). These are chat transports,
// so that is what the type is called; the wire key stays `channels`.
type chatChannels struct {
	// Channels is every chat transport this deployment supports, in a fixed
	// order, each carrying whether the org has connected it, the account behind
	// the connection, what the transport can do, the org's DM/group access
	// policies for it, and how many pairing requests are waiting.
	Channels []channelView `json:"channels"`
}

// list returns every chat transport channels can talk to — Discord, Slack, Teams
// and Telegram — with the caller org's own facts on each: whether it is
// connected and to which account, what the transport supports, the org's DM and
// group access policies, and how many pairing requests are pending approval. The
// order is fixed, so a console can render the same rows every time. A policy that
// cannot be read leaves that channel's policy fields empty rather than failing
// the whole listing.
func (o ops) list(ctx context.Context, _ *noInput) (*chatChannels, error) {
	s := o.s
	org, err := tenant(ctx)
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
		conn, connected := integrations.ConnectionFor(org, tr.id, "")
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
	return &chatChannels{Channels: out}, nil
}

// inboxIn pages the org's inbound message log.
//
// Both fields are STRINGS, not numbers, and deliberately so: this route answers
// 400 for a value it cannot parse as an integer, and zip's URL binder silently
// leaves an unparseable value at the field's zero — so an int64 field would turn
// `?since=abc` from today's 400 into a silent read from the beginning. Parsing
// here keeps the refusal exactly where the wire has it.
type inboxIn struct {
	// Since is the exclusive cursor: only messages with a higher row id come
	// back. Empty starts at the beginning. Must parse as an integer.
	Since string `json:"since"`
	// Limit caps how many messages come back. Empty or 0 uses the store's
	// default page size. Must parse as an integer.
	Limit string `json:"limit"`
}

// inboxPage is one page of the org's inbound messages.
type inboxPage struct {
	// Cursor is the row id to pass back as `since` for the next page. It is the
	// last message's id, or the requested cursor when the page is empty.
	Cursor int64 `json:"cursor"`
	// Messages are the inbound messages, oldest first.
	Messages []inboxView `json:"messages"`
}

// inbox returns the messages people have sent to the caller org's connected chat
// bots, oldest first, in the portable envelope shape every transport normalises
// into. It is a CURSOR feed, not a search: pass the returned cursor back as
// `since` to get only what has arrived since. Only this org's messages are
// stored under this org, so the feed can never carry another tenant's chat.
//
// Example: {"since": "1042", "limit": "100"}
func (o ops) inbox(ctx context.Context, in *inboxIn) (*inboxPage, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	var since int64
	if q := strings.TrimSpace(in.Since); q != "" {
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			return nil, zip.ErrBadRequest("since must be an integer cursor")
		}
		since = v
	}
	var limit int
	if q := strings.TrimSpace(in.Limit); q != "" {
		v, err := strconv.Atoi(q)
		if err != nil {
			return nil, zip.ErrBadRequest("limit must be an integer")
		}
		limit = v
	}
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
	return &inboxPage{Cursor: cursor, Messages: msgs}, nil
}

// pairingQueue is the org's pending pairing requests.
type pairingQueue struct {
	// Pending is every unexpired pairing request waiting on an org admin, each
	// carrying the channel, the requesting sender and the code to approve it with.
	Pending []pairingView `json:"pending"`
}

// pairingList returns the pairing requests waiting for the caller org to approve
// — one per person who messaged a connected bot on a channel whose DM policy is
// "pairing" and who is not allowed yet. Each row carries the CODE an org admin
// passes to POST /v1/channels/pairing/approve. Expired requests are not
// returned. Codes are capability strings: they are shown here, and never logged.
func (o ops) pairingList(ctx context.Context, _ *noInput) (*pairingQueue, error) {
	s := o.s
	org, err := tenant(ctx)
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
	return &pairingQueue{Pending: out}, nil
}

// approvePairingIn names one pending pairing request. Both fields are BODY-only
// (`url:"-"`): zip's binder fills an In field from the query string too, and this
// route has never taken an approval there — without the opt-out
// `?code=` would approve a pairing the body never asked for, from a URL that
// lands in access logs.
type approvePairingIn struct {
	// Channel is the transport the request came in on: discord, slack, teams or telegram.
	Channel string `json:"channel" url:"-"`
	// Code is the pairing code from GET /v1/channels/pairing. It is a capability:
	// holding it is what authorises the approval, alongside org admin.
	Code string `json:"code" url:"-"`
}

// pairingApproved is the result of approving one pairing request.
type pairingApproved struct {
	// OwnerBootstrapped is true when this approval was the org's FIRST on the
	// channel and therefore also made the sender its owner.
	OwnerBootstrapped bool `json:"ownerBootstrapped"`
	// Sender is the external chat identity that is now allowed to DM the org's bot.
	Sender string `json:"sender"`
}

// pairingApprove turns one pending pairing code into a standing allow entry, so
// that person can DM the org's bot on that channel from now on. It requires ORG
// ADMIN, not merely membership. The first approval an org makes on a channel also
// bootstraps that sender as the channel's owner, which the answer reports. An
// unknown or expired code is a 404, and a code always belongs to exactly one
// org, so it can never approve someone into another tenant.
//
// Example: {"channel": "telegram", "code": "PAIR-7Q2M"}
func (o ops) pairingApprove(ctx context.Context, in *approvePairingIn) (*pairingApproved, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx); err != nil {
		return nil, zip.ErrForbidden("approving a pairing requires org admin")
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
	return &pairingApproved{OwnerBootstrapped: ownerBoot, Sender: sender}, nil
}

// allowlistRef names the channel whose access policy to read.
type allowlistRef struct {
	// Channel is the transport to read: discord, slack, teams or telegram.
	// Required; an unknown value is a 404.
	Channel string `json:"channel"`
}

// allowlistGet returns the caller org's access policy for one channel: whether
// DMs are pairing-gated, allowlisted or open, whether group rooms are open,
// allowlisted or disabled, the config-managed DM and group allow entries, the
// senders approved through PAIRING (read-only here), and the org's named access
// groups. An unknown channel is a 404.
//
// Example: {"channel": "slack"}
func (o ops) allowlistGet(ctx context.Context, in *allowlistRef) (*allowlistView, error) {
	s := o.s
	org, err := tenant(ctx)
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

// allowlistPutIn is a PARTIAL edit of one channel's access policy: every field
// but `channel` is applied only when the request provides it. An empty string
// leaves a policy alone; a NULL or ABSENT list leaves that list alone, while an
// EMPTY list ([]) clears it. That distinction is the whole contract of this
// route, so the list fields are plain slices rather than pointers — encoding/json
// gives a nil slice for both null and absent, and a non-nil empty slice for [],
// which is exactly the three-way answer the handler keys on.
//
// `url:"-"` on every field is what keeps this a BODY: zip's binder fills an In
// field from the query string as well as the body, and this route has never
// taken a policy there — without the opt-out `?dmPolicy=open` would open an
// org's DMs from a URL.
type allowlistPutIn struct {
	// Channel is the transport to edit: discord, slack, teams or telegram.
	// Required; an unknown value is a 404.
	Channel string `json:"channel" url:"-"`
	// DMPolicy sets how direct messages are admitted: "pairing" (a person must be
	// approved first), "allowlist" (only listed senders) or "open". Empty leaves
	// it unchanged.
	DMPolicy string `json:"dmPolicy" url:"-"`
	// GroupPolicy sets how group and thread rooms are admitted: "open",
	// "allowlist" or "disabled". Empty leaves it unchanged.
	GroupPolicy string `json:"groupPolicy" url:"-"`
	// DM REPLACES the config-managed DM allow entries. Absent or null leaves them
	// alone; an empty list clears them. It never touches senders approved through
	// pairing — a policy edit cannot revoke an approved pairing.
	DM []string `json:"dm" url:"-"`
	// Group REPLACES the config-managed group allow entries. Absent or null
	// leaves them alone; an empty list clears them.
	Group []string `json:"group" url:"-"`
	// AccessGroups REPLACES the org's named access groups, as
	// group name -> channel -> entries. Absent or null leaves them alone.
	AccessGroups map[string]map[string][]string `json:"accessGroups" url:"-"`
}

// allowlistPut edits the caller org's access policy for one channel and answers
// the policy as GET would, so both verbs return ONE shape. It requires ORG ADMIN.
// Every field but `channel` is optional and applied only when provided: an empty
// policy string leaves that policy alone, an absent or null list leaves that list
// alone, and an EMPTY list clears it. It writes only CONFIG-sourced allow entries
// — senders approved through pairing belong to the approval flow, so a policy
// edit can never revoke one. An unknown channel is a 404.
//
// Example: {"channel": "slack", "dmPolicy": "allowlist", "dm": ["U024BE7LH"]}
func (o ops) allowlistPut(ctx context.Context, in *allowlistPutIn) (*allowlistView, error) {
	s := o.s
	org, err := tenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx); err != nil {
		return nil, zip.ErrForbidden("editing the allowlist requires org admin")
	}
	body := in
	channel := strings.TrimSpace(body.Channel)
	if channel == "" {
		return nil, zip.ErrBadRequest("channel is required")
	}
	if _, ok := transportFor(channel); !ok {
		return nil, zip.ErrNotFound("unknown channel")
	}
	dm, group := DMPolicy(body.DMPolicy), GroupPolicy(body.GroupPolicy)
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
	if body.DM != nil {
		if err := putAllow(ctx, st, org, channel, "dm", body.DM, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "allowlist: %v", err)
		}
	}
	if body.Group != nil {
		if err := putAllow(ctx, st, org, channel, "group", body.Group, now); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "allowlist: %v", err)
		}
	}
	if body.AccessGroups != nil {
		if err := putAccessGroups(ctx, st, org, body.AccessGroups); err != nil {
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

// The prose for the one operation here that cannot be a typed op. The six ops
// above carry theirs in a doc comment zipdoc lifts; send is pinned untyped by
// two wire facts (routes says which), so there is no comment for anything to
// lift and the published document would carry an operationId and nothing else —
// an SDK method and a CLI command that cannot explain themselves. Declared
// through the same registry Register uses, so it renders only while the router
// actually serves the route.
func init() {
	openapi.Describe("/v1/channels/:channel/send", http.MethodPost,
		"Send a message from your org's bot to one chat room",
		"Delivers text, attachments and actions to one room on a connected chat transport — "+
			"discord, slack, teams or telegram — and answers that transport's own receipt, the "+
			"`messageId` it assigned and the Unix second it landed. An unknown channel is a 404.\n\n"+
			"The body is the envelope's NARROW outbound projection: `room`, `text`, `attachments`, "+
			"`actions`, `replyTo` and `idempotency`, and nothing else. Identity is not a field — the "+
			"channel is the path segment and the sender is the caller's validated org — so a body "+
			"carrying `sender`, `account` or `channel` is refused with 400 rather than having it "+
			"silently dropped. `room.id` is required, and so is something to say: text, or at least "+
			"one attachment.\n\n"+
			"Requires a validated principal; 403 without one. The room must already belong to the "+
			"caller's org — each transport verifies the binding itself, so a room this org has not "+
			"bound is 403 and a room whose route the bot has never learned is 409, meaning someone "+
			"has to message the bot there first. A transport that fails answers 502 carrying status "+
			"and shape only, never a token.\n\n"+
			"Sending is at-most-once only if you ask for it: pass an `idempotency` string and a "+
			"replay answers 200 with the PRIOR receipt instead of sending twice, while a send that "+
			"fails releases the key so the caller can re-attempt. Bodies over 1 MiB are refused. All "+
			"four transports currently render text only, so attachments and actions are flattened "+
			"deterministically to one line each after the text rather than dropped.")
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
