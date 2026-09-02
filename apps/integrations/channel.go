package integrations

import (
	"github.com/hanzoai/cloud/internal/environ"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/kms"
	"github.com/hanzoai/cloud/plane"
	agentsplane "github.com/hanzoai/cloud/plane/agents"
	channelsplane "github.com/hanzoai/cloud/plane/channels"
	iamplane "github.com/hanzoai/cloud/plane/iam"
)

// channel.go is the ONE ChatBridge core: the platform-agnostic @hanzo entry point
// shared by EVERY chat platform. It owns everything provider-blind — the
// normalized inbound message, the bounded per-org pool, the ONE agent brain
// (on-behalf-of RunOnBehalf), and the per-user account-link binding — so a new
// platform is a thin ADAPTER (three edges: inbound-auth, parse, reply), never a
// copy of the whole bridge.
//
// Each adapter's webhook does, in order: (1) authenticate at the platform's OWN
// trust boundary (Slack HMAC / Teams Bot Framework JWT / Discord Ed25519 /
// Telegram secret-token — deliberately NOT all one scheme); (2) parse the payload
// into an Inbound; (3) hand it to the core, which dedupes, resolves the org
// (OrgForExternalID — the ISOLATION ROOT), runs the agent under the bounded pool,
// and delivers the answer via the adapter's `reply` closure.
//
// ISOLATION BAR (identical for every platform): a workspace's events reach ONLY
// the org that connected that workspace. The org comes ONLY from
// OrgForExternalID(provider, externalID) on a signature-VERIFIED payload — never a
// client-supplied field — the run is THAT org's agent on behalf of THAT org's
// linked user, billed against THAT org's ledger.

// ── normalized inbound + reply client ─────────────────────────────────────────

// Inbound is the normalized inbound chat event — ONE shape for every platform. An
// adapter produces it AFTER it has authenticated the request and parsed the
// payload. The core never sees a raw platform payload.
type Inbound struct {
	Provider   string // registry slug: "slack","teams","discord","telegram","whatsapp"
	ExternalID string // workspace/tenant/guild/chat id → OrgForExternalID (isolation root)
	User       string // platform-verified user id (billing/attribution subject via the link)
	Channel    string // reply target (channel/conversation/chat id)
	ThreadID   string // thread/message id to reply under (optional)
	Text       string // the user's prompt, mention stripped
	DedupeKey  string // event/update/interaction id ("" ⇒ non-dedupable)
}

// ── the bounded per-org pool (shared across ALL platforms) ──────────────────

const (
	// channelAgentTimeout bounds one async agent turn end-to-end (org resolve + run
	// + platform post). Generous: a run executes a real model completion.
	channelAgentTimeout = 110 * time.Second
	// channelDefaultConcurrency caps simultaneous agent turns ACROSS ALL orgs AND
	// platforms so no insider can exhaust goroutines/FDs. Override BRIDGE_AGENT_CONCURRENCY.
	channelDefaultConcurrency = 32
	// channelDefaultOrgConcurrency caps simultaneous turns for a SINGLE org (a
	// fraction of the global pool) so one tenant cannot starve the others.
	// Override BRIDGE_AGENT_ORG_CONCURRENCY.
	channelDefaultOrgConcurrency = 8
	// channelMaxBody bounds a webhook body the adapters read + sign/verify over.
	channelMaxBody = 1 << 20 // 1 MiB
)

var (
	channelOnce sync.Once
	channelLim  *orgLimiter // the ONE bounded per-org pool for the work an event costs this process
	channelSeen *seenSet    // single-use link-state nonces (process-lifetime)
)

// channelReady lazily initializes the shared channel process state (the bounded pool
// + the single-use link seen-set). Cheap + idempotent; every adapter handler calls
// it first. The durable dedupe table is created in the store's migrate() at Mount.
func channelReady() {
	channelOnce.Do(func() {
		channelLim = newOrgLimiter(channelAgentConcurrency(), channelOrgConcurrency())
		channelSeen = newSeenSet(time.Duration(linkStateTTLSec) * time.Second)
	})
}

// channelSpawn runs an ALREADY-SLOTTED agent turn in a recovered goroutine. The
// caller acquires the slot SYNCHRONOUSLY (channelLim.acquire) and hands the
// slotted turn here, which is the ONE pairing left in this file: every other
// holder of this pool is emitIngress, which acquires and releases inside itself.
// Two guarantees: (1) the slot is released on every exit, and (2) a panic
// anywhere in the turn (channelReply → agents.RunOnBehalf → the reply closure —
// a large surface over UNTRUSTED platform input) is CONTAINED. On the SHARED
// multi-tenant cloud binary this is non-negotiable: middleware.Recover() wraps
// only the sync request goroutine, so an unrecovered panic here would crash
// EVERY tenant and subsystem. The recover defer is registered LAST so it runs
// FIRST (LIFO); release still runs after it — a panicking turn frees its slot.
//
// Slack is the caller: its slash command runs its turn in THIS process, and its
// events path spends a slot on the thinking indicator it posts while channels
// answers. The other four adapters run no turn here — they take an event and
// emit it — so they hold no slot of their own.
func channelSpawn(s *cloud.Service[state], org string, run func()) {
	go func() {
		defer channelLim.release(org)
		defer channelRecover(s, org)
		run()
	}()
}

// channelRecover contains a panic in an async agent turn so it can never crash the
// shared cloud process. Called ONLY as a deferred func (recover must be a direct
// call in the deferred function).
func channelRecover(s *cloud.Service[state], org string) {
	if r := recover(); r != nil {
		s.Log.Error("channel: agent turn panic (recovered)", "org", org, "err", r)
	}
}

// ── the bounded per-org pool (the ONE limiter this process binds on) ────────

// orgLimiter bounds concurrent per-org work two ways: a GLOBAL cap (total
// in-flight across all orgs) AND a PER-ORG cap (max in-flight for any single
// org). Data / token / billing isolation already holds via the resolved org;
// this adds the AVAILABILITY isolation that stops one tenant exhausting the
// shared worker pool. It lives here (provider-agnostic): channelLim (the shared
// chat pool) above, the Slack coding pool (codingLim), and every caller bound
// against the SAME type.
type orgLimiter struct {
	mu       sync.Mutex
	inflight map[string]int
	perOrg   int
	global   chan struct{}
}

func newOrgLimiter(global, perOrg int) *orgLimiter {
	if global < 1 {
		global = 1
	}
	if perOrg < 1 {
		perOrg = 1
	}
	if perOrg > global {
		perOrg = global
	}
	return &orgLimiter{inflight: make(map[string]int), perOrg: perOrg, global: make(chan struct{}, global)}
}

// acquire takes one global + one per-org slot for org, non-blocking. It returns
// false (nothing acquired, no slot leaked) when the org is at its per-org cap OR
// the global pool is full — the per-org check precedes the global take, and the
// global take only bumps the per-org count on a successful send.
func (l *orgLimiter) acquire(org string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[org] >= l.perOrg {
		return false
	}
	select {
	case l.global <- struct{}{}:
		l.inflight[org]++
		return true
	default:
		return false
	}
}

// release returns the org's slot and the global slot. Called exactly once per
// successful acquire.
func (l *orgLimiter) release(org string) {
	l.mu.Lock()
	if n := l.inflight[org]; n > 1 {
		l.inflight[org] = n - 1
	} else {
		delete(l.inflight, org)
	}
	l.mu.Unlock()
	<-l.global
}

// ── the ONE agent brain (shared by every platform + its @mention/DM/slash) ──

// channelReply is the ONE agent brain, shared by every platform's @mention/DM/slash
// path. It resolves the caller's linked Hanzo identity and either runs the agent ON
// BEHALF OF them IN-PROCESS (RunOnBehalf — no gateway hop) returning the model's
// answer, or, when unlinked, returns a short prompt carrying THAT platform's link
// URL. ephemeral reports whether the reply is the (sensitive) link prompt — the
// caller MUST deliver those to the user only. Every returned string is safe to
// post; internal errors are logged (never a token) and surfaced as a terse message.
// channelRunContext is the context ONE agent turn runs on: detached from any
// request, carrying the tenant it bills, bounded by the turn budget.
//
// context.Background() is not an accident and is not merely about cancellation.
// zip reads a STATED caller only when NO request sits behind the context
// (caller.go:352-356) — deliberately, so that stating an identity can never
// override an authenticated one. Hand cloud.For the inbound webhook's ctx and the
// org is silently dropped: Slack's POST is a request, it carries no Hanzo identity,
// and CallerOf reads those empty headers instead. Downstream then answers
// `authorize: no org on the call`, which is precisely how this failed in
// production. Detaching is what makes the statement readable at all.
//
// It is not a laundering hole: For supplies a tenant where there is none and cannot
// override one, and this org came from OrgForExternalID on a signature-VERIFIED
// payload — never a client-supplied field.
//
// Detaching is independently required: the turn runs in channelSpawn's goroutine
// while the webhook handler has already answered Slack 200, so on the request ctx
// the model call would be cancelled the instant we reply.
func channelRunContext(org string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(cloud.For(context.Background(), org), channelAgentTimeout)
}

// It takes NO context on purpose. The turn it dispatches must run on a detached,
// tenant-stated one (see the call below), and a ctx parameter here is an invitation
// to pass the webhook's — which both cancels the run when we answer Slack and
// silently discards the org. Removing the parameter is what makes that unavailable
// rather than merely discouraged.
// It returns the RUN's id when a run happened, so the turn above can record which
// run answered this conversation. The id was already in hand and was thrown away
// everywhere except one failure log, which is why a thread and the run behind it
// had no value in common.
//
// It takes the whole Inbound rather than six of its fields. It used to take the
// six, and the two it left behind are why the assistant could not follow a
// thread: the THREAD was one of them, so a turn in a channel asked for the
// history of the ROOM and every thread in that channel read as one conversation.
// A normalized event exists so the core can pass it around whole.
func channelReply(s *cloud.Service[state], org string, in Inbound) (reply string, ephemeral bool, runID string) {
	provider, text := in.Provider, in.Text
	link, say, ephemeral := channelIdentity(s, org, provider, in.ExternalID, in.User)
	if say != "" {
		// No run happened — an unlinked user gets a prompt, not an agent turn — so
		// there is no id to name, and saying so with "" is the honest answer.
		return say, ephemeral, ""
	}
	// On-behalf-of run over the PLANE (ZAP/UDS): org is the isolation gate, tenant
	// and balance; the linked user's Hanzo subject drives attribution. No bearer,
	// no gateway hop, no public network.
	//
	// plane.Ask and NOT agents.RunOnBehalf, which reads that package's `mounted`
	// global. A plugin is a PROCESS: the global is nil unless agents happens to be
	// in THIS binary, so the direct call made co-residency an undeclared
	// requirement and answered ErrNoPeer for every deployment that separates them —
	// which is every real one. It failed the same way for every chat channel, so the
	// call belongs on the plane where the boundary is explicit.
	// Model is the person's OWN choice from the App Home tab, empty when they have
	// not chosen — the answering side then uses the deployment default. Carried
	// per turn rather than baked into an agent row, because it is a preference of
	// the PERSON asking and not a property of the agent.
	// STATE THE TENANT, on a context with NO REQUEST BEHIND IT. Both halves matter
	// and both are load-bearing.
	//
	// A run bills, and the balance gate is a plane call to commerce, which takes the
	// org from the CALLER's identity and never from an argument (balance_rpc.go:36)
	// so that no caller can name the books it charges. The org therefore has to ride
	// the caller. cloud.For states it — but zip reads a STATED caller only where
	// there is no request (caller.go:352-356), deliberately, so that stating an
	// identity can never override an authenticated one. Applied to the inbound
	// webhook's ctx it is a silent no-op: Slack's POST is a request, it carries no
	// Hanzo identity, and CallerOf reads those empty headers instead. That is
	// exactly how this failed in production with `authorize: no org on the call`
	// AFTER the tenant was supposedly stated one hop later.
	//
	// context.Background() is what makes the statement readable, and it is the same
	// pairing every other background caller uses (commerce/risk.go:190,
	// x402/peer.go:71). It is not a laundering hole: For cannot override an
	// authenticated caller, only supply one where none exists, and this org was
	// resolved from the Slack-verified team_id through the install→org map — never
	// from a payload field.
	//
	// Detaching is independently REQUIRED anyway: the turn runs in channelSpawn's
	// goroutine, and the webhook handler returns 200 to Slack immediately. On the
	// request ctx the model call would be cancelled the moment we answer Slack.
	runCtx, cancel := channelRunContext(org)
	defer cancel()
	// THE CONVERSATION, not just the newest line of it. channels has been recording
	// every inbound turn since ingest existed and nothing read them back, so the
	// agent saw one message with nothing around it: asked "weather in Benicia", then
	// "try again", it answered "what would you like me to help you with" — and by the
	// third turn it was looking "Benicia" up as an org, because a bare noun with no
	// conversation around it looks like a lookup rather than a place.
	//
	// A failed read is NOT a failed turn. An unreachable inbox costs context, which
	// makes a worse answer; refusing to answer at all makes none. So this degrades to
	// the single message it used to send, which is the behaviour it replaces.
	history := priorTurns(s, runCtx, org, in)
	out, rerr := agentsplane.AgentsRunOnBehalf(runCtx,
		&plane.RunOnBehalfIn{Org: org, Subject: link.Subject, Ref: agentRefFor(runCtx, provider, in.Channel),
			Input: text, Model: link.Model, History: history})
	run := plane.RunOnBehalfOut{}
	if out != nil {
		run = *out
	}
	if rerr != nil {
		s.Log.Warn("channel: agent run", "provider", provider, "org", org, "err", rerr) // never logs a token
		return "Sorry — the agent hit an error handling that. Please try again.", false, run.RunID
	}
	if run.Status != "ok" {
		// SAID, not just returned. A run that EXECUTED and whose model failed comes
		// back as a non-"ok" status with a nil error (agents.RunOnBehalf's contract),
		// so this branch — not the one above — is the one a broken inference path
		// lands in. It logged nothing, and the whole failure was therefore invisible:
		// the op answered 200, the channel posted its generic sentence, and the only
		// trace of the cause was the run row. That is how a dead model wire survived
		// a day of looking. The run id is here so the row is findable.
		s.Log.Warn("channel: agent run did not succeed", "provider", provider, "org", org,
			"status", run.Status, "run_id", run.RunID, "err", run.Error)
		return "Sorry — the agent hit an error handling that. Please try again.", false, run.RunID
	}
	if strings.TrimSpace(run.Output) == "" {
		return "(the agent returned an empty response)", false, run.RunID
	}
	return run.Output, false, run.RunID
}

// channelIdentity resolves the caller's linked Hanzo account, or the sentence to
// say INSTEAD of running anything. link is meaningful only when say is empty;
// ephemeral marks the (sensitive) link prompt, which carries a URL that must
// reach only the person who asked.
//
// It is its own function because two things now run on behalf of a linked user —
// the agent turn above and a registry command (slack_command.go) — and the
// prompt is the one sentence that must not be written twice: a second copy is a
// second chance to forget the ephemeral bit.
func channelIdentity(s *cloud.Service[state], org, provider, externalID, user string) (link userLink, say string, ephemeral bool) {
	link, linked, err := getUserLink(s, org, provider, user)
	if err != nil {
		s.Log.Warn("channel: user link lookup", "provider", provider, "org", org, "err", err)
		return userLink{}, "Sorry — I couldn't reach your Hanzo account just now. Please try again shortly.", false
	}
	if linked {
		return link, "", false
	}
	// A GitHub commenter who signed in to Hanzo through GitHub is already known:
	// IAM holds their GitHub user id, and the webhook carries the same id. Ask
	// the org's identity store once, then keep the answer as an ordinary link so
	// the next comment costs a KMS read and no plane hop.
	if provider == "github" {
		if sub := federatedSubject(s, org, provider, user); sub != "" {
			link = userLink{Subject: sub}
			if err := putUserLink(s, org, provider, user, link); err != nil {
				s.Log.Warn("channel: cache federated link", "provider", provider, "org", org, "err", err)
			}
			return link, "", false
		}
	}
	// An issue tracker has no sign-in leg of its own, so a turn there runs as the
	// organization's default subject — the person who bound the account — when
	// the commenter is not otherwise known. Written by the claim, read here, and
	// absent on the chat transports, whose users link themselves.
	if link, linked, err = getUserLink(s, org, provider, defaultSubjectKey); err == nil && linked {
		return link, "", false
	}
	u, lerr := linkURL(s, provider, externalID, user)
	if lerr != nil {
		s.Log.Error("channel: link url", "provider", provider, "err", lerr)
		return userLink{}, "Connect your Hanzo account to use @hanzo.", true
	}
	return userLink{}, "Connect your Hanzo account to use @hanzo: " + u, true
}

// federatedSubject asks IAM which member of org signed in through provider with
// this subject. "" is the honest answer for none, an unreachable store, or a
// caller with no org — the turn then falls to the org default or the link prompt.
func federatedSubject(s *cloud.Service[state], org, provider, subject string) string {
	ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), 5*time.Second)
	defer cancel()
	out, err := iamplane.IAMFederated(ctx,
		&plane.FederatedIn{Provider: provider, Subject: subject})
	if err != nil {
		s.Log.Warn("channel: federated lookup", "provider", provider, "org", org, "err", err)
		return ""
	}
	if out == nil {
		return ""
	}
	return strings.TrimSpace(out.User)
}

// linkURL builds the per-user "connect your Hanzo account" URL for a provider. Each
// platform owns its own link flow (its native identify leg + the shared hanzo.id
// OIDC leg), so the URL builder dispatches to the adapter. An unimplemented/absent
// flow returns an error → the brain shows a URL-less prompt (never a dead-end).
func linkURL(s *cloud.Service[state], provider, externalID, user string) (string, error) {
	switch provider {
	case "slack":
		return slackLinkURL(s, externalID, user)
	case "discord":
		return discordLinkURL(s, externalID, user)
	case "telegram":
		return telegramLinkURL(s, externalID, user)
	case "teams":
		return teamsLinkURL(s, externalID, user)
	}
	return "", fmt.Errorf("channel: no link flow for provider %q", provider)
}

// ── per-user Hanzo binding (KMS-custodied, per-org, per-provider) ────────────

const (
	userSecretPrefix = "user:"
	userSecretSuffix = ":refresh"
)

// defaultSubjectKey is the platform-user slot that holds an organization's
// DEFAULT answering subject for a provider. It is not a user id — no platform
// issues "*" — so it can never collide with a real link.
const defaultSubjectKey = "*"

// userSecretName is the KMS secret name for a linked platform user's binding —
// "user:<extUser>:refresh" under the org's integrations/<provider> namespace.
// Platform user ids are opaque tokens with no '/', NUL, or control chars, so this
// is a valid KMS segment.
func userSecretName(extUser string) string {
	return userSecretPrefix + extUser + userSecretSuffix
}

// userLink is the on-behalf-of binding for a linked platform user: the Hanzo
// account subject (what RunOnBehalf attributes the spend to), the account org, and
// the Hanzo refresh token (sealed for future token minting; the in-process run
// needs only the subject). Sealed KMS-encrypted; never a DB column, never logged.
type userLink struct {
	Subject string `json:"subject"`
	Org     string `json:"org"`
	Refresh string `json:"refresh"`
	// Model is what the person chose on the App Home tab. OMITEMPTY with a working
	// default, so a link written before the Home tab existed decodes fine and
	// behaves exactly as it did — a preference that breaks an existing link is not
	// a preference, it is an outage.
	Model string `json:"model,omitempty"`
}

func putUserLink(s *cloud.Service[state], org, provider, extUser string, link userLink) error {
	if !validOrg(org) {
		return fmt.Errorf("channel: invalid org")
	}
	blob, err := json.Marshal(link)
	if err != nil {
		return err
	}
	return kmsPut(s, kmsPath(org, provider), userSecretName(extUser), blob)
}

// getUserLink returns the linked (org, provider, extUser) binding. found=false (nil
// error) when the user has not linked (no secret). Fails closed on invalid org /
// KMS-down.
func getUserLink(s *cloud.Service[state], org, provider, extUser string) (userLink, bool, error) {
	if !validOrg(org) {
		return userLink{}, false, fmt.Errorf("channel: invalid org")
	}
	if !kmsReady(s) {
		return userLink{}, false, kms.ErrMasterKeyMissing
	}
	raw, err := kmsGet(s, kmsPath(org, provider), userSecretName(extUser))
	if errors.Is(err, kms.ErrSecretNotFound) {
		return userLink{}, false, nil
	}
	if err != nil {
		return userLink{}, false, err
	}
	var link userLink
	if err := json.Unmarshal(raw, &link); err != nil {
		return userLink{}, false, fmt.Errorf("channel: user link decode: %w", err)
	}
	if link.Subject == "" {
		return userLink{}, false, nil
	}
	return link, true, nil
}

// ── config (env, read at call time — operator-injected from KMS) ────────────

// agentRefFor asks channels which agent answers this room — the org's binding
// for the room, else its default for the transport, else the built-in. One row
// decides it for a slash command and for a mention alike. An unreachable
// channels answers the built-in rather than refusing the turn: a binding is a
// preference, and a person asked a question.
func agentRefFor(ctx context.Context, provider, room string) string {
	out, err := channelsplane.ChannelsAgent(ctx,
		&plane.AgentForIn{Channel: provider, Room: room})
	if err != nil || out == nil || strings.TrimSpace(out.Ref) == "" {
		return "hanzo"
	}
	return out.Ref
}

func channelAgentConcurrency() int {
	if v, err := strconv.Atoi(environ.Or("BRIDGE_AGENT_CONCURRENCY", "")); err == nil && v > 0 {
		return v
	}
	return channelDefaultConcurrency
}

// channelOrgConcurrency caps how many agent turns a SINGLE org may run concurrently
// (availability isolation) — a fraction of the global pool. Override
// BRIDGE_AGENT_ORG_CONCURRENCY; clamped to the global cap in newOrgLimiter.
func channelOrgConcurrency() int {
	if v, err := strconv.Atoi(environ.Or("BRIDGE_AGENT_ORG_CONCURRENCY", "")); err == nil && v > 0 {
		return v
	}
	return channelDefaultOrgConcurrency
}

// priorTurns is the conversation this message arrived in, oldest first.
//
// THE PLATFORM IS ASKED FIRST, where it can answer. Slack holds the real
// transcript — including the assistant's OWN replies, which the inbox
// structurally cannot: ingest records what ARRIVES, and an answer leaves. An
// agent shown only the questions reads its own words as the user's. Slack also
// keeps a thread apart from the room around it, which the inbox does not, and it
// is what the person is looking at, so it is the conversation by definition.
//
// The inbox answers for the transports there is no read-back for, and for a
// Slack workspace whose install predates the history scopes. A failed read is
// never a failed turn: less context makes a worse answer, refusing makes none.
func priorTurns(s *cloud.Service[state], ctx context.Context, org string, in Inbound) []plane.Turn {
	if in.Provider == "slack" {
		turns, err := slackTurns(ctx, org, in)
		if err == nil {
			return turns
		}
		s.Log.Warn("channel: slack transcript", "org", org, "err", err)
	}
	return recentTurns(ctx, in.Provider, in.Channel)
}

// recentTurns asks channels for the conversation this message arrived in.
//
// The room is the transport's own id, which only the adapter knows, and the org
// rides runCtx — the same context the run itself bills on, so the history and the
// charge cannot disagree about whose conversation this is.
//
// The newest turn is dropped: it IS this Input, already recorded by ingest before
// the turn ran, and sending it twice would have the agent answer a question it
// appears to have been asked a moment ago.
func recentTurns(ctx context.Context, provider, room string) []plane.Turn {
	if room == "" {
		return nil
	}
	out, err := channelsplane.ChannelsRecent(ctx,
		&plane.RecentIn{Channel: provider, Room: room})
	if err != nil || out == nil || len(out.Turns) == 0 {
		return nil
	}
	return out.Turns[:len(out.Turns)-1]
}
