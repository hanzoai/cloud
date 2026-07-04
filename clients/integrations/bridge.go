package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/kms"
)

// bridge.go is the ONE ChatBridge core: the platform-agnostic @hanzo front-door
// shared by EVERY chat platform (Slack, Teams, Discord, Telegram). It owns
// everything provider-blind — the normalized inbound message, the bounded per-org
// agent-turn pool, the ONE agent brain (on-behalf-of RunOnBehalf), and the
// per-user account-link binding — so a new platform is a thin ADAPTER (three edges:
// inbound-auth, parse, reply), never a copy of the whole bridge.
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

// ── normalized inbound + reply seam ─────────────────────────────────────────

// Inbound is the normalized inbound chat event — ONE shape for every platform. An
// adapter produces it AFTER it has authenticated the request and parsed the
// payload. The core never sees a raw platform payload.
type Inbound struct {
	Provider   string // registry slug: "slack","teams","discord","telegram"
	ExternalID string // workspace/tenant/guild/chat id → OrgForExternalID (isolation root)
	User       string // platform-verified user id (billing/attribution subject via the link)
	Channel    string // reply target (channel/conversation/chat id)
	ThreadID   string // thread/message id to reply under (optional)
	Text       string // the user's prompt, mention stripped
	DedupeKey  string // event/update/interaction id ("" ⇒ non-dedupable)
}

// replyFunc delivers the agent's answer back to the platform in the same
// thread/chat. ephemeral = visible ONLY to the invoking user (link prompts carry a
// sensitive URL and MUST be ephemeral). The adapter constructs this closure per
// request, capturing whatever the platform's reply sink needs (bot token,
// response_url, interaction token, serviceURL) — so the core stays sink-blind.
type replyFunc func(ctx context.Context, text string, ephemeral bool) error

// ── bounded per-org agent-turn pool (shared across ALL platforms) ───────────

const (
	// bridgeAgentTimeout bounds one async agent turn end-to-end (org resolve + run
	// + platform post). Generous: a run executes a real model completion.
	bridgeAgentTimeout = 110 * time.Second
	// bridgeDefaultConcurrency caps simultaneous agent turns ACROSS ALL orgs AND
	// platforms so no insider can exhaust goroutines/FDs. Override BRIDGE_AGENT_CONCURRENCY.
	bridgeDefaultConcurrency = 32
	// bridgeDefaultOrgConcurrency caps simultaneous turns for a SINGLE org (a
	// fraction of the global pool) so one tenant cannot starve the others.
	// Override BRIDGE_AGENT_ORG_CONCURRENCY.
	bridgeDefaultOrgConcurrency = 8
	// bridgeMaxBody bounds a webhook body the adapters read + sign/verify over.
	bridgeMaxBody = 1 << 20 // 1 MiB
)

var (
	bridgeOnce sync.Once
	bridgeLim  *orgLimiter // the ONE bounded agent-turn pool: global cap + per-org sub-limit
	bridgeSeen *seenSet    // single-use link-state nonces (process-lifetime)
)

// bridgeReady lazily initializes the shared bridge process state (the bounded pool
// + the single-use link seen-set). Cheap + idempotent; every adapter handler calls
// it first. The durable dedupe table is created in the store's migrate() at Mount.
func bridgeReady() {
	bridgeOnce.Do(func() {
		bridgeLim = newOrgLimiter(bridgeAgentConcurrency(), bridgeOrgConcurrency())
		bridgeSeen = newSeenSet(time.Duration(linkStateTTLSec) * time.Second)
	})
}

// bridgeSpawn runs an ALREADY-SLOTTED agent turn in a recovered goroutine. Every
// adapter handler acquires the pool slot SYNCHRONOUSLY (bridgeLim.acquire) BEFORE
// recording the dedupe key, so a capacity SHED returns a retriable non-2xx without
// burning the event id (Red M-1); the slotted turn is then handed here. Two
// guarantees: (1) the slot is released on every exit, and (2) a panic anywhere in
// the turn (bridgeReply → agents.RunOnBehalf → the reply closure — a large surface
// over UNTRUSTED platform input) is CONTAINED. On the SHARED multi-tenant cloud
// binary this is non-negotiable: middleware.Recover() wraps only the sync request
// goroutine, so an unrecovered panic here would crash EVERY tenant and subsystem
// (Red M-2). The recover defer is registered LAST so it runs FIRST (LIFO); release
// still runs after it — a panicking turn frees its slot. This is the generalized
// twin of the shipped slackSpawn: ONE spawn+recover for every platform.
func (s *svc) bridgeSpawn(org string, run func()) {
	go func() {
		defer bridgeLim.release(org)
		defer s.bridgeRecover(org)
		run()
	}()
}

// bridgeRecover contains a panic in an async agent turn so it can never crash the
// shared cloud process. Called ONLY as a deferred func (recover must be a direct
// call in the deferred function).
func (s *svc) bridgeRecover(org string) {
	if r := recover(); r != nil {
		s.log.Error("bridge: agent turn panic (recovered)", "org", org, "err", r)
	}
}

// orgLimiter bounds concurrent agent turns two ways: a GLOBAL cap (total in-flight
// across all orgs/platforms) AND a PER-ORG cap (max in-flight for any single org).
// Data/token/billing isolation already holds via the resolved org; this adds the
// AVAILABILITY isolation that stops one tenant exhausting the shared worker pool.
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

// acquire takes one global + one per-org slot for org, non-blocking. Returns false
// (nothing acquired, no slot leaked) when the org is at its per-org cap OR the
// global pool is full — the per-org check precedes the global take, and the global
// take only bumps the per-org count on a successful send.
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

// runBridgeTurn is the async body every adapter dispatches: compute the reply for a
// PRE-RESOLVED org and deliver it via `reply`. org is the isolation root (resolved
// in the sync webhook path via OrgForExternalID on a verified payload).
func (s *svc) runBridgeTurn(org string, in Inbound, reply replyFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), bridgeAgentTimeout)
	defer cancel()
	text, ephemeral := s.bridgeReply(ctx, org, in.Provider, in.ExternalID, in.User, in.Text)
	if text == "" {
		return
	}
	if err := reply(ctx, text, ephemeral); err != nil {
		s.log.Warn("bridge: reply", "provider", in.Provider, "err", err)
	}
}

// bridgeReply is the ONE agent brain, shared by every platform's @mention/DM/slash
// path. It resolves the caller's linked Hanzo identity and either runs the agent ON
// BEHALF OF them IN-PROCESS (RunOnBehalf — no gateway hop) returning the model's
// answer, or, when unlinked, returns a short prompt carrying THAT platform's link
// URL. ephemeral reports whether the reply is the (sensitive) link prompt — the
// caller MUST deliver those to the user only. Every returned string is safe to
// post; internal errors are logged (never a token) and surfaced as a terse message.
func (s *svc) bridgeReply(ctx context.Context, org, provider, externalID, user, text string) (reply string, ephemeral bool) {
	link, linked, err := s.getUserLink(org, provider, user)
	if err != nil {
		s.log.Warn("bridge: user link lookup", "provider", provider, "org", org, "err", err)
		return "Sorry — I couldn't reach your Hanzo account just now. Please try again shortly.", false
	}
	if !linked {
		u, lerr := s.linkURL(provider, externalID, user)
		if lerr != nil {
			s.log.Error("bridge: link url", "provider", provider, "err", lerr)
			return "Connect your Hanzo account to use @hanzo.", true
		}
		return "Connect your Hanzo account to use @hanzo: " + u, true
	}
	// IN-PROCESS on-behalf-of run: org (isolation gate + tenant + balance) and the
	// linked user's Hanzo subject drive billing/attribution. No bearer, no gateway hop.
	run, rerr := agents.RunOnBehalf(ctx, org, link.Subject, bridgeAgentRef(provider), text)
	if rerr != nil {
		s.log.Warn("bridge: agent run", "provider", provider, "org", org, "err", rerr) // never logs a token
		return "Sorry — the agent hit an error handling that. Please try again.", false
	}
	if run.Status != "ok" {
		return "Sorry — the agent hit an error handling that. Please try again.", false
	}
	if strings.TrimSpace(run.Output) == "" {
		return "(the agent returned an empty response)", false
	}
	return run.Output, false
}

// linkURL builds the per-user "connect your Hanzo account" URL for a provider. Each
// platform owns its own link flow (its native identify leg + the shared hanzo.id
// OIDC leg), so the URL builder dispatches to the adapter. An unimplemented/absent
// flow returns an error → the brain shows a URL-less prompt (never a dead-end).
func (s *svc) linkURL(provider, externalID, user string) (string, error) {
	switch provider {
	case "slack":
		return s.slackLinkURL(externalID, user)
	case "discord":
		return s.discordLinkURL(externalID, user)
	case "telegram":
		return s.telegramLinkURL(externalID, user)
	case "teams":
		return s.teamsLinkURL(externalID, user)
	}
	return "", fmt.Errorf("bridge: no link flow for provider %q", provider)
}

// ── per-user Hanzo binding (KMS-custodied, per-org, per-provider) ────────────

const (
	userSecretPrefix = "user:"
	userSecretSuffix = ":refresh"
)

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
}

func (s *svc) putUserLink(org, provider, extUser string, link userLink) error {
	if !validOrg(org) {
		return fmt.Errorf("bridge: invalid org")
	}
	blob, err := json.Marshal(link)
	if err != nil {
		return err
	}
	return s.kmsPut(org, provider, userSecretName(extUser), blob)
}

// getUserLink returns the linked (org, provider, extUser) binding. found=false (nil
// error) when the user has not linked (no secret). Fails closed on invalid org /
// KMS-down.
func (s *svc) getUserLink(org, provider, extUser string) (userLink, bool, error) {
	if !validOrg(org) {
		return userLink{}, false, fmt.Errorf("bridge: invalid org")
	}
	if !s.kmsReady() {
		return userLink{}, false, kms.ErrMasterKeyMissing
	}
	raw, err := s.kmsGet(org, provider, userSecretName(extUser))
	if errors.Is(err, kms.ErrSecretNotFound) {
		return userLink{}, false, nil
	}
	if err != nil {
		return userLink{}, false, err
	}
	var link userLink
	if err := json.Unmarshal(raw, &link); err != nil {
		return userLink{}, false, fmt.Errorf("bridge: user link decode: %w", err)
	}
	if link.Subject == "" {
		return userLink{}, false, nil
	}
	return link, true, nil
}

// ── config (env, read at call time — operator-injected from KMS) ────────────

// bridgeAgentRef resolves the agent every @hanzo turn runs. Per-platform override
// {PROVIDER}_AGENT_REF (e.g. SLACK_AGENT_REF) → shared BRIDGE_AGENT_REF → "hanzo".
func bridgeAgentRef(provider string) string {
	if v := strings.TrimSpace(os.Getenv(strings.ToUpper(provider) + "_AGENT_REF")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("BRIDGE_AGENT_REF")); v != "" {
		return v
	}
	return "hanzo"
}

func bridgeAgentConcurrency() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("BRIDGE_AGENT_CONCURRENCY"))); err == nil && v > 0 {
		return v
	}
	return bridgeDefaultConcurrency
}

// bridgeOrgConcurrency caps how many agent turns a SINGLE org may run concurrently
// (availability isolation) — a fraction of the global pool. Override
// BRIDGE_AGENT_ORG_CONCURRENCY; clamped to the global cap in newOrgLimiter.
func bridgeOrgConcurrency() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("BRIDGE_AGENT_ORG_CONCURRENCY"))); err == nil && v > 0 {
		return v
	}
	return bridgeDefaultOrgConcurrency
}
