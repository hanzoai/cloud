// SPDX-License-Identifier: Apache-2.0

package sandboxpolicy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
)

// Classic BPF (cBPF) / Linux seccomp-BPF ABI constants (clean-room definitions
// matching Linux UAPI include/uapi/linux/filter.h and include/uapi/linux/seccomp.h).
const (
	// BPFMaxInsns is the Linux kernel hard limit on classic BPF program length.
	BPFMaxInsns = 4096

	// BPF instruction classes.
	BPFClassLD  uint16 = 0x00
	BPFClassALU uint16 = 0x04
	BPFClassJMP uint16 = 0x05
	BPFClassRET uint16 = 0x06

	// Load size & mode.
	BPFSizeW   uint16 = 0x00
	BPFModeABS uint16 = 0x20

	// ALU operations & sources.
	BPFAluAND uint16 = 0x50
	BPFSrcK   uint16 = 0x00

	// Jump operations.
	BPFJmpJA   uint16 = 0x00
	BPFJmpJEQ  uint16 = 0x10
	BPFJmpJGT  uint16 = 0x20
	BPFJmpJGE  uint16 = 0x30
	BPFJmpJSET uint16 = 0x40

	// Convenience composite opcodes.
	OpLDWABS uint16 = BPFClassLD | BPFSizeW | BPFModeABS
	OpANDK   uint16 = BPFClassALU | BPFAluAND | BPFSrcK
	OpJA     uint16 = BPFClassJMP | BPFJmpJA
	OpJEQK   uint16 = BPFClassJMP | BPFJmpJEQ | BPFSrcK
	OpJGTK   uint16 = BPFClassJMP | BPFJmpJGT | BPFSrcK
	OpJGEK   uint16 = BPFClassJMP | BPFJmpJGE | BPFSrcK
	OpJSETK  uint16 = BPFClassJMP | BPFJmpJSET | BPFSrcK
	OpRETK   uint16 = BPFClassRET | BPFSrcK

	// Linux AUDIT_ARCH_* constants for struct seccomp_data.arch.
	AuditArchX86_64  uint32 = 0xC000003E
	AuditArchAArch64 uint32 = 0xC00000B7

	// X32SyscallBit is Linux x86_64 __X32_SYSCALL_BIT (0x40000000).
	// x32 ABI syscalls share AUDIT_ARCH_X86_64 and set bit 30 in seccomp_data.nr.
	X32SyscallBit uint32 = 0x40000000

	// Byte offsets inside Linux struct seccomp_data (64 bytes):
	//   int   nr;                 // offset 0  (4 bytes)
	//   __u32 arch;               // offset 4  (4 bytes)
	//   __u64 instruction_pointer;// offset 8  (8 bytes)
	//   __u64 args[6];            // offset 16 (48 bytes, little-endian)
	SeccompOffNR   uint32 = 0
	SeccompOffArch uint32 = 4
	SeccompOffIP   uint32 = 8
	SeccompOffArgs uint32 = 16

	// Seccomp return action codes.
	SeccompRetKillProcess uint32 = 0x80000000
	SeccompRetTrap        uint32 = 0x00030000
	SeccompRetErrnoBase   uint32 = 0x00050000
	SeccompRetLog         uint32 = 0x7FFC0000
	SeccompRetAllow       uint32 = 0x7FFF0000
	SeccompRetActionMask  uint32 = 0x7FFF0000
	SeccompRetDataMask    uint32 = 0x0000FFFF
)

// BPFInsn mirrors Linux struct sock_filter (8 bytes, wire-compatible for
// prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &sock_fprog)).
type BPFInsn struct {
	// Code is the 16-bit classic BPF opcode.
	Code uint16 `json:"code"`
	// JT is the relative jump offset when the comparison succeeds.
	JT uint8 `json:"jt"`
	// JF is the relative jump offset when the comparison fails.
	JF uint8 `json:"jf"`
	// K is the 32-bit immediate operand or offset.
	K uint32 `json:"k"`
}

// BPFProgram mirrors Linux struct sock_fprog alongside metadata.
type BPFProgram struct {
	// Arch is the validated target architecture ("amd64" or "arm64").
	Arch string `json:"arch"`
	// AuditArch is the numeric Linux AUDIT_ARCH_* constant checked at insn 0.
	AuditArch uint32 `json:"auditArch"`
	// Insns holds the ordered classic BPF filter instructions.
	Insns []BPFInsn `json:"insns"`
}

// SeccompData mirrors Linux struct seccomp_data passed to the kernel BPF filter.
type SeccompData struct {
	NR                 uint32
	Arch               uint32
	InstructionPointer uint64
	Args               [6]uint64
}

// Digest computes the deterministic SHA-256 hex digest over the BPF instruction stream.
func (p BPFProgram) Digest() string {
	h := sha256.New()
	h.Write([]byte("hanzo-seccomp-bpf-v1\x00"))
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], p.AuditArch)
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(p.Insns)))
	h.Write(hdr[:])
	var buf [8]byte
	for _, in := range p.Insns {
		binary.LittleEndian.PutUint16(buf[0:2], in.Code)
		buf[2] = in.JT
		buf[3] = in.JF
		binary.LittleEndian.PutUint32(buf[4:8], in.K)
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func auditArchFor(arch string) (uint32, error) {
	switch arch {
	case "amd64", "":
		return AuditArchX86_64, nil
	case "arm64":
		return AuditArchAArch64, nil
	default:
		return 0, fmt.Errorf("unsupported bpf arch %q", arch)
	}
}

func seccompRetFor(effect RuleEffect, action EnforcementMode, denyErrno int) uint32 {
	if effect == EffectAllow {
		return SeccompRetAllow
	}
	if denyErrno <= 0 {
		denyErrno = ErrnoEPERM
	}
	switch action {
	case ModeAuditOnly:
		return SeccompRetLog
	case ModeEnforceDeny:
		return SeccompRetErrnoBase | (uint32(denyErrno) & SeccompRetDataMask)
	case ModeEnforceSigkill:
		return SeccompRetKillProcess
	default:
		// Fail closed if action is somehow unspecified.
		return SeccompRetErrnoBase | (uint32(denyErrno) & SeccompRetDataMask)
	}
}

// DecisionFromSeccompRet translates a Linux seccomp return code back into a
// sandboxpolicy Decision + errno/signal pair for VM parity verification.
func DecisionFromSeccompRet(ret uint32) (Decision, int) {
	if ret == SeccompRetKillProcess {
		return DecisionSigkill, SignalSIGKILL
	}
	switch ret & SeccompRetActionMask {
	case SeccompRetAllow:
		return DecisionAllow, 0
	case SeccompRetLog:
		return DecisionAuditAlert, 0
	case SeccompRetErrnoBase:
		return DecisionDeny, int(ret & SeccompRetDataMask)
	default:
		return DecisionDeny, ErrnoEPERM
	}
}

// CompileSeccompBPF compiles a validated CompiledPolicy's syscall rules into a
// kernel-ready Linux classic BPF (seccomp-BPF) program.
func CompileSeccompBPF(cp *CompiledPolicy) (BPFProgram, error) {
	if cp == nil {
		return BPFProgram{}, errors.New("nil compiled policy")
	}
	archID, err := auditArchFor(cp.Spec.Arch)
	if err != nil {
		return BPFProgram{}, err
	}

	insns := make([]BPFInsn, 0, 18+len(cp.allSyscalls)*3)

	// Prologue: enforce exact ABI architecture to prevent compat-32bit/x32
	// syscall number confusion attacks (fail-closed with KILL_PROCESS).
	insns = append(insns,
		BPFInsn{Code: OpLDWABS, K: SeccompOffArch},
		BPFInsn{Code: OpJEQK, JT: 1, JF: 0, K: archID},
		BPFInsn{Code: OpRETK, K: SeccompRetKillProcess},
		BPFInsn{Code: OpLDWABS, K: SeccompOffNR},
	)
	if archID == AuditArchX86_64 {
		// Reject x32 ABI syscalls (nr >= __X32_SYSCALL_BIT = 0x40000000): x32 shares
		// AUDIT_ARCH_X86_64 and would otherwise bypass native x86_64 deny rules when
		// DefaultSyscallAllow=true.
		insns = append(insns,
			BPFInsn{Code: OpJGEK, JT: 0, JF: 1, K: X32SyscallBit},
			BPFInsn{Code: OpRETK, K: SeccompRetKillProcess},
		)
	}

	for _, cr := range cp.allSyscalls {
		retCode := seccompRetFor(cr.rule.Effect, cr.action, cr.errno)
		if len(cr.rule.ArgFilters) == 0 {
			// Fast 2-instruction check; A remains equal to seccomp_data.nr on miss.
			insns = append(insns,
				BPFInsn{Code: OpJEQK, JT: 0, JF: 1, K: cr.num},
				BPFInsn{Code: OpRETK, K: retCode},
			)
			continue
		}

		block, err := compileArgFilteredSyscallBlock(cr.num, cr.rule.ArgFilters, retCode)
		if err != nil {
			return BPFProgram{}, fmt.Errorf("rule %q: %w", cr.rule.ID, err)
		}
		insns = append(insns, block...)
	}

	// Default epilogue verdict.
	defaultRet := SeccompRetAllow
	if !cp.Spec.DefaultSyscallAllow {
		defaultRet = seccompRetFor(EffectDeny, cp.Spec.Mode, ErrnoEPERM)
	}
	insns = append(insns, BPFInsn{Code: OpRETK, K: defaultRet})

	if len(insns) > BPFMaxInsns {
		return BPFProgram{}, fmt.Errorf("seccomp-bpf program has %d instructions (exceeds Linux BPF_MAXINSNS=%d)", len(insns), BPFMaxInsns)
	}
	return BPFProgram{
		Arch:      cp.Spec.Arch,
		AuditArch: archID,
		Insns:     insns,
	}, nil
}

// compileArgFilteredSyscallBlock emits a self-contained classic BPF sequence for
// one syscall rule with 1..6 argument filters. Entry invariant: A == nr.
// Exit invariant (on mismatch): A == nr.
func compileArgFilteredSyscallBlock(nr uint32, filters []SyscallArgFilter, retCode uint32) ([]BPFInsn, error) {
	// Build inner filter checks using symbolic jumps to:
	//   - failTarget: reload A = seccomp_data.nr, then fall through to next rule
	//   - nextFilter: proceed to next argument filter (or RET retCode after last)
	//
	// Because each filter emits <= 8 instructions and max 6 filters => <= 50 insns,
	// every relative jump offset fits safely inside uint8 (<= 255).
	type pendingJump struct {
		idx    int
		isTrue bool // patch JT if true, JF if false
		target int  // -1 = failReload, >= 0 = explicit instruction index inside body
	}

	var body []BPFInsn
	var patches []pendingJump

	for _, af := range filters {
		offLo := SeccompOffArgs + uint32(af.Index)*8
		offHi := offLo + 4
		valLo := uint32(af.Value & 0xFFFFFFFF)
		valHi := uint32(af.Value >> 32)

		var nextPatches []int // patch target to nextFilterIdx once emitted
		markPass := func(idx int, isTrue bool) {
			patches = append(patches, pendingJump{idx: idx, isTrue: isTrue, target: -2})
			nextPatches = append(nextPatches, len(patches)-1)
		}
		markFail := func(idx int, isTrue bool) {
			patches = append(patches, pendingJump{idx: idx, isTrue: isTrue, target: -1})
		}

		switch af.Op {
		case ArgOpEqual:
			body = append(body, BPFInsn{Code: OpLDWABS, K: offLo})
			body = append(body, BPFInsn{Code: OpJEQK, K: valLo})
			markFail(len(body)-1, false)
			body = append(body, BPFInsn{Code: OpLDWABS, K: offHi})
			body = append(body, BPFInsn{Code: OpJEQK, K: valHi})
			markFail(len(body)-1, false)

		case ArgOpNotEqual:
			// Pass immediately if lo != valLo; otherwise fail if hi == valHi.
			body = append(body, BPFInsn{Code: OpLDWABS, K: offLo})
			body = append(body, BPFInsn{Code: OpJEQK, K: valLo})
			markPass(len(body)-1, false)
			body = append(body, BPFInsn{Code: OpLDWABS, K: offHi})
			body = append(body, BPFInsn{Code: OpJEQK, K: valHi})
			markFail(len(body)-1, true)

		case ArgOpEqual32:
			// Compare only the kernel's low 32-bit int width (prevents upper-32-bit bypasses
			// such as (1<<32) | AF_NETLINK on 32-bit C int syscall arguments).
			body = append(body, BPFInsn{Code: OpLDWABS, K: offLo})
			body = append(body, BPFInsn{Code: OpJEQK, K: valLo})
			markFail(len(body)-1, false)

		case ArgOpNotEqual32:
			body = append(body, BPFInsn{Code: OpLDWABS, K: offLo})
			body = append(body, BPFInsn{Code: OpJEQK, K: valLo})
			markFail(len(body)-1, true)

		case ArgOpMaskAny:
			// (arg & mask) != 0 -> pass if (lo & valLo) != 0 OR (hi & valHi) != 0.
			body = append(body, BPFInsn{Code: OpLDWABS, K: offLo})
			body = append(body, BPFInsn{Code: OpJSETK, K: valLo})
			markPass(len(body)-1, true)
			body = append(body, BPFInsn{Code: OpLDWABS, K: offHi})
			body = append(body, BPFInsn{Code: OpJSETK, K: valHi})
			markFail(len(body)-1, false)

		case ArgOpMaskAll:
			// (arg & mask) == mask -> both lo and hi must have all mask bits set.
			body = append(body, BPFInsn{Code: OpLDWABS, K: offLo})
			body = append(body, BPFInsn{Code: OpANDK, K: valLo})
			body = append(body, BPFInsn{Code: OpJEQK, K: valLo})
			markFail(len(body)-1, false)
			body = append(body, BPFInsn{Code: OpLDWABS, K: offHi})
			body = append(body, BPFInsn{Code: OpANDK, K: valHi})
			body = append(body, BPFInsn{Code: OpJEQK, K: valHi})
			markFail(len(body)-1, false)

		case ArgOpGreaterEq:
			// arg >= val iff hi > valHi OR (hi == valHi AND lo >= valLo).
			body = append(body, BPFInsn{Code: OpLDWABS, K: offHi})
			body = append(body, BPFInsn{Code: OpJGTK, K: valHi})
			markPass(len(body)-1, true)
			body = append(body, BPFInsn{Code: OpJEQK, K: valHi})
			markFail(len(body)-1, false)
			body = append(body, BPFInsn{Code: OpLDWABS, K: offLo})
			body = append(body, BPFInsn{Code: OpJGEK, K: valLo})
			markFail(len(body)-1, false)

		case ArgOpLessEq:
			// arg <= val iff NOT(hi > valHi) AND (hi != valHi OR NOT(lo > valLo)).
			body = append(body, BPFInsn{Code: OpLDWABS, K: offHi})
			body = append(body, BPFInsn{Code: OpJGTK, K: valHi})
			markFail(len(body)-1, true)
			body = append(body, BPFInsn{Code: OpJEQK, K: valHi})
			markPass(len(body)-1, false)
			body = append(body, BPFInsn{Code: OpLDWABS, K: offLo})
			body = append(body, BPFInsn{Code: OpJGTK, K: valLo})
			markFail(len(body)-1, true)

		default:
			return nil, fmt.Errorf("unsupported arg op %q", af.Op)
		}

		nextFilterIdx := len(body)
		for _, pi := range nextPatches {
			patches[pi].target = nextFilterIdx
		}
	}

	// All argument filters matched: emit return verdict.
	body = append(body, BPFInsn{Code: OpRETK, K: retCode})
	// Failure target: reload A = seccomp_data.nr before falling through to next rule.
	failIdx := len(body)
	body = append(body, BPFInsn{Code: OpLDWABS, K: SeccompOffNR})

	// Resolve symbolic jump targets.
	for _, p := range patches {
		target := p.target
		if target == -1 {
			target = failIdx
		}
		delta := target - (p.idx + 1)
		if delta < 0 || delta > 255 {
			return nil, fmt.Errorf("bpf jump offset %d out of uint8 bounds", delta)
		}
		if p.isTrue {
			body[p.idx].JT = uint8(delta)
		} else {
			body[p.idx].JF = uint8(delta)
		}
	}

	// Prepend syscall number check. If nr != rule.nr, skip the entire body
	// including the reload instruction (A already holds nr).
	if len(body) > 255 {
		return nil, fmt.Errorf("arg filter block length %d exceeds uint8 jump limit", len(body))
	}
	out := make([]BPFInsn, 0, 1+len(body))
	out = append(out, BPFInsn{Code: OpJEQK, JT: 0, JF: uint8(len(body)), K: nr})
	out = append(out, body...)
	return out, nil
}

// EvalBPF deterministically executes a compiled classic BPF seccomp program
// against a SeccompData packet, mirroring Linux kernel __seccomp_filter execution
// for cross-platform CI verification and parity testing.
func EvalBPF(prog BPFProgram, data SeccompData) (uint32, error) {
	if len(prog.Insns) == 0 {
		return 0, errors.New("bpf: empty program")
	}
	if len(prog.Insns) > BPFMaxInsns {
		return 0, fmt.Errorf("bpf: program length %d exceeds %d", len(prog.Insns), BPFMaxInsns)
	}

	var pkt [64]byte
	binary.LittleEndian.PutUint32(pkt[0:4], data.NR)
	binary.LittleEndian.PutUint32(pkt[4:8], data.Arch)
	binary.LittleEndian.PutUint64(pkt[8:16], data.InstructionPointer)
	for i := 0; i < 6; i++ {
		binary.LittleEndian.PutUint64(pkt[16+i*8:24+i*8], data.Args[i])
	}

	var acc uint32
	for pc := 0; pc < len(prog.Insns); {
		in := prog.Insns[pc]
		switch in.Code {
		case OpLDWABS:
			if in.K > uint32(len(pkt)-4) {
				return 0, fmt.Errorf("bpf: out-of-bounds load offset %d at pc=%d", in.K, pc)
			}
			acc = binary.LittleEndian.Uint32(pkt[in.K : in.K+4])
			pc++
		case OpANDK:
			acc &= in.K
			pc++
		case OpJA:
			pc += 1 + int(in.K)
		case OpJEQK:
			if acc == in.K {
				pc += 1 + int(in.JT)
			} else {
				pc += 1 + int(in.JF)
			}
		case OpJGTK:
			if acc > in.K {
				pc += 1 + int(in.JT)
			} else {
				pc += 1 + int(in.JF)
			}
		case OpJGEK:
			if acc >= in.K {
				pc += 1 + int(in.JT)
			} else {
				pc += 1 + int(in.JF)
			}
		case OpJSETK:
			if (acc & in.K) != 0 {
				pc += 1 + int(in.JT)
			} else {
				pc += 1 + int(in.JF)
			}
		case OpRETK:
			return in.K, nil
		default:
			return 0, fmt.Errorf("bpf: unsupported opcode 0x%04x at pc=%d", in.Code, pc)
		}
	}
	return 0, errors.New("bpf: program terminated without RET")
}
