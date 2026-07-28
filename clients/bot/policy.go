// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The gate an invocation passes before it may reach a node.
//
// A node runs on someone's machine, so "allowed" here means "allowed to run on
// hardware we do not own". Everything in this file is a pure function of its
// arguments: no registry, no socket, no wall clock, no config file. The clock
// and the approval store are injected, so the whole policy is testable by
// value.
//
// Two layers, in the order an invocation meets them:
//
//  1. Check — may this command reach this node at all? Platform defaults,
//     the operator's allow/deny overlay, and what the node itself declared.
//  2. SanitizeSystemRun — may this *particular* command line run? Approval
//     records bind an operator's "yes" to one argv, one cwd, one env, one
//     device and one node; anything else is a different question that has not
//     been answered.
//
// Denials are values, not exceptions: a Decision carries a stable Code for the
// wire and a Reason for a human.

package bot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ---------------------------------------------------------------------------
// Decision
// ---------------------------------------------------------------------------

// Decision is the answer this package gives. The zero value denies: a policy
// that has not run cannot have said yes.
type Decision struct {
	Allow bool
	// Code is stable and safe to put on the wire. Callers switch on it.
	Code string
	// Reason is for a human reading a log or an error body.
	Reason string
}

// Decision codes. The approval codes match the TypeScript gateway's
// details.code values so an existing node/CLI keeps recognizing them.
const (
	CodeNoSession              = "NO_SESSION"
	CodeCommandRequired        = "COMMAND_REQUIRED"
	CodeCommandNotAllowlisted  = "COMMAND_NOT_ALLOWLISTED"
	CodeCommandsNotDeclared    = "COMMANDS_NOT_DECLARED"
	CodeCommandNotDeclared     = "COMMAND_NOT_DECLARED"
	CodeExecApprovalsForbidden = "EXEC_APPROVALS_FORBIDDEN"
	CodeAuthModeForbids        = "AUTH_MODE_FORBIDS_COMMAND"
	CodeArgvRequired           = "ARGV_REQUIRED"

	CodeMissingCommand     = "MISSING_COMMAND"
	CodeRawCommandMismatch = "RAW_COMMAND_MISMATCH"
	CodeMissingRunID       = "MISSING_RUN_ID"
	CodeApprovalsUnavail   = "APPROVALS_UNAVAILABLE"
	CodeUnknownApprovalID  = "UNKNOWN_APPROVAL_ID"
	CodeApprovalExpired    = "APPROVAL_EXPIRED"
	CodeMissingNodeID      = "MISSING_NODE_ID"
	CodeNodeBindingMissing = "APPROVAL_NODE_BINDING_MISSING"
	CodeNodeMismatch       = "APPROVAL_NODE_MISMATCH"
	CodeDeviceMismatch     = "APPROVAL_DEVICE_MISMATCH"
	CodeClientMismatch     = "APPROVAL_CLIENT_MISMATCH"
	CodeRequestMismatch    = "APPROVAL_REQUEST_MISMATCH"
	CodeEnvBindingMissing  = "APPROVAL_ENV_BINDING_MISSING"
	CodeEnvMismatch        = "APPROVAL_ENV_MISMATCH"
	CodeApprovalRequired   = "APPROVAL_REQUIRED"
)

func allowed() Decision                 { return Decision{Allow: true} }
func deny(code, reason string) Decision { return Decision{Code: code, Reason: reason} }

// ---------------------------------------------------------------------------
// The command vocabulary
// ---------------------------------------------------------------------------

// Commands the gateway itself knows by name.
const (
	CommandSystemRun        = "system.run"
	CommandSystemRunPrepare = "system.run.prepare"
	CommandSystemWhich      = "system.which"
	CommandSystemNotify     = "system.notify"
	CommandBrowserProxy     = "browser.proxy"
)

var (
	canvasCommands = []string{
		"canvas.present", "canvas.hide", "canvas.navigate", "canvas.eval",
		"canvas.snapshot", "canvas.a2ui.push", "canvas.a2ui.pushJSONL", "canvas.a2ui.reset",
	}
	cameraCommands        = []string{"camera.list"}
	cameraDangerous       = []string{"camera.snap", "camera.clip"}
	screenDangerous       = []string{"screen.record"}
	locationCommands      = []string{"location.get"}
	androidNotifCommands  = []string{"notifications.list", "notifications.actions"}
	deviceCommands        = []string{"device.info", "device.status"}
	androidDeviceCommands = []string{"device.info", "device.status", "device.permissions", "device.health"}
	contactsCommands      = []string{"contacts.search"}
	contactsDangerous     = []string{"contacts.add"}
	calendarCommands      = []string{"calendar.events"}
	calendarDangerous     = []string{"calendar.add"}
	remindersCommands     = []string{"reminders.list"}
	remindersDangerous    = []string{"reminders.add"}
	photosCommands        = []string{"photos.latest"}
	motionCommands        = []string{"motion.activity", "motion.pedometer"}
	smsDangerous          = []string{"sms.send"}

	// systemRunCommands are the host-exec surface: they run a program on the
	// user's machine. Everything else reads or shows something.
	systemRunCommands = []string{CommandSystemRunPrepare, CommandSystemRun, CommandSystemWhich}

	systemCommands = concat(systemRunCommands, []string{CommandSystemNotify, CommandBrowserProxy})

	// iOS nodes do not implement system.run/which, but they do notify.
	iosSystemCommands = []string{CommandSystemNotify}

	// unknownPlatformCommands is the fail-safe: metadata we could not classify
	// never receives host-exec defaults.
	unknownPlatformCommands = concat(canvasCommands, cameraCommands, locationCommands, []string{CommandSystemNotify})

	// dangerousCommands are high risk: they capture the user's surroundings or
	// act on their behalf. They are in NO platform default. An operator turns
	// one on by naming it in Mode.Allow, and Mode.Deny still overrides.
	dangerousCommands = concat(cameraDangerous, screenDangerous, contactsDangerous,
		calendarDangerous, remindersDangerous, smsDangerous)

	// execApprovalsCommands must never travel over an invoke. They edit the
	// node's own approval policy, which is the thing approvals protect; the
	// dedicated exec.approvals.node.* path exists for that.
	execApprovalsCommands = []string{"system.execApprovals.get", "system.execApprovals.set"}
)

// Platform is a node's classified operating system. It is derived from
// self-reported metadata, so it is a hint the policy hardens against, never a
// credential.
type Platform string

const (
	PlatformIOS     Platform = "ios"
	PlatformAndroid Platform = "android"
	PlatformMacOS   Platform = "macos"
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
	PlatformUnknown Platform = "unknown"
)

// platformDefaults is what each platform may be asked to do before an operator
// changes anything.
var platformDefaults = map[Platform][]string{
	PlatformIOS: concat(canvasCommands, cameraCommands, locationCommands, deviceCommands,
		contactsCommands, calendarCommands, remindersCommands, photosCommands, motionCommands,
		iosSystemCommands),
	PlatformAndroid: concat(canvasCommands, cameraCommands, locationCommands, androidNotifCommands,
		[]string{CommandSystemNotify}, androidDeviceCommands, contactsCommands, calendarCommands,
		remindersCommands, photosCommands, motionCommands),
	PlatformMacOS: concat(canvasCommands, cameraCommands, locationCommands, deviceCommands,
		contactsCommands, calendarCommands, remindersCommands, photosCommands, motionCommands,
		systemCommands),
	PlatformLinux:   systemCommands,
	PlatformWindows: systemCommands,
	PlatformUnknown: unknownPlatformCommands,
}

// DangerousCommands lists the high-risk commands, off by default everywhere.
func DangerousCommands() []string { return slices.Clone(dangerousCommands) }

// IsDangerous reports whether a command is high risk.
func IsDangerous(command string) bool {
	return slices.Contains(dangerousCommands, strings.TrimSpace(command))
}

// IsHostExec reports whether a command runs a program on the node's machine.
func IsHostExec(command string) bool {
	return slices.Contains(systemRunCommands, strings.TrimSpace(command))
}

// PlatformCommands returns a platform's defaults, sorted.
func PlatformCommands(p Platform) []string {
	base, ok := platformDefaults[p]
	if !ok {
		base = platformDefaults[PlatformUnknown]
	}
	out := slices.Clone(base)
	slices.Sort(out)
	return slices.Compact(out)
}

// ---------------------------------------------------------------------------
// Platform classification
// ---------------------------------------------------------------------------

var platformPrefixes = []struct {
	id       Platform
	prefixes []string
}{
	{PlatformIOS, []string{"ios"}},
	{PlatformAndroid, []string{"android"}},
	{PlatformMacOS, []string{"mac", "darwin"}},
	{PlatformWindows, []string{"win"}},
	{PlatformLinux, []string{"linux"}},
}

var deviceFamilyTokens = []struct {
	id     Platform
	tokens []string
}{
	{PlatformIOS, []string{"iphone", "ipad", "ios"}},
	{PlatformAndroid, []string{"android"}},
	{PlatformMacOS, []string{"mac"}},
	{PlatformWindows, []string{"windows"}},
	{PlatformLinux, []string{"linux"}},
}

// ClassifyPlatform resolves self-reported metadata to a platform.
//
// Metadata is normalized (NFKD, marks stripped, lowercased) before matching so
// a confusable spelling cannot dodge classification: "ĺinux" must land on
// linux, not on the unknown bucket, because unknown grants canvas and camera
// that linux does not.
//
// Session carries no device family, so Check classifies from Platform alone. A
// transport that has family metadata should resolve it here before registering
// the session.
func ClassifyPlatform(platform, deviceFamily string) Platform {
	raw := normalizeMetadata(platform)
	for _, rule := range platformPrefixes {
		for _, prefix := range rule.prefixes {
			if strings.HasPrefix(raw, prefix) {
				return rule.id
			}
		}
	}
	family := normalizeMetadata(deviceFamily)
	for _, rule := range deviceFamilyTokens {
		for _, token := range rule.tokens {
			if strings.Contains(family, token) {
				return rule.id
			}
		}
	}
	return PlatformUnknown
}

// normalizeMetadata collapses Unicode confusables to a stable ASCII-ish token.
func normalizeMetadata(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	decomposed := norm.NFKD.String(trimmed)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.M, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// ---------------------------------------------------------------------------
// Mode: the deployment posture a check runs under
// ---------------------------------------------------------------------------

// AuthMode is how the edge authenticates a caller.
type AuthMode string

const (
	// AuthUnset is the zero value and is treated as the strictest reading.
	AuthUnset        AuthMode = ""
	AuthIAM          AuthMode = "iam"
	AuthToken        AuthMode = "token"
	AuthTrustedProxy AuthMode = "trusted-proxy"
	// AuthNone means the edge authenticates nobody.
	AuthNone AuthMode = "none"
)

// Mode is the operator's policy for this deployment: the auth posture plus the
// explicit overlay on the platform defaults. Its zero value is the safe one —
// no extra commands, unknown auth treated as strict.
type Mode struct {
	Auth AuthMode
	// Allow adds commands on top of the platform defaults. This is the only
	// way a dangerous command becomes reachable.
	Allow []string
	// Deny removes commands, and wins over Allow and over the defaults.
	Deny []string
}

// Allowlist resolves the commands reachable on a platform under this mode,
// sorted. Deny is applied last, so a denied command is denied however it got in.
func Allowlist(p Platform, mode Mode) []string {
	set := allowlistSet(p, mode)
	out := make([]string, 0, len(set))
	for cmd := range set {
		out = append(out, cmd)
	}
	slices.Sort(out)
	return out
}

func allowlistSet(p Platform, mode Mode) map[string]struct{} {
	base, ok := platformDefaults[p]
	if !ok {
		base = platformDefaults[PlatformUnknown]
	}
	set := make(map[string]struct{}, len(base)+len(mode.Allow))
	for _, cmd := range base {
		if cmd = strings.TrimSpace(cmd); cmd != "" {
			set[cmd] = struct{}{}
		}
	}
	for _, cmd := range mode.Allow {
		if cmd = strings.TrimSpace(cmd); cmd != "" {
			set[cmd] = struct{}{}
		}
	}
	for _, cmd := range mode.Deny {
		if cmd = strings.TrimSpace(cmd); cmd != "" {
			delete(set, cmd)
		}
	}
	return set
}

// ---------------------------------------------------------------------------
// Check
// ---------------------------------------------------------------------------

// Check decides whether one command may reach one node.
//
// args is the invocation's argv, and is consulted only for the host-exec
// commands where the argv IS the command being run; for everything else it is
// ignored. A caller that does not pass it for system.run gets a denial, which
// is the failure direction we want.
//
// Session.Permissions is deliberately not consulted: it is the node's report of
// its own OS grants, useful to show a human, and not something the node's own
// operator can be prevented from lying about. The gate is the allowlist, the
// declared commands, and the approval record.
func Check(s *Session, command string, args []string, mode Mode) Decision {
	if s == nil {
		return deny(CodeNoSession, "no connected node")
	}
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return deny(CodeCommandRequired, "command required")
	}
	if slices.Contains(execApprovalsCommands, cmd) {
		return deny(CodeExecApprovalsForbidden,
			"invoke does not allow system.execApprovals.*; use exec.approvals.node.*")
	}

	platform := ClassifyPlatform(s.Platform, "")
	if _, ok := allowlistSet(platform, mode)[cmd]; !ok {
		return deny(CodeCommandNotAllowlisted, "command not allowlisted")
	}

	// A node that declared nothing gets nothing. The empty declaration is not
	// "no opinion", it is "I do not implement commands".
	if len(s.Commands) == 0 {
		return deny(CodeCommandsNotDeclared, "node did not declare commands")
	}
	if !slices.Contains(s.Commands, cmd) {
		return deny(CodeCommandNotDeclared, "command not declared by node")
	}

	// Stricter than the TypeScript gateway, deliberately: that gateway was one
	// operator's own machine, where "no auth" still meant "me". Here it means
	// no caller identity at all, and an anonymous caller must not reach a
	// user's shell or camera.
	if mode.Auth == AuthNone && (IsHostExec(cmd) || IsDangerous(cmd)) {
		return deny(CodeAuthModeForbids, "gateway auth is disabled; this command is unreachable")
	}

	// Also stricter: a run with nothing to run is malformed, and forwarding it
	// only moves the failure onto the node.
	if cmd == CommandSystemRun && (len(args) == 0 || strings.TrimSpace(args[0]) == "") {
		return deny(CodeArgvRequired, "system.run requires a non-empty command argv")
	}

	return allowed()
}

// ---------------------------------------------------------------------------
// Auth mode policy
// ---------------------------------------------------------------------------

// ErrAmbiguousAuthMode is returned when two shared secrets are configured and
// nothing says which one authenticates.
var ErrAmbiguousAuthMode = errors.New(
	"invalid config: gateway.auth.token and gateway.auth.password are both configured, " +
		"but gateway.auth.mode is unset; set gateway.auth.mode to token or password")

// RequiresTokenForInstall reports whether the node install flow must present a
// gateway token. Only the two modes that explicitly move authentication
// elsewhere are exempt; an unset or unrecognized mode requires the token.
func RequiresTokenForInstall(mode AuthMode) bool {
	switch mode {
	case AuthNone, AuthTrustedProxy:
		return false
	default:
		return true
	}
}

// CheckAuthMode refuses a configuration that does not say how callers
// authenticate.
//
// The TypeScript this ports had drifted to always returning "not ambiguous",
// on the reasoning that a token is the only shared secret left; its own tests
// still assert the opposite. Taking the safer reading: two configured secrets
// with no declared mode is a config whose author has not decided, and guessing
// on their behalf picks a credential nobody meant to be live.
func CheckAuthMode(mode AuthMode, hasToken, hasPassword bool) error {
	if strings.TrimSpace(string(mode)) != "" {
		return nil
	}
	if hasToken && hasPassword {
		return ErrAmbiguousAuthMode
	}
	return nil
}

// ---------------------------------------------------------------------------
// Auth rate limiting
// ---------------------------------------------------------------------------

// Rate-limit scopes keep credential classes on independent budgets, so failing
// device-token auth cannot lock out shared-secret auth.
const (
	RateLimitScopeDefault      = "default"
	RateLimitScopeSharedSecret = "shared-secret"
	RateLimitScopeDeviceToken  = "device-token"
	RateLimitScopeHookAuth     = "hook-auth"
)

const (
	defaultMaxAttempts = 10
	defaultWindow      = time.Minute
	defaultLockout     = 5 * time.Minute
)

// RateLimitConfig configures a RateLimiter. The zero value is the default
// policy: 10 attempts a minute, five minute lockout, loopback exempt.
type RateLimitConfig struct {
	MaxAttempts int
	Window      time.Duration
	Lockout     time.Duration
	// RateLimitLoopback turns off the loopback exemption. It is phrased
	// negatively so the zero value keeps a local CLI from locking itself out.
	RateLimitLoopback bool
	// Now is the injected clock. nil means time.Now.
	Now func() time.Time
}

// RateLimitResult answers "may this client try again".
type RateLimitResult struct {
	Allow      bool
	Remaining  int
	RetryAfter time.Duration
}

type rateEntry struct {
	attempts    []time.Time
	lockedUntil time.Time
}

// RateLimiter is a sliding-window limiter for failed authentication attempts,
// keyed by (scope, client ip).
//
// The org is deliberately NOT part of that key. Everywhere else in this package
// the org is part of an identity; here the key is a budget, and a budget
// partitioned by something the caller names is no budget at all — an attacker
// would multiply their attempts by inventing org names.
type RateLimiter struct {
	mu           sync.Mutex
	max          int
	window       time.Duration
	lockout      time.Duration
	rateLoopback bool
	now          func() time.Time
	entries      map[string]*rateEntry
}

// NewRateLimiter builds a limiter. It owns no goroutine and no timer: call
// Prune from whatever the process already schedules.
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	l := &RateLimiter{
		max:          cfg.MaxAttempts,
		window:       cfg.Window,
		lockout:      cfg.Lockout,
		rateLoopback: cfg.RateLimitLoopback,
		now:          cfg.Now,
		entries:      make(map[string]*rateEntry),
	}
	if l.max <= 0 {
		l.max = defaultMaxAttempts
	}
	if l.window <= 0 {
		l.window = defaultWindow
	}
	if l.lockout <= 0 {
		l.lockout = defaultLockout
	}
	if l.now == nil {
		l.now = time.Now
	}
	return l
}

// Check reports whether ip may attempt authentication in scope.
func (l *RateLimiter) Check(ip, scope string) RateLimitResult {
	key, canonical := l.key(ip, scope)
	if l.exempt(canonical) {
		return RateLimitResult{Allow: true, Remaining: l.max}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	entry := l.entries[key]
	if entry == nil {
		return RateLimitResult{Allow: true, Remaining: l.max}
	}
	if !entry.lockedUntil.IsZero() {
		if now.Before(entry.lockedUntil) {
			return RateLimitResult{Remaining: 0, RetryAfter: entry.lockedUntil.Sub(now)}
		}
		entry.lockedUntil = time.Time{}
		entry.attempts = nil
	}
	entry.slide(now, l.window)
	remaining := max(0, l.max-len(entry.attempts))
	return RateLimitResult{Allow: remaining > 0, Remaining: remaining}
}

// RecordFailure counts one failed attempt.
func (l *RateLimiter) RecordFailure(ip, scope string) {
	key, canonical := l.key(ip, scope)
	if l.exempt(canonical) {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	entry := l.entries[key]
	if entry == nil {
		entry = &rateEntry{}
		l.entries[key] = entry
	}
	if !entry.lockedUntil.IsZero() && now.Before(entry.lockedUntil) {
		return
	}
	entry.slide(now, l.window)
	entry.attempts = append(entry.attempts, now)
	if len(entry.attempts) >= l.max {
		entry.lockedUntil = now.Add(l.lockout)
	}
}

// Reset clears one (scope, ip) budget, e.g. after a successful login.
func (l *RateLimiter) Reset(ip, scope string) {
	key, _ := l.key(ip, scope)
	l.mu.Lock()
	delete(l.entries, key)
	l.mu.Unlock()
}

// Size is the number of tracked budgets.
func (l *RateLimiter) Size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// Prune drops budgets that no longer hold anything. A locked-out entry is kept
// until its lockout expires, or pruning would release the lock.
func (l *RateLimiter) Prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, entry := range l.entries {
		if !entry.lockedUntil.IsZero() && now.Before(entry.lockedUntil) {
			continue
		}
		entry.slide(now, l.window)
		if len(entry.attempts) == 0 {
			delete(l.entries, key)
		}
	}
}

func (e *rateEntry) slide(now time.Time, window time.Duration) {
	cutoff := now.Add(-window)
	kept := e.attempts[:0]
	for _, ts := range e.attempts {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	e.attempts = kept
}

func (l *RateLimiter) key(ip, scope string) (key, canonical string) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = RateLimitScopeDefault
	}
	canonical = CanonicalClientIP(ip)
	return scope + ":" + canonical, canonical
}

func (l *RateLimiter) exempt(canonical string) bool {
	if l.rateLoopback {
		return false
	}
	addr, err := netip.ParseAddr(canonical)
	return err == nil && addr.IsLoopback()
}

// CanonicalClientIP reduces a client address to one representation, so that
// 1.2.3.4 and ::ffff:1.2.3.4 spend the same budget. Anything unparseable
// becomes "unknown" and shares one budget, which is the conservative choice.
func CanonicalClientIP(ip string) string {
	raw := strings.TrimSpace(ip)
	if raw == "" {
		return "unknown"
	}
	if addr, err := netip.ParseAddr(strings.Trim(raw, "[]")); err == nil {
		return addr.Unmap().String()
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
			return addr.Unmap().String()
		}
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// system.run: the forwarded parameters
// ---------------------------------------------------------------------------

// ApprovalDecision is what an operator answered.
type ApprovalDecision string

const (
	DecisionAllowOnce   ApprovalDecision = "allow-once"
	DecisionAllowAlways ApprovalDecision = "allow-always"
)

// ApprovalHostNode is the only host an approval may bind to for a node invoke.
const ApprovalHostNode = "node"

// Scopes that let a caller speak for the approval system itself.
const (
	ScopeOperatorAdmin     = "operator.admin"
	ScopeOperatorApprovals = "operator.approvals"
)

// SystemRunParams is exactly the set of fields a node's system.run handler
// understands.
//
// The type is the allowlist. The TypeScript had to copy a hand-written list of
// keys out of an untyped object to stop internal control fields being smuggled
// through; here a field the node does not implement has nowhere to live.
type SystemRunParams struct {
	Command              []string          `json:"command,omitempty"`
	RawCommand           string            `json:"rawCommand,omitempty"`
	Cwd                  string            `json:"cwd,omitempty"`
	Env                  map[string]string `json:"env,omitempty"`
	TimeoutMs            int               `json:"timeoutMs,omitempty"`
	NeedsScreenRecording bool              `json:"needsScreenRecording,omitempty"`
	AgentID              string            `json:"agentId,omitempty"`
	SessionKey           string            `json:"sessionKey,omitempty"`
	RunID                string            `json:"runId,omitempty"`

	// Approved and ApprovalDecision are control fields. A caller may ask for
	// them; only a matching approval record puts them on the wire.
	Approved         bool             `json:"approved,omitempty"`
	ApprovalDecision ApprovalDecision `json:"approvalDecision,omitempty"`
}

// forwardable strips the control fields. Whatever a caller claimed about
// approval is re-derived from the record, never carried over.
func (p SystemRunParams) forwardable() SystemRunParams {
	next := p
	next.Approved = false
	next.ApprovalDecision = ""
	return next
}

// Caller is who asked for the invocation.
type Caller struct {
	ConnID   string
	DeviceID string
	Scopes   []string
}

func (c Caller) mayApprove() bool {
	return slices.Contains(c.Scopes, ScopeOperatorAdmin) ||
		slices.Contains(c.Scopes, ScopeOperatorApprovals)
}

// ApprovalPlan is the command the gateway resolved when it asked the operator.
// When a record carries one, it — not the caller's parameters — is what runs.
type ApprovalPlan struct {
	Argv       []string
	Cwd        string
	RawCommand string
	AgentID    string
	SessionKey string
}

func (p *ApprovalPlan) normalized() *ApprovalPlan {
	if p == nil || len(p.Argv) == 0 {
		return nil
	}
	return &ApprovalPlan{
		Argv:       slices.Clone(p.Argv),
		Cwd:        strings.TrimSpace(p.Cwd),
		RawCommand: strings.TrimSpace(p.RawCommand),
		AgentID:    strings.TrimSpace(p.AgentID),
		SessionKey: strings.TrimSpace(p.SessionKey),
	}
}

// ApprovalBinding is what the operator actually said yes to.
type ApprovalBinding struct {
	Argv       []string
	Cwd        string
	AgentID    string
	SessionKey string
	EnvHash    string
}

// ApprovalRecord is the gateway's record of one approval request.
//
// Node is a NodeKey, not a bare id: an approval is an answer about one tenant's
// machine. Binding by id alone would let an approval for org A's "laptop"
// authorize org B's "laptop".
type ApprovalRecord struct {
	RunID   string
	Node    NodeKey
	Host    string
	Binding *ApprovalBinding
	Plan    *ApprovalPlan

	ExpiresAt time.Time

	RequestedByConnID   string
	RequestedByDeviceID string

	// Decision is empty once consumed or when the request timed out.
	Decision ApprovalDecision
	// Resolved records that the request reached an end state.
	Resolved bool
	// ResolvedBy is empty when nobody answered, i.e. the request timed out.
	ResolvedBy string
}

// timedOut is the state the TypeScript expressed as
// resolvedAtMs !== undefined && decision === undefined && resolvedBy === null.
func (r *ApprovalRecord) timedOut() bool {
	return r != nil && r.Resolved && r.Decision == "" && r.ResolvedBy == ""
}

// ApprovalStore is the gateway's live approval state. It is an interface so
// this file stays pure: the only mutation policy needs is consuming a
// one-shot approval, and that has to be atomic with the check.
type ApprovalStore interface {
	// Snapshot returns the record for runID, or nil if unknown or expired.
	Snapshot(runID string) *ApprovalRecord
	// ConsumeAllowOnce spends a one-shot approval, returning false if it was
	// already spent. Replay protection lives here because it must be atomic.
	ConsumeAllowOnce(runID string) bool
}

// ---------------------------------------------------------------------------
// SanitizeSystemRun
// ---------------------------------------------------------------------------

// SanitizeSystemRun decides what a system.run invocation may forward.
//
// It gates the approval control fields behind a real approval record, so a
// caller holding only write access cannot approve their own command by setting
// approved=true. On denial the returned params are zero: there is nothing safe
// to forward.
func SanitizeSystemRun(key NodeKey, in SystemRunParams, caller Caller, store ApprovalStore, now time.Time) (SystemRunParams, Decision) {
	next := in.forwardable()

	approved := in.Approved
	requested := normalizeDecision(in.ApprovalDecision)
	if !approved && requested == "" {
		// No override claimed: the only question is whether the command line
		// is coherent.
		if _, _, d := ResolveSystemRunCommand(in.Command, in.RawCommand); !d.Allow {
			return SystemRunParams{}, d
		}
		return next, allowed()
	}

	runID := strings.TrimSpace(in.RunID)
	if runID == "" {
		// Trusted internal pre-approval: a client that holds the approval
		// scopes (the embedded runner with ask=off) is the approval authority,
		// so it may pre-approve without a record. Every other caller — browser,
		// CLI, anything with plain write access — cannot.
		if approved && caller.mayApprove() {
			next.Approved = true
			return next, allowed()
		}
		return SystemRunParams{}, deny(CodeMissingRunID, "approval override requires params.runId")
	}

	if store == nil {
		return SystemRunParams{}, deny(CodeApprovalsUnavail, "exec approvals unavailable")
	}
	snap := store.Snapshot(runID)
	if snap == nil {
		return SystemRunParams{}, deny(CodeUnknownApprovalID, "unknown or expired approval id")
	}
	if now.After(snap.ExpiresAt) {
		return SystemRunParams{}, deny(CodeApprovalExpired, "approval expired")
	}

	if strings.TrimSpace(key.Org) == "" || strings.TrimSpace(key.NodeID) == "" {
		return SystemRunParams{}, deny(CodeMissingNodeID, "invoke requires an org and a node id")
	}
	if strings.TrimSpace(snap.Node.Org) == "" || strings.TrimSpace(snap.Node.NodeID) == "" {
		return SystemRunParams{}, deny(CodeNodeBindingMissing, "approval id missing node binding")
	}
	// One code for "different node" and "another tenant's node": telling them
	// apart would confirm that some other org has a node by that name.
	if snap.Node != key {
		return SystemRunParams{}, deny(CodeNodeMismatch, "approval id not valid for this node")
	}

	// Bind by device where we have one: it survives reconnects, while a conn id
	// does not. Fall back to the connection only when there is no device.
	if snap.RequestedByDeviceID != "" {
		if snap.RequestedByDeviceID != caller.DeviceID {
			return SystemRunParams{}, deny(CodeDeviceMismatch, "approval id not valid for this device")
		}
	} else if snap.RequestedByConnID != "" && snap.RequestedByConnID != caller.ConnID {
		return SystemRunParams{}, deny(CodeClientMismatch, "approval id not valid for this client")
	}

	rt, d := resolveRuntimeContext(snap.Plan, in)
	if !d.Allow {
		return SystemRunParams{}, d
	}
	if rt.plan != nil {
		// The plan is authoritative. A caller who edits the command after the
		// operator answered gets the command the operator saw, not theirs.
		next.Command = slices.Clone(rt.plan.Argv)
		next.RawCommand = rt.rawCommand
		next.Cwd = rt.cwd
		next.AgentID = rt.agentID
		next.SessionKey = rt.sessionKey
	}

	if d := EvaluateApprovalMatch(rt.argv, snap, rt.cwd, rt.agentID, rt.sessionKey, in.Env); !d.Allow {
		return SystemRunParams{}, d
	}

	switch snap.Decision {
	case DecisionAllowOnce:
		if !store.ConsumeAllowOnce(runID) {
			return SystemRunParams{}, approvalRequired()
		}
		next.Approved = true
		next.ApprovalDecision = DecisionAllowOnce
		return next, allowed()
	case DecisionAllowAlways:
		next.Approved = true
		next.ApprovalDecision = DecisionAllowAlways
		return next, allowed()
	}

	// Nobody answered in time. An askFallback "allow-once" is honored only for
	// a client that is itself allowed to decide approvals.
	if snap.timedOut() && approved && requested == DecisionAllowOnce && caller.mayApprove() {
		next.Approved = true
		next.ApprovalDecision = DecisionAllowOnce
		return next, allowed()
	}

	return SystemRunParams{}, approvalRequired()
}

func approvalRequired() Decision { return deny(CodeApprovalRequired, "approval required") }

func normalizeDecision(d ApprovalDecision) ApprovalDecision {
	switch ApprovalDecision(strings.TrimSpace(string(d))) {
	case DecisionAllowOnce:
		return DecisionAllowOnce
	case DecisionAllowAlways:
		return DecisionAllowAlways
	default:
		return ""
	}
}

type runtimeContext struct {
	plan       *ApprovalPlan
	argv       []string
	cwd        string
	agentID    string
	sessionKey string
	rawCommand string
}

// resolveRuntimeContext prefers the approved plan and falls back to the
// caller's parameters only when the record carries no plan.
func resolveRuntimeContext(plan *ApprovalPlan, in SystemRunParams) (runtimeContext, Decision) {
	if p := plan.normalized(); p != nil {
		return runtimeContext{
			plan:       p,
			argv:       slices.Clone(p.Argv),
			cwd:        p.Cwd,
			agentID:    p.AgentID,
			sessionKey: p.SessionKey,
			rawCommand: p.RawCommand,
		}, allowed()
	}
	argv, _, d := ResolveSystemRunCommand(in.Command, in.RawCommand)
	if !d.Allow {
		return runtimeContext{}, d
	}
	return runtimeContext{
		argv:       argv,
		cwd:        strings.TrimSpace(in.Cwd),
		agentID:    strings.TrimSpace(in.AgentID),
		sessionKey: strings.TrimSpace(in.SessionKey),
		rawCommand: strings.TrimSpace(in.RawCommand),
	}, allowed()
}

// ---------------------------------------------------------------------------
// Approval binding and matching
// ---------------------------------------------------------------------------

// BuildApprovalBinding derives what an approval binds to. The returned keys are
// the env variable names, safe to show a human in an approval prompt; the
// values are only ever hashed.
func BuildApprovalBinding(argv []string, cwd, agentID, sessionKey string, env map[string]string) (ApprovalBinding, []string) {
	hash, keys := hashEnv(env)
	return ApprovalBinding{
		Argv:       slices.Clone(argv),
		Cwd:        strings.TrimSpace(cwd),
		AgentID:    strings.TrimSpace(agentID),
		SessionKey: strings.TrimSpace(sessionKey),
		EnvHash:    hash,
	}, keys
}

// hashEnv reduces env overrides to one value.
//
// Every key is bound, verbatim, including names no shell would call portable.
// The TypeScript filtered a key through a portability check first and dropped
// whatever failed it, which meant an override the gateway forwards anyway —
// `{"BASH_ENV=x": "y"}` — was invisible to the binding and rode along on an
// approval that never covered it. A key we cannot reason about has to widen the
// binding, not vanish from it: unrecognized must mean "different question",
// never "no question".
//
// Keys are sorted bytewise rather than by locale collation, and the digest is
// over Go's JSON encoding: the hash is only ever compared against another hash
// produced here, so what matters is that it is stable and order-independent,
// not that it matches any other runtime's spelling.
func hashEnv(env map[string]string) (string, []string) {
	if len(env) == 0 {
		return "", nil
	}
	pairs := make([][]string, 0, len(env))
	for k, v := range env {
		pairs = append(pairs, []string{k, v})
	}
	slices.SortFunc(pairs, func(a, b []string) int { return strings.Compare(a[0], b[0]) })
	keys := make([]string, 0, len(pairs))
	for _, p := range pairs {
		keys = append(keys, p[0])
	}
	raw, _ := json.Marshal(pairs)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), keys
}

// EvaluateApprovalMatch reports whether a record answers the question actually
// being asked: same argv, same cwd, same agent, same session, same env.
//
// A record with no binding never matches. An approval that predates binding is
// an approval to an unknown question.
func EvaluateApprovalMatch(argv []string, rec *ApprovalRecord, cwd, agentID, sessionKey string, env map[string]string) Decision {
	if rec == nil || rec.Host != ApprovalHostNode {
		return requestMismatch()
	}
	actual, _ := BuildApprovalBinding(argv, cwd, agentID, sessionKey, env)
	if rec.Binding == nil {
		return requestMismatch()
	}
	return matchBinding(*rec.Binding, actual)
}

func requestMismatch() Decision {
	return deny(CodeRequestMismatch, "approval id does not match request")
}

func matchBinding(expected, actual ApprovalBinding) Decision {
	if len(expected.Argv) == 0 || !slices.Equal(expected.Argv, actual.Argv) {
		return requestMismatch()
	}
	if expected.Cwd != actual.Cwd ||
		expected.AgentID != actual.AgentID ||
		expected.SessionKey != actual.SessionKey {
		return requestMismatch()
	}
	return matchEnvHash(expected.EnvHash, actual.EnvHash)
}

func matchEnvHash(expected, actual string) Decision {
	switch {
	case expected == "" && actual == "":
		return allowed()
	case expected == "":
		// The operator approved a command with no env overrides and the caller
		// added some. GIT_EXTERNAL_DIFF turns "git diff" into arbitrary exec.
		return deny(CodeEnvBindingMissing, "approval id missing env binding for requested env overrides")
	case expected != actual:
		return deny(CodeEnvMismatch, "approval id env binding mismatch")
	default:
		return allowed()
	}
}

// ---------------------------------------------------------------------------
// Command text: what the operator read must be what the machine runs
// ---------------------------------------------------------------------------

// ResolveSystemRunCommand validates a command line and returns the argv to bind
// against, plus the text a human would be shown.
//
// rawCommand is display text, and display text that disagrees with the argv is
// the whole attack: an operator approves `echo`, the machine runs
// `cmd.exe /d /s /c echo SAFE&&whoami`.
func ResolveSystemRunCommand(command []string, rawCommand string) (argv []string, cmdText string, d Decision) {
	raw := strings.TrimSpace(rawCommand)
	if len(command) == 0 {
		if raw != "" {
			return nil, "", deny(CodeMissingCommand, "rawCommand requires params.command")
		}
		return nil, "", allowed()
	}
	text, d := validateCommandConsistency(command, raw)
	if !d.Allow {
		return nil, "", d
	}
	return command, text, allowed()
}

func validateCommandConsistency(argv []string, raw string) (string, Decision) {
	isWrapper, shellCommand := extractShellWrapperCommand(argv, "")
	positionalAfterInline := hasTrailingPositionalArgvAfterInlineCommand(argv)
	envManipulation := isWrapper && hasEnvManipulationBeforeShellWrapper(argv)

	// When the argv manipulates the environment before reaching the shell, or
	// carries positional arguments after the inline command, the inline text
	// alone is a lie: $0/$1 and BASH_ENV change what actually runs. Bind the
	// display text to the whole argv instead.
	mustBindFullArgv := envManipulation || positionalAfterInline

	inferred := formatExecCommand(argv)
	if shellCommand != "" && !mustBindFullArgv {
		inferred = strings.TrimSpace(shellCommand)
	}
	if raw != "" && raw != inferred {
		return "", deny(CodeRawCommandMismatch, "rawCommand does not match command")
	}
	if raw != "" {
		return raw, allowed()
	}
	return inferred, allowed()
}

// FormatExecCommand renders an argv the way an approval prompt shows it.
func FormatExecCommand(argv []string) string { return formatExecCommand(argv) }

func formatExecCommand(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		if arg == "" {
			parts = append(parts, `""`)
			continue
		}
		if !strings.ContainsFunc(arg, func(r rune) bool { return unicode.IsSpace(r) || r == '"' }) {
			parts = append(parts, arg)
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(arg, `"`, `\"`)+`"`)
	}
	return strings.Join(parts, " ")
}

// ---------------------------------------------------------------------------
// Shell wrapper resolution
//
// Finding the command inside `sh -lc "..."`, `cmd.exe /c ...`, `busybox sh -c`,
// `env A=B bash -lc ...` and so on. Anything not recognized here binds to the
// full argv, which is the safe direction: less is hidden from the operator.
// ---------------------------------------------------------------------------

const maxDispatchWrapperDepth = 4

var (
	posixShellWrappers  = []string{"ash", "bash", "dash", "fish", "ksh", "sh", "zsh"}
	cmdShellWrappers    = []string{"cmd"}
	powerShellWrappers  = []string{"powershell", "pwsh"}
	shellMultiplexers   = []string{"busybox", "toybox"}
	transparentWrappers = []string{"nice", "nohup", "stdbuf", "timeout"}

	posixInlineFlags      = []string{"-lc", "-c", "--command"}
	powerShellInlineFlags = []string{"-c", "-command", "--command"}

	// Wrappers whose inline command may be followed by positional arguments,
	// which become $0/$1 inside the script.
	inlinePositionalWrappers = []string{"ash", "bash", "dash", "fish", "ksh", "powershell", "pwsh", "sh", "zsh"}

	envOptionsWithValue     = []string{"-u", "--unset", "-c", "--chdir", "-s", "--split-string", "--default-signal", "--ignore-signal", "--block-signal"}
	envInlineValuePrefixes  = []string{"-u", "-c", "-s", "--unset=", "--chdir=", "--split-string=", "--default-signal=", "--ignore-signal=", "--block-signal="}
	envFlagOptions          = []string{"-i", "--ignore-environment", "-0", "--null"}
	niceOptionsWithValue    = []string{"-n", "--adjustment", "--priority"}
	stdbufOptionsWithValue  = []string{"-i", "--input", "-o", "--output", "-e", "--error"}
	timeoutFlagOptions      = []string{"--foreground", "--preserve-status", "-v", "--verbose"}
	timeoutOptionsWithValue = []string{"-k", "--kill-after", "-s", "--signal"}
)

type shellKind int

const (
	shellNone shellKind = iota
	shellPosix
	shellCmd
	shellPowerShell
)

func shellWrapperKind(base string) shellKind {
	switch {
	case slices.Contains(posixShellWrappers, base):
		return shellPosix
	case slices.Contains(cmdShellWrappers, base):
		return shellCmd
	case slices.Contains(powerShellWrappers, base):
		return shellPowerShell
	default:
		return shellNone
	}
}

// normalizeExecutableToken reduces "/usr/bin/Bash" and "C:\\Windows\\CMD.EXE"
// to "bash" and "cmd".
func normalizeExecutableToken(token string) string {
	base := baseNameLower(token)
	return strings.TrimSuffix(base, ".exe")
}

func baseNameLower(token string) string {
	win := baseName(stripDriveLetter(token), `/\`)
	posix := baseName(token, "/")
	base := posix
	if len(win) < len(posix) {
		base = win
	}
	return strings.ToLower(strings.TrimSpace(base))
}

func stripDriveLetter(s string) string {
	if len(s) >= 2 && s[1] == ':' {
		c := s[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return s[2:]
		}
	}
	return s
}

func baseName(s, seps string) string {
	end := len(s)
	for end > 0 && strings.IndexByte(seps, s[end-1]) >= 0 {
		end--
	}
	start := end
	for start > 0 && strings.IndexByte(seps, s[start-1]) < 0 {
		start--
	}
	return s[start:end]
}

type unwrapKind int

const (
	unwrapNotWrapper unwrapKind = iota
	unwrapBlocked
	unwrapOK
)

type unwrapResult struct {
	kind    unwrapKind
	wrapper string
	argv    []string
}

// unwrapDispatchWrapper peels one layer of `env`, `nice`, `nohup`, `stdbuf` or
// `timeout`. The privilege and session wrappers (sudo, doas, setsid, ...) are
// blocked rather than peeled: what runs under them is not the same question.
func unwrapDispatchWrapper(argv []string) unwrapResult {
	if len(argv) == 0 {
		return unwrapResult{kind: unwrapNotWrapper}
	}
	token0 := strings.TrimSpace(argv[0])
	if token0 == "" {
		return unwrapResult{kind: unwrapNotWrapper}
	}
	wrapper := normalizeExecutableToken(token0)
	switch wrapper {
	case "env":
		return dispatchResult(wrapper, unwrapEnvInvocation(argv))
	case "nice":
		return dispatchResult(wrapper, unwrapNiceInvocation(argv))
	case "nohup":
		return dispatchResult(wrapper, unwrapNohupInvocation(argv))
	case "stdbuf":
		return dispatchResult(wrapper, unwrapStdbufInvocation(argv))
	case "timeout":
		return dispatchResult(wrapper, unwrapTimeoutInvocation(argv))
	case "chrt", "doas", "ionice", "setsid", "sudo", "taskset":
		return unwrapResult{kind: unwrapBlocked, wrapper: wrapper}
	default:
		return unwrapResult{kind: unwrapNotWrapper}
	}
}

func dispatchResult(wrapper string, argv []string) unwrapResult {
	if argv == nil {
		return unwrapResult{kind: unwrapBlocked, wrapper: wrapper}
	}
	return unwrapResult{kind: unwrapOK, wrapper: wrapper, argv: argv}
}

// unwrapShellMultiplexer peels `busybox sh -c ...`. A multiplexer invoking
// anything other than a shell is blocked.
func unwrapShellMultiplexer(argv []string) unwrapResult {
	if len(argv) == 0 {
		return unwrapResult{kind: unwrapNotWrapper}
	}
	token0 := strings.TrimSpace(argv[0])
	if token0 == "" {
		return unwrapResult{kind: unwrapNotWrapper}
	}
	wrapper := normalizeExecutableToken(token0)
	if !slices.Contains(shellMultiplexers, wrapper) {
		return unwrapResult{kind: unwrapNotWrapper}
	}
	idx := 1
	if idx < len(argv) && strings.TrimSpace(argv[idx]) == "--" {
		idx++
	}
	if idx >= len(argv) {
		return unwrapResult{kind: unwrapBlocked, wrapper: wrapper}
	}
	applet := strings.TrimSpace(argv[idx])
	if applet == "" || shellWrapperKind(normalizeExecutableToken(applet)) == shellNone {
		return unwrapResult{kind: unwrapBlocked, wrapper: wrapper}
	}
	return unwrapResult{kind: unwrapOK, wrapper: wrapper, argv: argv[idx:]}
}

type scanDirective int

const (
	scanContinue scanDirective = iota
	scanConsumeNext
	scanStop
	scanInvalid
)

// scanWrapperInvocation walks a wrapper's own options and returns the argv of
// the command it wraps, or nil when the invocation is not one we can read.
func scanWrapperInvocation(
	argv []string,
	seps []string,
	onToken func(token, lower string) scanDirective,
	adjust func(commandIndex int, argv []string) int,
) []string {
	idx := 1
	expectsValue := false
scan:
	for idx < len(argv) {
		token := strings.TrimSpace(argv[idx])
		if token == "" {
			idx++
			continue
		}
		if expectsValue {
			expectsValue = false
			idx++
			continue
		}
		if slices.Contains(seps, token) {
			idx++
			break
		}
		switch onToken(token, strings.ToLower(token)) {
		case scanStop:
			break scan
		case scanInvalid:
			return nil
		case scanConsumeNext:
			expectsValue = true
		}
		idx++
	}
	if expectsValue {
		return nil
	}
	commandIndex := idx
	if adjust != nil {
		commandIndex = adjust(idx, argv)
	}
	if commandIndex < 0 || commandIndex >= len(argv) {
		return nil
	}
	return argv[commandIndex:]
}

func isEnvAssignment(token string) bool {
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return false
	}
	name := token[:eq]
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func flagOf(lower string) string {
	if i := strings.IndexByte(lower, '='); i >= 0 {
		return lower[:i]
	}
	return lower
}

func hasEnvInlineValuePrefix(lower string) bool {
	for _, prefix := range envInlineValuePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func unwrapEnvInvocation(argv []string) []string {
	return scanWrapperInvocation(argv, []string{"--", "-"}, func(token, lower string) scanDirective {
		if isEnvAssignment(token) {
			return scanContinue
		}
		if !strings.HasPrefix(token, "-") || token == "-" {
			return scanStop
		}
		flag := flagOf(lower)
		if slices.Contains(envFlagOptions, flag) {
			return scanContinue
		}
		if slices.Contains(envOptionsWithValue, flag) {
			if strings.Contains(lower, "=") {
				return scanContinue
			}
			return scanConsumeNext
		}
		if hasEnvInlineValuePrefix(lower) {
			return scanContinue
		}
		return scanInvalid
	}, nil)
}

// envInvocationUsesModifiers reports whether an `env` invocation does anything
// beyond finding a program: assignments, -i, --unset, --chdir. Those change the
// environment the wrapped shell starts in, so the display text must cover them.
func envInvocationUsesModifiers(argv []string) bool {
	idx := 1
	expectsValue := false
	for idx < len(argv) {
		token := strings.TrimSpace(argv[idx])
		if token == "" {
			idx++
			continue
		}
		if expectsValue {
			return true
		}
		if token == "--" || token == "-" {
			break
		}
		if isEnvAssignment(token) {
			return true
		}
		if !strings.HasPrefix(token, "-") {
			break
		}
		lower := strings.ToLower(token)
		flag := flagOf(lower)
		if slices.Contains(envFlagOptions, flag) {
			return true
		}
		if slices.Contains(envOptionsWithValue, flag) {
			if strings.Contains(lower, "=") {
				return true
			}
			expectsValue = true
			idx++
			continue
		}
		if hasEnvInlineValuePrefix(lower) {
			return true
		}
		// An env flag we do not recognize is treated as a modifier.
		return true
	}
	return false
}

func unwrapDashOptionInvocation(argv []string, onFlag func(flag, lower string) scanDirective, adjust func(int, []string) int) []string {
	return scanWrapperInvocation(argv, []string{"--"}, func(token, lower string) scanDirective {
		if !strings.HasPrefix(token, "-") || token == "-" {
			return scanStop
		}
		return onFlag(flagOf(lower), lower)
	}, adjust)
}

func unwrapNiceInvocation(argv []string) []string {
	return unwrapDashOptionInvocation(argv, func(flag, lower string) scanDirective {
		if isNegativeNumberFlag(lower) {
			return scanContinue
		}
		if slices.Contains(niceOptionsWithValue, flag) {
			if strings.Contains(lower, "=") || lower != flag {
				return scanContinue
			}
			return scanConsumeNext
		}
		if strings.HasPrefix(lower, "-n") && len(lower) > 2 {
			return scanContinue
		}
		return scanInvalid
	}, nil)
}

func isNegativeNumberFlag(lower string) bool {
	if len(lower) < 2 || lower[0] != '-' {
		return false
	}
	for _, r := range lower[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func unwrapNohupInvocation(argv []string) []string {
	return scanWrapperInvocation(argv, []string{"--"}, func(token, lower string) scanDirective {
		if !strings.HasPrefix(token, "-") || token == "-" {
			return scanStop
		}
		if lower == "--help" || lower == "--version" {
			return scanContinue
		}
		return scanInvalid
	}, nil)
}

func unwrapStdbufInvocation(argv []string) []string {
	return unwrapDashOptionInvocation(argv, func(flag, lower string) scanDirective {
		if !slices.Contains(stdbufOptionsWithValue, flag) {
			return scanInvalid
		}
		if strings.Contains(lower, "=") {
			return scanContinue
		}
		return scanConsumeNext
	}, nil)
}

func unwrapTimeoutInvocation(argv []string) []string {
	return unwrapDashOptionInvocation(argv, func(flag, lower string) scanDirective {
		if slices.Contains(timeoutFlagOptions, flag) {
			return scanContinue
		}
		if slices.Contains(timeoutOptionsWithValue, flag) {
			if strings.Contains(lower, "=") {
				return scanContinue
			}
			return scanConsumeNext
		}
		return scanInvalid
	}, func(commandIndex int, argv []string) int {
		// timeout takes a required duration before the wrapped command.
		if commandIndex+1 < len(argv) {
			return commandIndex + 1
		}
		return -1
	})
}

// resolveDispatchChain peels transparent wrappers until it reaches the real
// command. It stops at the layer that does something semantic (sudo, or env
// with modifiers) and returns the argv INCLUDING that layer, so the caller
// binds to text that still shows it.
func resolveDispatchChain(argv []string) []string {
	current := argv
	wrappers := 0
	for depth := 0; depth < maxDispatchWrapperDepth; depth++ {
		u := unwrapDispatchWrapper(current)
		if u.kind == unwrapBlocked {
			return current
		}
		if u.kind != unwrapOK || len(u.argv) == 0 {
			break
		}
		wrappers++
		if isSemanticWrapperUsage(u.wrapper, current) {
			return current
		}
		current = u.argv
	}
	if wrappers >= maxDispatchWrapperDepth {
		if overflow := unwrapDispatchWrapper(current); overflow.kind != unwrapNotWrapper {
			return current
		}
	}
	return current
}

func isSemanticWrapperUsage(wrapper string, argv []string) bool {
	if wrapper == "env" {
		return envInvocationUsesModifiers(argv)
	}
	return !slices.Contains(transparentWrappers, wrapper)
}

// hasTrailingPositionalArgvAfterInlineCommand reports whether anything follows
// the inline command. `sh -c 'echo "$0"' whoami` runs whoami, and the inline
// text alone would not show it.
func hasTrailingPositionalArgvAfterInlineCommand(argv []string) bool {
	wrapperArgv := resolveDispatchChain(argv)
	if m := unwrapShellMultiplexer(wrapperArgv); m.kind == unwrapOK {
		wrapperArgv = m.argv
	}
	if len(wrapperArgv) == 0 {
		return false
	}
	token0 := strings.TrimSpace(wrapperArgv[0])
	if token0 == "" {
		return false
	}
	wrapper := normalizeExecutableToken(token0)
	if !slices.Contains(inlinePositionalWrappers, wrapper) {
		return false
	}
	var idx int
	if wrapper == "powershell" || wrapper == "pwsh" {
		_, idx = resolveInlineCommand(wrapperArgv, powerShellInlineFlags, false)
	} else {
		_, idx = resolveInlineCommand(wrapperArgv, posixInlineFlags, true)
	}
	if idx < 0 {
		return false
	}
	for _, entry := range wrapperArgv[idx+1:] {
		if strings.TrimSpace(entry) != "" {
			return true
		}
	}
	return false
}

// hasEnvManipulationBeforeShellWrapper reports whether the environment is
// changed on the way to the shell. `env BASH_ENV=/tmp/payload.sh bash -lc 'echo
// SAFE'` runs payload.sh, and "echo SAFE" is not what it does.
func hasEnvManipulationBeforeShellWrapper(argv []string) bool {
	return envManipulationBeforeShell(argv, 0, false)
}

func envManipulationBeforeShell(argv []string, depth int, seen bool) bool {
	if depth >= maxDispatchWrapperDepth || len(argv) == 0 {
		return false
	}
	token0 := strings.TrimSpace(argv[0])
	if token0 == "" {
		return false
	}

	switch u := unwrapDispatchWrapper(argv); u.kind {
	case unwrapBlocked:
		return false
	case unwrapOK:
		next := seen || (u.wrapper == "env" && envInvocationUsesModifiers(argv))
		return envManipulationBeforeShell(u.argv, depth+1, next)
	}

	switch m := unwrapShellMultiplexer(argv); m.kind {
	case unwrapBlocked:
		return false
	case unwrapOK:
		return envManipulationBeforeShell(m.argv, depth+1, seen)
	}

	kind := shellWrapperKind(normalizeExecutableToken(token0))
	if kind == shellNone {
		return false
	}
	if shellWrapperPayload(argv, kind) == "" {
		return false
	}
	return seen
}

// extractShellWrapperCommand returns the inline command a shell wrapper runs.
// rawCommand, when given, takes its place as the display text.
func extractShellWrapperCommand(argv []string, rawCommand string) (bool, string) {
	return extractShellWrapper(argv, strings.TrimSpace(rawCommand), 0)
}

func extractShellWrapper(argv []string, raw string, depth int) (bool, string) {
	if depth >= maxDispatchWrapperDepth || len(argv) == 0 {
		return false, ""
	}
	token0 := strings.TrimSpace(argv[0])
	if token0 == "" {
		return false, ""
	}

	switch u := unwrapDispatchWrapper(argv); u.kind {
	case unwrapBlocked:
		return false, ""
	case unwrapOK:
		return extractShellWrapper(u.argv, raw, depth+1)
	}

	switch m := unwrapShellMultiplexer(argv); m.kind {
	case unwrapBlocked:
		return false, ""
	case unwrapOK:
		return extractShellWrapper(m.argv, raw, depth+1)
	}

	kind := shellWrapperKind(normalizeExecutableToken(token0))
	if kind == shellNone {
		return false, ""
	}
	payload := shellWrapperPayload(argv, kind)
	if payload == "" {
		return false, ""
	}
	if raw != "" {
		return true, raw
	}
	return true, payload
}

func shellWrapperPayload(argv []string, kind shellKind) string {
	switch kind {
	case shellPosix:
		cmd, _ := resolveInlineCommand(argv, posixInlineFlags, true)
		return cmd
	case shellCmd:
		return extractCmdInlineCommand(argv)
	case shellPowerShell:
		cmd, _ := resolveInlineCommand(argv, powerShellInlineFlags, false)
		return cmd
	default:
		return ""
	}
}

func extractCmdInlineCommand(argv []string) string {
	idx := -1
	for i, item := range argv {
		token := strings.ToLower(strings.TrimSpace(item))
		if token == "/c" || token == "/k" {
			idx = i
			break
		}
	}
	if idx == -1 || idx+1 >= len(argv) {
		return ""
	}
	return strings.TrimSpace(strings.Join(argv[idx+1:], " "))
}

// resolveInlineCommand finds the inline command a shell was given, returning it
// and the index of the token it came from (-1 when there is none).
func resolveInlineCommand(argv []string, flags []string, allowCombinedC bool) (string, int) {
	for i := 1; i < len(argv); i++ {
		token := strings.TrimSpace(argv[i])
		if token == "" {
			continue
		}
		lower := strings.ToLower(token)
		if lower == "--" {
			break
		}
		if slices.Contains(flags, lower) {
			return nextToken(argv, i)
		}
		if allowCombinedC && isCombinedCFlag(token) {
			// A bundle like -lc carries the command in the next token; -cecho
			// carries it inline.
			cut := strings.IndexByte(lower, 'c')
			if inline := strings.TrimSpace(token[cut+1:]); inline != "" {
				return inline, i
			}
			return nextToken(argv, i)
		}
	}
	return "", -1
}

func nextToken(argv []string, i int) (string, int) {
	if i+1 >= len(argv) {
		return "", -1
	}
	return strings.TrimSpace(argv[i+1]), i + 1
}

// isCombinedCFlag matches a single-dash bundle containing c, like -c or -lc.
func isCombinedCFlag(token string) bool {
	if len(token) < 2 || token[0] != '-' {
		return false
	}
	rest := token[1:]
	if strings.ContainsRune(rest, '-') {
		return false
	}
	return strings.ContainsAny(rest, "cC")
}

// ---------------------------------------------------------------------------

func concat(lists ...[]string) []string {
	var out []string
	for _, list := range lists {
		out = append(out, list...)
	}
	return out
}
