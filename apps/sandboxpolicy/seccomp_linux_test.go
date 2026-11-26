// SPDX-License-Identifier: Apache-2.0

//go:build linux && amd64

package sandboxpolicy

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

const (
	linuxPrSetNoNewPrivs       = 38
	linuxSeccompSetModeFilter  = 1
	linuxSeccompFilterFlagSync = 1
	linuxSysSeccomp            = 317
	linuxSysClone3             = 435
)

type linuxSockFprog struct {
	len    uint16
	pad    [6]byte
	filter *BPFInsn
}

func installSeccompBPF(prog BPFProgram) error {
	if len(prog.Insns) == 0 || len(prog.Insns) > BPFMaxInsns {
		return fmt.Errorf("invalid bpf program length %d", len(prog.Insns))
	}
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, linuxPrSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_NO_NEW_PRIVS): %w", errno)
	}
	fprog := linuxSockFprog{
		len:    uint16(len(prog.Insns)),
		filter: &prog.Insns[0],
	}
	if _, _, errno := syscall.Syscall(
		linuxSysSeccomp,
		linuxSeccompSetModeFilter,
		linuxSeccompFilterFlagSync,
		uintptr(unsafe.Pointer(&fprog)),
	); errno != 0 {
		return fmt.Errorf("seccomp(SECCOMP_SET_MODE_FILTER|TSYNC): %w", errno)
	}
	return nil
}

// TestKernelSeccompSubprocessHelper is re-executed in an isolated child process
// by TestKernelAttachedSeccompSubprocessExecution so the seccomp filter never
// restricts the parent `go test` runner.
func TestKernelSeccompSubprocessHelper(t *testing.T) {
	mode := os.Getenv("SANDBOXPOLICY_SECCOMP_HELPER")
	if mode == "" {
		return
	}

	cp, err := CompilePolicy(DefaultVisorSandboxPolicy(ModeEnforceSigkill))
	if err != nil {
		fmt.Fprintf(os.Stderr, "compile policy: %v\n", err)
		os.Exit(2)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := installSeccompBPF(cp.BPF); err != nil {
		fmt.Fprintf(os.Stderr, "install seccomp: %v\n", err)
		os.Exit(3)
	}

	switch mode {
	case "exec":
		bin := os.Getenv("SANDBOXPOLICY_EXEC_BIN")
		args := strings.Split(os.Getenv("SANDBOXPOLICY_EXEC_ARGS"), "\x1f")
		if err := syscall.Exec(bin, args, os.Environ()); err != nil {
			fmt.Fprintf(os.Stderr, "exec %s: %v\n", bin, err)
			os.Exit(4)
		}

	case "bypass-checks":
		// 1. Plain AF_NETLINK must return EPERM.
		if fd, _, errno := syscall.RawSyscall(syscall.SYS_SOCKET, uintptr(AFNetlink), syscall.SOCK_RAW, 0); errno != syscall.EPERM {
			if errno == 0 {
				_ = syscall.Close(int(fd))
			}
			fmt.Fprintf(os.Stderr, "socket(AF_NETLINK): got errno=%v, want EPERM\n", errno)
			os.Exit(10)
		}
		// 2. Upper-bit spoofed (1<<32)|AF_NETLINK must ALSO return EPERM (Codex Finding #2).
		if fd, _, errno := syscall.RawSyscall(syscall.SYS_SOCKET, uintptr((uint64(1)<<32)|AFNetlink), syscall.SOCK_RAW, 0); errno != syscall.EPERM {
			if errno == 0 {
				_ = syscall.Close(int(fd))
			}
			fmt.Fprintf(os.Stderr, "socket((1<<32)|AF_NETLINK): got errno=%v, want EPERM\n", errno)
			os.Exit(11)
		}
		// 3. Upper-bit spoofed (1<<32)|AF_PACKET must ALSO return EPERM.
		if fd, _, errno := syscall.RawSyscall(syscall.SYS_SOCKET, uintptr((uint64(1)<<32)|AFPacket), syscall.SOCK_RAW, 0); errno != syscall.EPERM {
			if errno == 0 {
				_ = syscall.Close(int(fd))
			}
			fmt.Fprintf(os.Stderr, "socket((1<<32)|AF_PACKET): got errno=%v, want EPERM\n", errno)
			os.Exit(12)
		}
		// 4. clone3 must return ENOSYS so runtimes fall back to argument-inspected clone (Codex Finding #3).
		if _, _, errno := syscall.RawSyscall(linuxSysClone3, 0, 0, 0); errno != syscall.ENOSYS {
			fmt.Fprintf(os.Stderr, "clone3: got errno=%v, want ENOSYS\n", errno)
			os.Exit(13)
		}
		// 5. Benign AF_INET socket + ephemeral 127.0.0.1:0 bind must still succeed.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fmt.Fprintf(os.Stderr, "net.Listen(127.0.0.1:0): %v\n", err)
			os.Exit(14)
		}
		_ = ln.Close()
		fmt.Println("bypass-checks-ok")
		os.Exit(0)

	case "kill-ptrace":
		_, _, _ = syscall.RawSyscall(syscall.SYS_PTRACE, 0, 0, 0)
		fmt.Fprintln(os.Stderr, "ptrace was NOT killed by seccomp!")
		os.Exit(20)
	}
}

// TestKernelAttachedSeccompSubprocessExecution attaches the compiled DefaultVisorSandboxPolicy
// (enforce_sigkill) seccomp-BPF filter directly to the Linux kernel in child subprocesses and
// verifies real execution of glibc tools (true, git, sh), a freshly compiled Go binary,
// kernel enforcement of eq32 socket-family denies, clone3 ENOSYS fallback, and ptrace kill.
func TestKernelAttachedSeccompSubprocessExecution(t *testing.T) {
	runHelper := func(helperMode, bin string, argv []string) ([]byte, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestKernelSeccompSubprocessHelper$")
		env := append(os.Environ(), "SANDBOXPOLICY_SECCOMP_HELPER="+helperMode)
		if bin != "" {
			env = append(env,
				"SANDBOXPOLICY_EXEC_BIN="+bin,
				"SANDBOXPOLICY_EXEC_ARGS="+strings.Join(argv, "\x1f"),
			)
		}
		cmd.Env = env
		return cmd.CombinedOutput()
	}

	// 1. Real glibc subprocesses (exercise rseq, arch_prctl, mmap, openat, etc.).
	if truePath, err := exec.LookPath("true"); err == nil {
		if out, err := runHelper("exec", truePath, []string{truePath}); err != nil {
			t.Fatalf("seccomp-confined /usr/bin/true failed: %v\noutput: %s", err, string(out))
		}
	}
	if gitPath, err := exec.LookPath("git"); err == nil {
		out, err := runHelper("exec", gitPath, []string{gitPath, "--version"})
		if err != nil {
			t.Fatalf("seccomp-confined git --version failed: %v\noutput: %s", err, string(out))
		}
		if !strings.Contains(string(out), "git version") {
			t.Fatalf("unexpected git --version output: %q", string(out))
		}
	}
	if shPath, err := exec.LookPath("sh"); err == nil {
		out, err := runHelper("exec", shPath, []string{shPath, "-c", "echo seccomp-sh-ok"})
		if err != nil {
			t.Fatalf("seccomp-confined sh failed: %v\noutput: %s", err, string(out))
		}
		if !strings.Contains(string(out), "seccomp-sh-ok") {
			t.Fatalf("unexpected sh output: %q", string(out))
		}
	}

	// 2. Real freshly compiled Go binary (exercises sched_getaffinity, clone/futex, goroutines, net.Listen("tcp", "127.0.0.1:0")).
	if goPath, err := exec.LookPath("go"); err == nil {
		tmpDir := t.TempDir()
		srcPath := filepath.Join(tmpDir, "probe.go")
		binPath := filepath.Join(tmpDir, "probe")
		src := `package main

import (
	"fmt"
	"net"
	"sync"
)

func main() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		_ = ln.Close()
	}()
	wg.Wait()
	fmt.Println("seccomp-go-ok")
}
`
		if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
			t.Fatalf("write probe.go: %v", err)
		}
		buildCmd := exec.Command(goPath, "build", "-o", binPath, srcPath)
		if out, err := buildCmd.CombinedOutput(); err != nil {
			t.Fatalf("go build probe: %v\n%s", err, string(out))
		}
		out, err := runHelper("exec", binPath, []string{binPath})
		if err != nil {
			t.Fatalf("seccomp-confined Go binary failed: %v\noutput: %s", err, string(out))
		}
		if !strings.Contains(string(out), "seccomp-go-ok") {
			t.Fatalf("unexpected Go probe output: %q", string(out))
		}
	}

	// 3. Real kernel verification of eq32 socket-family denies (including (1<<32)|AF_NETLINK)
	//    and clone3 ENOSYS fallback.
	if out, err := runHelper("bypass-checks", "", nil); err != nil {
		t.Fatalf("kernel seccomp bypass-checks failed: %v\noutput: %s", err, string(out))
	} else if !strings.Contains(string(out), "bypass-checks-ok") {
		t.Fatalf("unexpected bypass-checks output: %q", string(out))
	}

	// 4. Real kernel verification of SECCOMP_RET_KILL_PROCESS on ptrace.
	out, err := runHelper("kill-ptrace", "", nil)
	if err == nil {
		t.Fatalf("expected ptrace subprocess to be killed by seccomp, exited 0: %s", string(out))
	}
	var exitErr *exec.ExitError
	if ws, ok := func() (syscall.WaitStatus, bool) {
		if ee, ok := err.(*exec.ExitError); ok {
			exitErr = ee
			if s, ok := ee.Sys().(syscall.WaitStatus); ok {
				return s, true
			}
		}
		return 0, false
	}(); !ok || !ws.Signaled() {
		t.Fatalf("expected ptrace to terminate via kernel signal, got err=%v exitErr=%v out=%s", err, exitErr, string(out))
	}
}
