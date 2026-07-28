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

// The denials are the subject of these tests. A policy that allows too much
// fails silently on someone else's machine, so every case here that ends in
// "allow" exists to prove the corresponding denial is not vacuous.
//
// The system.run approval cases are ports of the TypeScript gateway's own
// tests (node-invoke-system-run-approval*.test.ts). They are the specification
// for the approval match, and they are carried over case for case.

package bot

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func nodeSession(platform string, commands ...string) *Session {
	return &Session{
		Key:      NodeKey{Org: "acme", NodeID: "node-1"},
		ConnID:   "conn-1",
		Platform: platform,
		Commands: commands,
	}
}

func assertDeny(t *testing.T, d Decision, code string) {
	t.Helper()
	if d.Allow {
		t.Fatalf("expected denial %s, got allow", code)
	}
	if d.Code != code {
		t.Fatalf("expected code %s, got %s (%s)", code, d.Code, d.Reason)
	}
	if strings.TrimSpace(d.Reason) == "" {
		t.Fatalf("denial %s carries no reason", code)
	}
}

func assertAllow(t *testing.T, d Decision) {
	t.Helper()
	if !d.Allow {
		t.Fatalf("expected allow, got %s (%s)", d.Code, d.Reason)
	}
}

// store is an ApprovalStore whose one-shot consumption is real, so replay is
// actually exercised rather than asserted.
type store struct {
	rec      *ApprovalRecord
	consumed bool
}

func (s *store) Snapshot(runID string) *ApprovalRecord {
	if s.rec == nil || s.rec.RunID != runID {
		return nil
	}
	return s.rec
}

func (s *store) ConsumeAllowOnce(runID string) bool {
	if s.rec == nil || s.rec.RunID != runID || s.consumed || s.rec.Decision != DecisionAllowOnce {
		return false
	}
	s.consumed = true
	return true
}

var now = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

// record builds an approval bound to argv, mirroring the TypeScript makeRecord.
func record(argv []string, opts ...func(*ApprovalRecord)) *ApprovalRecord {
	binding, _ := BuildApprovalBinding(argv, "", "", "", nil)
	r := &ApprovalRecord{
		RunID:               "approval-1",
		Node:                NodeKey{Org: "acme", NodeID: "node-1"},
		Host:                ApprovalHostNode,
		Binding:             &binding,
		ExpiresAt:           now.Add(time.Minute),
		RequestedByConnID:   "conn-1",
		RequestedByDeviceID: "dev-1",
		Decision:            DecisionAllowOnce,
		Resolved:            true,
		ResolvedBy:          "operator",
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// operator holds the approval scope; writer does not.
var (
	operator = Caller{ConnID: "conn-1", DeviceID: "dev-1", Scopes: []string{"operator.write", ScopeOperatorApprovals}}
	writer   = Caller{ConnID: "conn-1", DeviceID: "dev-1", Scopes: []string{"operator.write"}}
	thisNode = NodeKey{Org: "acme", NodeID: "node-1"}
)

func sanitize(t *testing.T, in SystemRunParams, rec *ApprovalRecord) (SystemRunParams, Decision) {
	t.Helper()
	return SanitizeSystemRun(thisNode, in, operator, &store{rec: rec}, now)
}

// wantAllowOnce is the TypeScript expectAllowOnceForwardingResult.
func wantAllowOnce(t *testing.T, out SystemRunParams, d Decision) {
	t.Helper()
	assertAllow(t, d)
	if !out.Approved || out.ApprovalDecision != DecisionAllowOnce {
		t.Fatalf("expected approved allow-once, got approved=%v decision=%q", out.Approved, out.ApprovalDecision)
	}
}

// ---------------------------------------------------------------------------
// Check
// ---------------------------------------------------------------------------

func TestCheckRefusesWithoutASession(t *testing.T) {
	assertDeny(t, Check(nil, CommandSystemRun, []string{"echo"}, Mode{}), CodeNoSession)
}

func TestCheckRefusesAnEmptyCommand(t *testing.T) {
	s := nodeSession("linux", CommandSystemRun)
	for _, cmd := range []string{"", "   ", "\t\n"} {
		assertDeny(t, Check(s, cmd, nil, Mode{}), CodeCommandRequired)
	}
}

// The declaration gate is the one the node itself controls. A command the node
// never claimed to implement must not be forwarded even when the operator
// allowlisted it.
func TestCheckRefusesACommandTheNodeDidNotDeclare(t *testing.T) {
	s := nodeSession("linux", CommandSystemWhich)
	assertDeny(t, Check(s, CommandSystemRun, []string{"echo"}, Mode{}), CodeCommandNotDeclared)

	// Even with the operator explicitly allowing it.
	mode := Mode{Allow: []string{CommandSystemRun}}
	assertDeny(t, Check(s, CommandSystemRun, []string{"echo"}, mode), CodeCommandNotDeclared)
}

// An empty declaration is "I implement nothing", not "no opinion".
func TestCheckRefusesANodeThatDeclaredNothing(t *testing.T) {
	s := nodeSession("linux")
	assertDeny(t, Check(s, CommandSystemRun, []string{"echo"}, Mode{}), CodeCommandsNotDeclared)
}

func TestCheckRefusesACommandOffThePlatformDefaults(t *testing.T) {
	// iOS nodes do not implement system.run, whatever they declare.
	ios := nodeSession("ios", CommandSystemRun, CommandSystemNotify)
	assertDeny(t, Check(ios, CommandSystemRun, []string{"echo"}, Mode{}), CodeCommandNotAllowlisted)
	assertAllow(t, Check(ios, CommandSystemNotify, nil, Mode{}))

	// Linux nodes get host exec and nothing else.
	linux := nodeSession("linux", CommandSystemRun, "camera.list")
	assertAllow(t, Check(linux, CommandSystemRun, []string{"echo"}, Mode{}))
	assertDeny(t, Check(linux, "camera.list", nil, Mode{}), CodeCommandNotAllowlisted)
}

// Unclassifiable metadata must not inherit host exec. This is the fail-safe the
// TypeScript spelled "unknown: UNKNOWN_PLATFORM_COMMANDS".
func TestCheckDeniesHostExecToAnUnclassifiedPlatform(t *testing.T) {
	for _, platform := range []string{"", "plan9", "haiku", "   "} {
		s := nodeSession(platform, CommandSystemRun, CommandSystemWhich, "canvas.hide")
		assertDeny(t, Check(s, CommandSystemRun, []string{"echo"}, Mode{}), CodeCommandNotAllowlisted)
		assertDeny(t, Check(s, CommandSystemWhich, nil, Mode{}), CodeCommandNotAllowlisted)
		assertAllow(t, Check(s, "canvas.hide", nil, Mode{}))
	}
}

// Every dangerous command is off on every platform until an operator names it.
func TestCheckDeniesDangerousCommandsByDefaultEverywhere(t *testing.T) {
	platforms := []string{"ios", "android", "macos", "linux", "windows", "plan9"}
	for _, platform := range platforms {
		for _, cmd := range DangerousCommands() {
			s := nodeSession(platform, cmd)
			assertDeny(t, Check(s, cmd, nil, Mode{}), CodeCommandNotAllowlisted)
		}
	}
}

func TestCheckAllowsADangerousCommandOnlyWhenTheOperatorNamesIt(t *testing.T) {
	s := nodeSession("macos", "screen.record")
	assertDeny(t, Check(s, "screen.record", nil, Mode{}), CodeCommandNotAllowlisted)
	assertAllow(t, Check(s, "screen.record", nil, Mode{Allow: []string{"screen.record"}}))
}

// Deny is applied last, so it wins over the operator's own Allow and over the
// platform defaults.
func TestCheckDenyBeatsAllowAndDefaults(t *testing.T) {
	s := nodeSession("macos", "sms.send", CommandSystemRun)

	mode := Mode{Allow: []string{"sms.send"}, Deny: []string{"sms.send"}}
	assertDeny(t, Check(s, "sms.send", nil, mode), CodeCommandNotAllowlisted)

	mode = Mode{Deny: []string{CommandSystemRun}}
	assertDeny(t, Check(s, CommandSystemRun, []string{"echo"}, mode), CodeCommandNotAllowlisted)
}

// system.execApprovals.* edits the very policy approvals protect. It is refused
// on this path however the operator configured the overlay.
func TestCheckRefusesExecApprovalsOverInvoke(t *testing.T) {
	for _, cmd := range []string{"system.execApprovals.get", "system.execApprovals.set"} {
		s := nodeSession("linux", cmd)
		mode := Mode{Allow: []string{cmd}}
		assertDeny(t, Check(s, cmd, nil, mode), CodeExecApprovalsForbidden)
	}
}

// Stricter than the TypeScript, deliberately: no caller identity at all must not
// reach a user's shell or camera.
func TestCheckRefusesHostExecAndDangerWhenNobodyIsAuthenticated(t *testing.T) {
	s := nodeSession("macos", CommandSystemRun, CommandSystemWhich, "sms.send", "canvas.hide")
	mode := Mode{Auth: AuthNone, Allow: []string{"sms.send"}}

	assertDeny(t, Check(s, CommandSystemRun, []string{"echo"}, mode), CodeAuthModeForbids)
	assertDeny(t, Check(s, CommandSystemWhich, nil, mode), CodeAuthModeForbids)
	assertDeny(t, Check(s, "sms.send", nil, mode), CodeAuthModeForbids)

	// Read-only surface stays reachable; the gate is about exec and capture.
	assertAllow(t, Check(s, "canvas.hide", nil, mode))

	// Any other posture leaves the command reachable.
	for _, auth := range []AuthMode{AuthUnset, AuthIAM, AuthToken, AuthTrustedProxy} {
		assertAllow(t, Check(s, CommandSystemRun, []string{"echo"}, Mode{Auth: auth}))
	}
}

func TestCheckRefusesSystemRunWithNothingToRun(t *testing.T) {
	s := nodeSession("linux", CommandSystemRun)
	for _, args := range [][]string{nil, {}, {""}, {"   "}} {
		assertDeny(t, Check(s, CommandSystemRun, args, Mode{}), CodeArgvRequired)
	}
	assertAllow(t, Check(s, CommandSystemRun, []string{"echo", "hi"}, Mode{}))
}

// Permissions are the node's report of its own OS grants. They are not a gate:
// the node's operator can write anything there.
func TestCheckIgnoresSelfReportedPermissions(t *testing.T) {
	s := nodeSession("linux", CommandSystemRun)
	s.Permissions = map[string]bool{"system.run": false, "everything": true}
	assertAllow(t, Check(s, CommandSystemRun, []string{"echo"}, Mode{}))
}

func TestCheckTrimsTheCommand(t *testing.T) {
	s := nodeSession("linux", CommandSystemRun)
	assertAllow(t, Check(s, "  system.run  ", []string{"echo"}, Mode{}))
}

// ---------------------------------------------------------------------------
// Platform classification
// ---------------------------------------------------------------------------

func TestClassifyPlatform(t *testing.T) {
	cases := []struct {
		platform, family string
		want             Platform
	}{
		{"ios", "", PlatformIOS},
		{"iOS 18.2", "", PlatformIOS},
		{"android", "", PlatformAndroid},
		{"darwin", "", PlatformMacOS},
		{"macOS 15", "", PlatformMacOS},
		{"win32", "", PlatformWindows},
		{"windows", "", PlatformWindows},
		{"linux", "", PlatformLinux},
		{"", "iPhone14,2", PlatformIOS},
		{"", "Pixel Android", PlatformAndroid},
		{"", "MacBookPro18,3", PlatformMacOS},
		{"", "windows-pc", PlatformWindows},
		{"", "linux-box", PlatformLinux},
		{"", "", PlatformUnknown},
		{"plan9", "vax", PlatformUnknown},
		// The platform field wins; family is only a fallback.
		{"linux", "iphone", PlatformLinux},
		// Confusables must not dodge classification into the unknown bucket,
		// which grants canvas and camera that linux does not.
		{"ĺinux", "", PlatformLinux},
		{"ⅿacos", "", PlatformMacOS},
		{"  LINUX  ", "", PlatformLinux},
	}
	for _, c := range cases {
		if got := ClassifyPlatform(c.platform, c.family); got != c.want {
			t.Errorf("ClassifyPlatform(%q, %q) = %q, want %q", c.platform, c.family, got, c.want)
		}
	}
}

func TestAllowlistIsSortedAndDeduplicated(t *testing.T) {
	got := Allowlist(PlatformLinux, Mode{Allow: []string{CommandSystemRun, " ", "zzz.cmd"}})
	want := []string{"browser.proxy", "system.notify", "system.run", "system.run.prepare", "system.which", "zzz.cmd"}
	if len(got) != len(want) {
		t.Fatalf("Allowlist = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Allowlist = %v, want %v", got, want)
		}
	}
}

func TestNoPlatformDefaultContainsADangerousCommand(t *testing.T) {
	platforms := []Platform{PlatformIOS, PlatformAndroid, PlatformMacOS, PlatformLinux, PlatformWindows, PlatformUnknown}
	for _, p := range platforms {
		for _, cmd := range PlatformCommands(p) {
			if IsDangerous(cmd) {
				t.Errorf("platform %s defaults include dangerous command %s", p, cmd)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Auth mode
// ---------------------------------------------------------------------------

func TestRequiresTokenForInstall(t *testing.T) {
	cases := map[AuthMode]bool{
		AuthToken:        true,
		AuthIAM:          true,
		AuthUnset:        true,
		"something-else": true,
		AuthNone:         false,
		AuthTrustedProxy: false,
	}
	for mode, want := range cases {
		if got := RequiresTokenForInstall(mode); got != want {
			t.Errorf("RequiresTokenForInstall(%q) = %v, want %v", mode, got, want)
		}
	}
}

func TestCheckAuthModeRefusesTwoSecretsWithNoDeclaredMode(t *testing.T) {
	if err := CheckAuthMode(AuthUnset, true, true); err == nil {
		t.Fatal("two configured secrets with no mode must not be accepted")
	}
	for _, c := range []struct{ token, password bool }{{true, false}, {false, true}, {false, false}} {
		if err := CheckAuthMode(AuthUnset, c.token, c.password); err != nil {
			t.Errorf("CheckAuthMode(unset, %v, %v) = %v, want nil", c.token, c.password, err)
		}
	}
	if err := CheckAuthMode(AuthToken, true, true); err != nil {
		t.Errorf("an explicit mode resolves the ambiguity: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

func TestRateLimiterLocksOutAfterTheBudgetIsSpent(t *testing.T) {
	clock := now
	l := NewRateLimiter(RateLimitConfig{MaxAttempts: 3, Window: time.Minute, Lockout: 5 * time.Minute,
		Now: func() time.Time { return clock }})

	for i := 0; i < 2; i++ {
		l.RecordFailure("1.2.3.4", RateLimitScopeSharedSecret)
		res := l.Check("1.2.3.4", RateLimitScopeSharedSecret)
		if !res.Allow {
			t.Fatalf("attempt %d: locked out early", i)
		}
	}
	l.RecordFailure("1.2.3.4", RateLimitScopeSharedSecret)

	res := l.Check("1.2.3.4", RateLimitScopeSharedSecret)
	if res.Allow || res.Remaining != 0 {
		t.Fatalf("expected lockout, got %+v", res)
	}
	if res.RetryAfter != 5*time.Minute {
		t.Fatalf("RetryAfter = %v, want 5m", res.RetryAfter)
	}

	// Still locked one second before expiry, free one second after.
	clock = clock.Add(5*time.Minute - time.Second)
	if l.Check("1.2.3.4", RateLimitScopeSharedSecret).Allow {
		t.Fatal("lockout released early")
	}
	clock = clock.Add(2 * time.Second)
	if !l.Check("1.2.3.4", RateLimitScopeSharedSecret).Allow {
		t.Fatal("lockout never released")
	}
}

// A budget is per credential class: failing device-token auth must not lock out
// shared-secret auth, and vice versa.
func TestRateLimiterScopesAreIndependent(t *testing.T) {
	clock := now
	l := NewRateLimiter(RateLimitConfig{MaxAttempts: 2, Now: func() time.Time { return clock }})
	l.RecordFailure("1.2.3.4", RateLimitScopeDeviceToken)
	l.RecordFailure("1.2.3.4", RateLimitScopeDeviceToken)

	if l.Check("1.2.3.4", RateLimitScopeDeviceToken).Allow {
		t.Fatal("device-token budget should be spent")
	}
	if !l.Check("1.2.3.4", RateLimitScopeSharedSecret).Allow {
		t.Fatal("shared-secret budget must be untouched")
	}
	if !l.Check("5.6.7.8", RateLimitScopeDeviceToken).Allow {
		t.Fatal("another address must be untouched")
	}
}

func TestRateLimiterSlidesTheWindow(t *testing.T) {
	clock := now
	l := NewRateLimiter(RateLimitConfig{MaxAttempts: 2, Window: time.Minute,
		Now: func() time.Time { return clock }})
	l.RecordFailure("1.2.3.4", "")
	clock = clock.Add(61 * time.Second)
	l.RecordFailure("1.2.3.4", "")
	if res := l.Check("1.2.3.4", ""); !res.Allow || res.Remaining != 1 {
		t.Fatalf("the first attempt should have fallen out of the window: %+v", res)
	}
}

func TestRateLimiterExemptsLoopbackUnlessAskedNotTo(t *testing.T) {
	l := NewRateLimiter(RateLimitConfig{MaxAttempts: 1, Now: func() time.Time { return now }})
	for _, ip := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1"} {
		l.RecordFailure(ip, "")
		if !l.Check(ip, "").Allow {
			t.Fatalf("loopback %s must not lock itself out", ip)
		}
	}
	if l.Size() != 0 {
		t.Fatalf("loopback must not allocate budgets, size = %d", l.Size())
	}

	strict := NewRateLimiter(RateLimitConfig{MaxAttempts: 1, RateLimitLoopback: true,
		Now: func() time.Time { return now }})
	strict.RecordFailure("127.0.0.1", "")
	if strict.Check("127.0.0.1", "").Allow {
		t.Fatal("RateLimitLoopback must remove the exemption")
	}
}

func TestRateLimiterResetAndPrune(t *testing.T) {
	clock := now
	l := NewRateLimiter(RateLimitConfig{MaxAttempts: 2, Window: time.Minute, Lockout: time.Minute,
		Now: func() time.Time { return clock }})
	l.RecordFailure("1.2.3.4", "")
	l.Reset("1.2.3.4", "")
	if !l.Check("1.2.3.4", "").Allow || l.Size() != 0 {
		t.Fatal("Reset must clear the budget")
	}

	l.RecordFailure("1.2.3.4", "")
	l.RecordFailure("1.2.3.4", "")
	l.Prune()
	if l.Size() != 1 {
		t.Fatal("a locked-out entry must survive pruning, or pruning releases the lock")
	}
	clock = clock.Add(2 * time.Minute)
	l.Prune()
	if l.Size() != 0 {
		t.Fatal("an expired entry should have been pruned")
	}
}

// An attacker must not multiply their budget by spelling their address
// differently.
func TestCanonicalClientIPCollapsesSpellings(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":            "1.2.3.4",
		"::ffff:1.2.3.4":     "1.2.3.4",
		"1.2.3.4:5678":       "1.2.3.4",
		"[::ffff:1.2.3.4]":   "1.2.3.4",
		"[2001:db8::1]:443":  "2001:db8::1",
		"2001:DB8::1":        "2001:db8::1",
		"  1.2.3.4  ":        "1.2.3.4",
		"":                   "unknown",
		"not-an-address":     "unknown",
		"1.2.3.4.5":          "unknown",
		"::ffff:127.0.0.1":   "127.0.0.1",
		"[::1]:1234":         "::1",
		"host.example:1234":  "unknown",
		"1.2.3.4:notaport:9": "unknown",
	}
	for in, want := range cases {
		if got := CanonicalClientIP(in); got != want {
			t.Errorf("CanonicalClientIP(%q) = %q, want %q", in, got, want)
		}
	}

	clock := now
	l := NewRateLimiter(RateLimitConfig{MaxAttempts: 2, Now: func() time.Time { return clock }})
	l.RecordFailure("1.2.3.4", "")
	l.RecordFailure("::ffff:1.2.3.4", "")
	if l.Check("1.2.3.4:9999", "").Allow {
		t.Fatal("the same address spelled three ways must share one budget")
	}
}

// ---------------------------------------------------------------------------
// SanitizeSystemRun — ports of the TypeScript approval tests
// ---------------------------------------------------------------------------

// The operator reads rawCommand. Display text that disagrees with the argv is
// the whole attack.
func TestSanitizeRejectsCmdExeTrailingArgMismatchAgainstRawCommand(t *testing.T) {
	_, d := sanitize(t, SystemRunParams{
		Command:          []string{"cmd.exe", "/d", "/s", "/c", "echo", "SAFE&&whoami"},
		RawCommand:       "echo",
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, record([]string{"echo"}))
	assertDeny(t, d, CodeRawCommandMismatch)
}

func TestSanitizeAcceptsMatchingCmdExeCommandText(t *testing.T) {
	argv := []string{"cmd.exe", "/d", "/s", "/c", "echo", "SAFE&&whoami"}
	out, d := sanitize(t, SystemRunParams{
		Command:          argv,
		RawCommand:       "echo SAFE&&whoami",
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, record(argv))
	wantAllowOnce(t, out, d)
}

// `env BASH_ENV=/tmp/payload.sh bash -lc 'echo SAFE'` does not run "echo SAFE".
// An approval bound to the inline text alone must not cover it.
func TestSanitizeRejectsEnvAssignmentWrapperWhenApprovalOmitsThePrelude(t *testing.T) {
	_, d := sanitize(t, SystemRunParams{
		Command:          []string{"/usr/bin/env", "BASH_ENV=/tmp/payload.sh", "bash", "-lc", "echo SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, record([]string{"echo SAFE"}))
	assertDeny(t, d, CodeRequestMismatch)
}

func TestSanitizeAcceptsEnvAssignmentWrapperBoundToTheFullArgv(t *testing.T) {
	argv := []string{"/usr/bin/env", "BASH_ENV=/tmp/payload.sh", "bash", "-lc", "echo SAFE"}
	out, d := sanitize(t, SystemRunParams{
		Command:          argv,
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, record(argv))
	wantAllowOnce(t, out, d)
}

func TestSanitizeRejectsTrailingSpaceArgvAgainstACommandOnlyApproval(t *testing.T) {
	_, d := sanitize(t, SystemRunParams{
		Command:          []string{"runner "},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, record([]string{"runner"}))
	assertDeny(t, d, CodeRequestMismatch)
}

func TestSanitizeAcceptsMatchingTrailingSpaceArgv(t *testing.T) {
	out, d := sanitize(t, SystemRunParams{
		Command:          []string{"runner "},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, record([]string{"runner "}))
	wantAllowOnce(t, out, d)
}

// One string is not the same question as two tokens.
func TestSanitizeEnforcesArgvIdentityNotJoinedText(t *testing.T) {
	_, d := sanitize(t, SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, record([]string{"echo SAFE"}))
	assertDeny(t, d, CodeRequestMismatch)
}

// When the record carries a plan, the plan is what runs. Everything the caller
// said about the command after the operator answered is discarded.
func TestSanitizeUsesThePlanAndIgnoresCallerTampering(t *testing.T) {
	plan := &ApprovalPlan{
		Argv:       []string{"/usr/bin/echo", "SAFE"},
		Cwd:        "/real/cwd",
		RawCommand: "/usr/bin/echo SAFE",
		AgentID:    "main",
		SessionKey: "agent:main:main",
	}
	binding, _ := BuildApprovalBinding(plan.Argv, plan.Cwd, plan.AgentID, plan.SessionKey, nil)
	rec := record(nil, func(r *ApprovalRecord) {
		r.Plan = plan
		r.Binding = &binding
	})

	out, d := sanitize(t, SystemRunParams{
		Command:          []string{"echo", "PWNED"},
		RawCommand:       "echo PWNED",
		Cwd:              "/tmp/attacker-link/sub",
		AgentID:          "attacker",
		SessionKey:       "agent:attacker:main",
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, rec)
	wantAllowOnce(t, out, d)

	if strings.Join(out.Command, " ") != "/usr/bin/echo SAFE" {
		t.Fatalf("forwarded command = %v, want the plan's argv", out.Command)
	}
	if out.RawCommand != "/usr/bin/echo SAFE" || out.Cwd != "/real/cwd" ||
		out.AgentID != "main" || out.SessionKey != "agent:main:main" {
		t.Fatalf("forwarded context came from the caller, not the plan: %+v", out)
	}
}

// GIT_EXTERNAL_DIFF turns an approved `git diff` into arbitrary execution.
func TestSanitizeRejectsEnvOverridesTheApprovalNeverCovered(t *testing.T) {
	_, d := sanitize(t, SystemRunParams{
		Command:          []string{"git", "diff"},
		RawCommand:       "git diff",
		Env:              map[string]string{"GIT_EXTERNAL_DIFF": "/tmp/pwn.sh"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, record([]string{"git", "diff"}))
	assertDeny(t, d, CodeEnvBindingMissing)
}

func TestSanitizeRejectsAnEnvHashMismatch(t *testing.T) {
	binding, _ := BuildApprovalBinding([]string{"git", "diff"}, "", "", "", map[string]string{"SAFE": "1"})
	rec := record(nil, func(r *ApprovalRecord) { r.Binding = &binding })

	_, d := sanitize(t, SystemRunParams{
		Command:          []string{"git", "diff"},
		RawCommand:       "git diff",
		Env:              map[string]string{"SAFE": "2"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, rec)
	assertDeny(t, d, CodeEnvMismatch)
}

func TestSanitizeAcceptsAMatchingEnvHashWhateverTheKeyOrder(t *testing.T) {
	binding, keys := BuildApprovalBinding([]string{"git", "diff"}, "", "", "",
		map[string]string{"SAFE_A": "1", "SAFE_B": "2"})
	if len(keys) != 2 || keys[0] != "SAFE_A" || keys[1] != "SAFE_B" {
		t.Fatalf("env keys must be sorted for a stable prompt, got %v", keys)
	}
	rec := record(nil, func(r *ApprovalRecord) { r.Binding = &binding })

	out, d := sanitize(t, SystemRunParams{
		Command:          []string{"git", "diff"},
		RawCommand:       "git diff",
		Env:              map[string]string{"SAFE_B": "2", "SAFE_A": "1"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, rec)
	wantAllowOnce(t, out, d)
}

// A key no shell would call portable still has to be bound, name and value.
// Dropping it — the TypeScript filtered keys through a portability check first —
// would let an override ride along on an approval that never covered it.
func TestSanitizeBindsEnvKeysItCannotReasonAbout(t *testing.T) {
	weird := []string{"BASH_ENV=x", "9LEADING", "with space", "", "a-b"}

	// Unapproved, it widens the binding rather than vanishing from it.
	for _, key := range weird {
		_, d := sanitize(t, SystemRunParams{
			Command:          []string{"git", "diff"},
			RawCommand:       "git diff",
			Env:              map[string]string{key: "y"},
			RunID:            "approval-1",
			Approved:         true,
			ApprovalDecision: DecisionAllowOnce,
		}, record([]string{"git", "diff"}))
		assertDeny(t, d, CodeEnvBindingMissing)
	}

	// Approved, the binding still distinguishes it from its neighbours, so the
	// approval covers that key with that value and nothing else.
	for _, key := range weird {
		binding, keys := BuildApprovalBinding([]string{"git", "diff"}, "", "", "",
			map[string]string{key: "y"})
		if len(keys) != 1 || keys[0] != key {
			t.Fatalf("env key %q must reach the operator's prompt, got %v", key, keys)
		}
		rec := func() *ApprovalRecord {
			return record(nil, func(r *ApprovalRecord) { b := binding; r.Binding = &b })
		}
		params := func(env map[string]string) SystemRunParams {
			return SystemRunParams{
				Command:          []string{"git", "diff"},
				RawCommand:       "git diff",
				Env:              env,
				RunID:            "approval-1",
				Approved:         true,
				ApprovalDecision: DecisionAllowOnce,
			}
		}

		out, d := sanitize(t, params(map[string]string{key: "y"}), rec())
		wantAllowOnce(t, out, d)

		_, d = sanitize(t, params(map[string]string{key: "z"}), rec())
		assertDeny(t, d, CodeEnvMismatch)

		_, d = sanitize(t, params(map[string]string{key + "!": "y"}), rec())
		assertDeny(t, d, CodeEnvMismatch)

		_, d = sanitize(t, params(nil), rec())
		assertDeny(t, d, CodeEnvMismatch)
	}
}

func TestSanitizeConsumesAllowOnceAndBlocksReplay(t *testing.T) {
	s := &store{rec: record([]string{"echo", "SAFE"})}
	params := SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RawCommand:       "echo SAFE",
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}

	out, d := SanitizeSystemRun(thisNode, params, operator, s, now)
	wantAllowOnce(t, out, d)

	_, d = SanitizeSystemRun(thisNode, params, operator, s, now)
	assertDeny(t, d, CodeApprovalRequired)
}

func TestSanitizeAllowAlwaysIsNotConsumed(t *testing.T) {
	s := &store{rec: record([]string{"echo", "SAFE"}, func(r *ApprovalRecord) {
		r.Decision = DecisionAllowAlways
	})}
	params := SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowAlways,
	}
	for i := 0; i < 3; i++ {
		out, d := SanitizeSystemRun(thisNode, params, operator, s, now)
		assertAllow(t, d)
		if !out.Approved || out.ApprovalDecision != DecisionAllowAlways {
			t.Fatalf("call %d: %+v", i, out)
		}
	}
}

func TestSanitizeRejectsAnApprovalWithNoNodeBinding(t *testing.T) {
	for _, node := range []NodeKey{{}, {Org: "acme"}, {NodeID: "node-1"}} {
		rec := record([]string{"echo", "SAFE"}, func(r *ApprovalRecord) { r.Node = node })
		_, d := sanitize(t, SystemRunParams{
			Command:          []string{"echo", "SAFE"},
			RunID:            "approval-1",
			Approved:         true,
			ApprovalDecision: DecisionAllowOnce,
		}, rec)
		assertDeny(t, d, CodeNodeBindingMissing)
	}
}

func TestSanitizeRejectsAnApprovalReplayedAtAnotherNode(t *testing.T) {
	other := NodeKey{Org: "acme", NodeID: "node-2"}
	_, d := SanitizeSystemRun(other, SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, operator, &store{rec: record([]string{"echo", "SAFE"})}, now)
	assertDeny(t, d, CodeNodeMismatch)
}

// The invariant the whole port exists for: another tenant's node must be
// indistinguishable from a node that is simply not this one. Same code, same
// reason, byte for byte — anything else confirms that some other org has a node
// by that name.
func TestSanitizeCannotTellAForeignNodeFromAnUnrelatedOne(t *testing.T) {
	params := SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}
	rec := record([]string{"echo", "SAFE"})

	// Same node id, different tenant.
	_, foreign := SanitizeSystemRun(NodeKey{Org: "globex", NodeID: "node-1"}, params, operator, &store{rec: rec}, now)
	// Same tenant, a node id that does not exist.
	_, unrelated := SanitizeSystemRun(NodeKey{Org: "acme", NodeID: "node-404"}, params, operator, &store{rec: rec}, now)

	assertDeny(t, foreign, CodeNodeMismatch)
	if foreign != unrelated {
		t.Fatalf("a foreign node is distinguishable from an unrelated one: %+v vs %+v", foreign, unrelated)
	}
}

func TestSanitizeRejectsAnApprovalRequestedByAnotherDevice(t *testing.T) {
	caller := Caller{ConnID: "conn-1", DeviceID: "dev-2", Scopes: []string{ScopeOperatorApprovals}}
	_, d := SanitizeSystemRun(thisNode, SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, caller, &store{rec: record([]string{"echo", "SAFE"})}, now)
	assertDeny(t, d, CodeDeviceMismatch)
}

// Without a device identity the connection is the only binding available.
func TestSanitizeFallsBackToTheConnectionWhenThereIsNoDevice(t *testing.T) {
	rec := record([]string{"echo", "SAFE"}, func(r *ApprovalRecord) { r.RequestedByDeviceID = "" })
	params := SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}

	other := Caller{ConnID: "conn-2", Scopes: []string{ScopeOperatorApprovals}}
	_, d := SanitizeSystemRun(thisNode, params, other, &store{rec: rec}, now)
	assertDeny(t, d, CodeClientMismatch)

	same := Caller{ConnID: "conn-1", Scopes: []string{ScopeOperatorApprovals}}
	out, d := SanitizeSystemRun(thisNode, params, same, &store{rec: rec}, now)
	wantAllowOnce(t, out, d)
}

func TestSanitizeRejectsAnApprovalForAnotherHost(t *testing.T) {
	rec := record([]string{"echo", "SAFE"}, func(r *ApprovalRecord) { r.Host = "gateway" })
	_, d := sanitize(t, SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, rec)
	assertDeny(t, d, CodeRequestMismatch)
}

// An approval that predates binding is an answer to an unknown question.
func TestSanitizeRejectsAnApprovalCarryingNoBinding(t *testing.T) {
	rec := record([]string{"echo", "SAFE"}, func(r *ApprovalRecord) { r.Binding = nil })
	_, d := sanitize(t, SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, rec)
	assertDeny(t, d, CodeRequestMismatch)

	empty := record(nil)
	_, d = sanitize(t, SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, empty)
	assertDeny(t, d, CodeRequestMismatch)
}

func TestSanitizeRejectsUnknownAndExpiredApprovals(t *testing.T) {
	params := SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}

	_, d := SanitizeSystemRun(thisNode, params, operator, &store{}, now)
	assertDeny(t, d, CodeUnknownApprovalID)

	other := params
	other.RunID = "approval-elsewhere"
	_, d = SanitizeSystemRun(thisNode, other, operator, &store{rec: record([]string{"echo", "SAFE"})}, now)
	assertDeny(t, d, CodeUnknownApprovalID)

	_, d = SanitizeSystemRun(thisNode, params, operator, &store{rec: record([]string{"echo", "SAFE"})},
		now.Add(2*time.Minute))
	assertDeny(t, d, CodeApprovalExpired)
}

func TestSanitizeRefusesWhenThereIsNoApprovalStore(t *testing.T) {
	_, d := SanitizeSystemRun(thisNode, SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}, operator, nil, now)
	assertDeny(t, d, CodeApprovalsUnavail)
}

// A caller with plain write access cannot approve their own command by asking.
func TestSanitizeRefusesAnOverrideWithNoRunIDFromAWriter(t *testing.T) {
	for _, params := range []SystemRunParams{
		{Command: []string{"echo", "SAFE"}, Approved: true},
		{Command: []string{"echo", "SAFE"}, ApprovalDecision: DecisionAllowAlways},
		{Command: []string{"echo", "SAFE"}, Approved: true, RunID: "   "},
	} {
		out, d := SanitizeSystemRun(thisNode, params, writer, &store{rec: record([]string{"echo", "SAFE"})}, now)
		assertDeny(t, d, CodeMissingRunID)
		if out.Approved || len(out.Command) > 0 {
			t.Fatalf("a denial must forward nothing, got %+v", out)
		}
	}
}

// The embedded runner with ask=off IS the approval authority, so it may
// pre-approve without a record. Nobody else may.
func TestSanitizeAllowsPreApprovalOnlyFromAnApprovalScopedCaller(t *testing.T) {
	params := SystemRunParams{Command: []string{"echo", "SAFE"}, Approved: true}

	for _, c := range []Caller{
		{Scopes: []string{ScopeOperatorApprovals}},
		{Scopes: []string{ScopeOperatorAdmin}},
	} {
		out, d := SanitizeSystemRun(thisNode, params, c, nil, now)
		assertAllow(t, d)
		if !out.Approved {
			t.Fatal("pre-approval must forward approved=true")
		}
		if out.ApprovalDecision != "" {
			t.Fatalf("pre-approval must not invent a decision, got %q", out.ApprovalDecision)
		}
	}

	for _, c := range []Caller{{}, writer, {Scopes: []string{"operator.read", "admin"}}} {
		_, d := SanitizeSystemRun(thisNode, params, c, nil, now)
		assertDeny(t, d, CodeMissingRunID)
	}

	// A decision alone is not a pre-approval: the TypeScript gated this on
	// approved === true, and asking for allow-once is not claiming it.
	_, d := SanitizeSystemRun(thisNode,
		SystemRunParams{Command: []string{"echo"}, ApprovalDecision: DecisionAllowOnce},
		operator, nil, now)
	assertDeny(t, d, CodeMissingRunID)
}

// Nobody answered in time. An askFallback allow-once is honored only for a
// caller that is itself allowed to decide approvals.
func TestSanitizeHonorsAskFallbackOnlyForApprovalScopedCallers(t *testing.T) {
	timedOut := func() *ApprovalRecord {
		return record([]string{"echo", "SAFE"}, func(r *ApprovalRecord) {
			r.Decision = ""
			r.Resolved = true
			r.ResolvedBy = ""
		})
	}
	params := SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RunID:            "approval-1",
		Approved:         true,
		ApprovalDecision: DecisionAllowOnce,
	}

	out, d := SanitizeSystemRun(thisNode, params, operator, &store{rec: timedOut()}, now)
	wantAllowOnce(t, out, d)

	_, d = SanitizeSystemRun(thisNode, params, writer, &store{rec: timedOut()}, now)
	assertDeny(t, d, CodeApprovalRequired)

	// allow-always is not a fallback an operator ever offered.
	always := params
	always.ApprovalDecision = DecisionAllowAlways
	_, d = SanitizeSystemRun(thisNode, always, operator, &store{rec: timedOut()}, now)
	assertDeny(t, d, CodeApprovalRequired)

	// A request still pending (never resolved) is not a timeout.
	pending := record([]string{"echo", "SAFE"}, func(r *ApprovalRecord) {
		r.Decision = ""
		r.Resolved = false
		r.ResolvedBy = ""
	})
	_, d = SanitizeSystemRun(thisNode, params, operator, &store{rec: pending}, now)
	assertDeny(t, d, CodeApprovalRequired)

	// A denial is not a timeout either.
	denied := record([]string{"echo", "SAFE"}, func(r *ApprovalRecord) {
		r.Decision = "deny"
		r.ResolvedBy = "operator"
	})
	_, d = SanitizeSystemRun(thisNode, params, operator, &store{rec: denied}, now)
	assertDeny(t, d, CodeApprovalRequired)
}

// With no override claimed there is no approval to check — but the control
// fields are still stripped, and the command line still has to be coherent.
func TestSanitizeStripsControlFieldsWhenNoOverrideIsClaimed(t *testing.T) {
	out, d := SanitizeSystemRun(thisNode, SystemRunParams{
		Command:          []string{"echo", "SAFE"},
		RawCommand:       "echo SAFE",
		Cwd:              "/tmp",
		Env:              map[string]string{"SAFE": "1"},
		TimeoutMs:        1000,
		AgentID:          "main",
		ApprovalDecision: "allow-forever", // not a decision this package knows
	}, writer, nil, now)
	assertAllow(t, d)
	if out.Approved || out.ApprovalDecision != "" {
		t.Fatalf("control fields survived: %+v", out)
	}
	if out.Cwd != "/tmp" || out.TimeoutMs != 1000 || out.AgentID != "main" || out.Env["SAFE"] != "1" {
		t.Fatalf("the rest of the request must be forwarded intact: %+v", out)
	}

	// An incoherent command line is refused even with nothing to approve.
	_, d = SanitizeSystemRun(thisNode, SystemRunParams{
		Command:    []string{"echo", "SAFE"},
		RawCommand: "rm -rf /",
	}, writer, nil, now)
	assertDeny(t, d, CodeRawCommandMismatch)
}

func TestSanitizeForwardsNothingOnEveryDenial(t *testing.T) {
	cases := []SystemRunParams{
		{Command: []string{"echo"}, Approved: true},
		{Command: []string{"echo"}, Approved: true, RunID: "nope"},
		{Command: []string{"echo", "PWNED"}, Approved: true, RunID: "approval-1"},
		{Command: []string{"echo"}, RawCommand: "not-echo", Approved: true, RunID: "approval-1"},
	}
	for i, params := range cases {
		out, d := SanitizeSystemRun(thisNode, params, writer, &store{rec: record([]string{"echo"})}, now)
		if d.Allow {
			continue
		}
		if len(out.Command) > 0 || out.Approved || out.ApprovalDecision != "" ||
			out.RawCommand != "" || out.Cwd != "" || out.RunID != "" {
			t.Fatalf("case %d: a denial forwarded %+v", i, out)
		}
	}
}

// ---------------------------------------------------------------------------
// EvaluateApprovalMatch — ports of the TypeScript match tests
// ---------------------------------------------------------------------------

func TestEvaluateApprovalMatch(t *testing.T) {
	bind := func(argv []string, env map[string]string) *ApprovalBinding {
		b, _ := BuildApprovalBinding(argv, "", "", "", env)
		return &b
	}

	t.Run("rejects an approval carrying no binding", func(t *testing.T) {
		rec := &ApprovalRecord{Host: ApprovalHostNode}
		assertDeny(t, EvaluateApprovalMatch([]string{"echo", "SAFE"}, rec, "", "", "", nil), CodeRequestMismatch)
	})

	t.Run("enforces exact argv", func(t *testing.T) {
		rec := &ApprovalRecord{Host: ApprovalHostNode, Binding: bind([]string{"echo", "SAFE"}, nil)}
		assertAllow(t, EvaluateApprovalMatch([]string{"echo", "SAFE"}, rec, "", "", "", nil))
		assertDeny(t, EvaluateApprovalMatch([]string{"echo", "safe"}, rec, "", "", "", nil), CodeRequestMismatch)
		assertDeny(t, EvaluateApprovalMatch([]string{"echo"}, rec, "", "", "", nil), CodeRequestMismatch)
		assertDeny(t, EvaluateApprovalMatch([]string{"echo", "SAFE", ""}, rec, "", "", "", nil), CodeRequestMismatch)
		assertDeny(t, EvaluateApprovalMatch([]string{"echo SAFE"}, rec, "", "", "", nil), CodeRequestMismatch)
	})

	t.Run("rejects env overrides when the binding has no env hash", func(t *testing.T) {
		rec := &ApprovalRecord{Host: ApprovalHostNode, Binding: bind([]string{"git", "diff"}, nil)}
		d := EvaluateApprovalMatch([]string{"git", "diff"}, rec, "", "", "",
			map[string]string{"GIT_EXTERNAL_DIFF": "/tmp/pwn.sh"})
		assertDeny(t, d, CodeEnvBindingMissing)
	})

	t.Run("accepts a matching env hash with reordered keys", func(t *testing.T) {
		rec := &ApprovalRecord{Host: ApprovalHostNode,
			Binding: bind([]string{"git", "diff"}, map[string]string{"SAFE_A": "1", "SAFE_B": "2"})}
		assertAllow(t, EvaluateApprovalMatch([]string{"git", "diff"}, rec, "", "", "",
			map[string]string{"SAFE_B": "2", "SAFE_A": "1"}))
	})

	t.Run("rejects a non-node host", func(t *testing.T) {
		rec := &ApprovalRecord{Host: "gateway", Binding: bind([]string{"echo", "SAFE"}, nil)}
		assertDeny(t, EvaluateApprovalMatch([]string{"echo", "SAFE"}, rec, "", "", "", nil), CodeRequestMismatch)
		assertDeny(t, EvaluateApprovalMatch([]string{"echo", "SAFE"}, nil, "", "", "", nil), CodeRequestMismatch)
	})

	t.Run("binds cwd, agent and session too", func(t *testing.T) {
		b, _ := BuildApprovalBinding([]string{"echo"}, "/real", "main", "agent:main:main", nil)
		rec := &ApprovalRecord{Host: ApprovalHostNode, Binding: &b}
		assertAllow(t, EvaluateApprovalMatch([]string{"echo"}, rec, "/real", "main", "agent:main:main", nil))
		assertDeny(t, EvaluateApprovalMatch([]string{"echo"}, rec, "/other", "main", "agent:main:main", nil), CodeRequestMismatch)
		assertDeny(t, EvaluateApprovalMatch([]string{"echo"}, rec, "/real", "attacker", "agent:main:main", nil), CodeRequestMismatch)
		assertDeny(t, EvaluateApprovalMatch([]string{"echo"}, rec, "/real", "main", "agent:attacker:main", nil), CodeRequestMismatch)
	})

	// Dropping the env overrides entirely is a different question from making
	// them, and must not silently pass an approval that named them.
	t.Run("rejects a request that drops the approved env", func(t *testing.T) {
		rec := &ApprovalRecord{Host: ApprovalHostNode,
			Binding: bind([]string{"git", "diff"}, map[string]string{"SAFE": "1"})}
		assertDeny(t, EvaluateApprovalMatch([]string{"git", "diff"}, rec, "", "", "", nil), CodeEnvMismatch)
	})
}

// ---------------------------------------------------------------------------
// Command text
// ---------------------------------------------------------------------------

func TestResolveSystemRunCommandRequiresACommandForRawText(t *testing.T) {
	_, _, d := ResolveSystemRunCommand(nil, "echo hi")
	assertDeny(t, d, CodeMissingCommand)

	argv, text, d := ResolveSystemRunCommand(nil, "")
	assertAllow(t, d)
	if len(argv) != 0 || text != "" {
		t.Fatalf("an empty request resolves to nothing, got %v %q", argv, text)
	}
}

// The text an operator reads has to be the text the machine runs.
func TestResolveSystemRunCommandBindsDisplayTextToWhatActuallyRuns(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		raw  string
		text string // "" means expect RAW_COMMAND_MISMATCH
	}{
		{"plain argv", []string{"echo", "hi"}, "", "echo hi"},
		{"argv needing quotes", []string{"echo", "a b"}, "", `echo "a b"`},
		{"empty arg is visible", []string{"echo", ""}, "", `echo ""`},
		{"posix inline command", []string{"bash", "-lc", "echo SAFE"}, "", "echo SAFE"},
		{"posix combined flag", []string{"sh", "-c", "echo SAFE"}, "", "echo SAFE"},
		{"cmd inline command", []string{"cmd.exe", "/d", "/s", "/c", "echo", "SAFE"}, "", "echo SAFE"},
		{"powershell inline command", []string{"pwsh", "-c", "echo SAFE"}, "", "echo SAFE"},
		{"busybox multiplexer", []string{"busybox", "sh", "-c", "echo SAFE"}, "", "echo SAFE"},
		{"transparent timeout wrapper", []string{"timeout", "5", "bash", "-lc", "echo SAFE"}, "", "echo SAFE"},
		{"transparent nohup wrapper", []string{"nohup", "bash", "-lc", "echo SAFE"}, "", "echo SAFE"},

		// The inline text alone is a lie in each of these, so the display text
		// binds to the whole argv instead.
		{"env prelude before the shell", []string{"env", "BASH_ENV=/tmp/p.sh", "bash", "-lc", "echo SAFE"}, "",
			"env BASH_ENV=/tmp/p.sh bash -lc \"echo SAFE\""},
		{"positional args after the inline command", []string{"sh", "-c", `echo "$0"`, "whoami"}, "",
			`sh -c "echo \"$0\"" whoami`},
		{"sudo is not peeled", []string{"sudo", "bash", "-lc", "echo SAFE"}, "", `sudo bash -lc "echo SAFE"`},
		{"setsid is not peeled", []string{"setsid", "bash", "-lc", "echo SAFE"}, "", `setsid bash -lc "echo SAFE"`},

		// Matching raw text is accepted; anything else is refused.
		{"matching raw text", []string{"bash", "-lc", "echo SAFE"}, "echo SAFE", "echo SAFE"},
		{"truncated raw text", []string{"cmd.exe", "/c", "echo", "SAFE&&whoami"}, "echo", ""},
		{"raw text hiding the env prelude", []string{"env", "BASH_ENV=/tmp/p.sh", "bash", "-lc", "echo SAFE"}, "echo SAFE", ""},
		{"raw text hiding a trailing positional", []string{"sh", "-c", "echo hi", "whoami"}, "echo hi", ""},
		{"raw text hiding sudo", []string{"sudo", "bash", "-lc", "echo SAFE"}, "echo SAFE", ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, text, d := ResolveSystemRunCommand(c.argv, c.raw)
			if c.text == "" {
				assertDeny(t, d, CodeRawCommandMismatch)
				return
			}
			assertAllow(t, d)
			if text != c.text {
				t.Fatalf("cmdText = %q, want %q", text, c.text)
			}
			if len(argv) != len(c.argv) {
				t.Fatalf("argv must be forwarded unchanged, got %v", argv)
			}
		})
	}
}

func TestFormatExecCommandQuotesWhatWouldOtherwiseHide(t *testing.T) {
	cases := map[string]string{
		"echo|hi":       "echo hi",
		"echo|a b":      `echo "a b"`,
		`echo|say "hi"`: `echo "say \"hi\""`,
		"echo|":         `echo ""`,
		"echo|\ttab":    "echo \"\ttab\"",
		"a b|c\nd":      `"a b" "c` + "\n" + `d"`,
	}
	for in, want := range cases {
		argv := strings.Split(in, "|")
		if got := FormatExecCommand(argv); got != want {
			t.Errorf("FormatExecCommand(%q) = %q, want %q", argv, got, want)
		}
	}
}
