// WSL2 detection for arcd.
//
// arcd inside WSL2 is a Linux process by build target (GOOS=linux) but the
// host kernel and Windows side are reachable through interop. Two things
// matter to the daemon:
//
//  1. Service install uses systemd-user, NOT the Windows SCM. The Windows
//     SCM is unreachable from inside the WSL Linux namespace, and even if
//     it were reachable, registering a Windows service that runs arcd-linux
//     under wsl.exe is brittle. systemd-user works exactly like a native
//     Linux box (modulo systemd-genie-ish caveats — see svcInstall fallback).
//
//  2. JIT label routing must distinguish a WSL2 host from native Linux.
//     The same physical box (e.g. evo) can run BOTH a native Windows arcd
//     (windows/amd64, labels include `windows`) AND a WSL2 arcd
//     (linux/amd64, labels include `wsl`). Workflows targeting
//     `runs-on: [self-hosted, evo, windows, x64, hip]` must land on the
//     Windows daemon; `runs-on: [self-hosted, evo, linux, x64, wsl, hip-rocm-wsl]`
//     must land on the WSL2 daemon. The two daemons coexist because their
//     label sets are distinct.
//
// Detection signals (any one is sufficient — we union them for robustness):
//
//   - $WSL_DISTRO_NAME set in env (set automatically inside any WSL distro)
//   - /proc/sys/kernel/osrelease contains "WSL" or "microsoft" (case-insens)
//   - /proc/version contains "microsoft" (case-insensitive)
//
// On non-Linux GOOS this file compiles to no-ops via the build tag below.
package runner

import (
	"os"
	"strings"
)

// IsWSL reports whether the current process is running inside a WSL2 distro.
//
// Lazy: we read /proc twice the first time, then cache nothing — the result
// can't change for a process's lifetime, but each call is two os.ReadFile
// of ~30 bytes total, so caching would be premature.
func IsWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	// /proc/sys/kernel/osrelease is the canonical kernel-version signal.
	// Inside WSL2 it looks like:
	//   6.6.114.1-microsoft-standard-WSL2
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		s := strings.ToLower(string(b))
		if strings.Contains(s, "wsl") || strings.Contains(s, "microsoft") {
			return true
		}
	}
	// /proc/version is the fallback — older WSL builds don't put "WSL"
	// into osrelease but always carry "Microsoft" in /proc/version.
	if b, err := os.ReadFile("/proc/version"); err == nil {
		if strings.Contains(strings.ToLower(string(b)), "microsoft") {
			return true
		}
	}
	return false
}

// AugmentLabelsWithWSL appends "wsl" to the host's effective labels when
// running inside WSL2. Caller passes the labels parsed from config.yaml
// and receives them back with the marker added if appropriate. No-op on
// native Linux, macOS, or Windows.
//
// Idempotent: if "wsl" is already present (case-insensitive), nothing is
// added. The caller's slice order is preserved; the new marker, if any,
// is appended at the end.
func AugmentLabelsWithWSL(labels []string) []string {
	if !IsWSL() {
		return labels
	}
	for _, l := range labels {
		if strings.EqualFold(l, "wsl") {
			return labels
		}
	}
	return append(labels, "wsl")
}
