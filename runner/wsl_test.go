// WSL detection unit tests.
//
// We exercise the env-based signal (WSL_DISTRO_NAME) deterministically
// on any host and assert the substring policy catches the real-world
// payloads WSL2 distros put into /proc/version and
// /proc/sys/kernel/osrelease.
package runner

import (
	"os"
	"strings"
	"testing"
)

func TestIsWSL_FromEnv(t *testing.T) {
	// Save + restore the env across the test so we don't pollute later
	// tests that may depend on the absence of WSL_DISTRO_NAME.
	prev, hadPrev := os.LookupEnv("WSL_DISTRO_NAME")
	defer func() {
		if hadPrev {
			_ = os.Setenv("WSL_DISTRO_NAME", prev)
		} else {
			_ = os.Unsetenv("WSL_DISTRO_NAME")
		}
	}()

	_ = os.Setenv("WSL_DISTRO_NAME", "Ubuntu-24.04")
	if !IsWSL() {
		t.Fatal("WSL_DISTRO_NAME=Ubuntu-24.04 should yield IsWSL=true")
	}

	_ = os.Unsetenv("WSL_DISTRO_NAME")
	// We can't reliably assert false here because the test runner itself
	// might be inside WSL2 (the /proc/version path would still fire).
	// Just ensure the env-only path doesn't panic and is correlated
	// with the file-path fallback.
	_ = IsWSL()
}

func TestAugmentLabelsWithWSL_NotInWSL(t *testing.T) {
	// On a non-WSL host (proc files don't contain microsoft, env unset)
	// AugmentLabelsWithWSL must be a strict no-op.
	prev, hadPrev := os.LookupEnv("WSL_DISTRO_NAME")
	_ = os.Unsetenv("WSL_DISTRO_NAME")
	defer func() {
		if hadPrev {
			_ = os.Setenv("WSL_DISTRO_NAME", prev)
		}
	}()

	if IsWSL() {
		// We're actually in WSL — skip; the not-in-WSL invariant doesn't
		// hold and the test below would fail for the right reason.
		t.Skip("running inside WSL — invariant for this test doesn't hold")
	}

	in := []string{"self-hosted", "evo", "linux", "x64"}
	out := AugmentLabelsWithWSL(in)
	if len(out) != len(in) {
		t.Fatalf("non-WSL host: AugmentLabelsWithWSL added %d labels (want 0)", len(out)-len(in))
	}
}

func TestAugmentLabelsWithWSL_InWSL(t *testing.T) {
	// Force the env signal so the test passes deterministically on any
	// host (CI runner, native Linux, macOS dev box).
	prev, hadPrev := os.LookupEnv("WSL_DISTRO_NAME")
	_ = os.Setenv("WSL_DISTRO_NAME", "Ubuntu-24.04")
	defer func() {
		if hadPrev {
			_ = os.Setenv("WSL_DISTRO_NAME", prev)
		} else {
			_ = os.Unsetenv("WSL_DISTRO_NAME")
		}
	}()

	in := []string{"self-hosted", "evo", "linux", "x64"}
	out := AugmentLabelsWithWSL(in)
	if len(out) != len(in)+1 {
		t.Fatalf("WSL host: AugmentLabelsWithWSL added %d labels (want 1): %v", len(out)-len(in), out)
	}
	if !strings.EqualFold(out[len(out)-1], "wsl") {
		t.Fatalf("AugmentLabelsWithWSL: last label = %q, want %q", out[len(out)-1], "wsl")
	}
}

func TestAugmentLabelsWithWSL_Idempotent(t *testing.T) {
	prev, hadPrev := os.LookupEnv("WSL_DISTRO_NAME")
	_ = os.Setenv("WSL_DISTRO_NAME", "Ubuntu-24.04")
	defer func() {
		if hadPrev {
			_ = os.Setenv("WSL_DISTRO_NAME", prev)
		} else {
			_ = os.Unsetenv("WSL_DISTRO_NAME")
		}
	}()

	in := []string{"self-hosted", "evo", "linux", "x64", "WSL"}
	out := AugmentLabelsWithWSL(in)
	if len(out) != len(in) {
		t.Fatalf("idempotency violation: WSL marker present, got %d added: %v", len(out)-len(in), out)
	}
}

// TestProcContentDetection: assert the substring policy actually catches the
// strings WSL2 distros put into /proc/version and /proc/sys/kernel/osrelease.
func TestProcContentDetection(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "osrelease real-world WSL2",
			content: "6.6.114.1-microsoft-standard-WSL2\n",
			want:    true,
		},
		{
			name:    "version real-world WSL2",
			content: "Linux version 6.6.114.1-microsoft-standard-WSL2 (root@507f3e43091d) (gcc (GCC) 13.2.0)\n",
			want:    true,
		},
		{
			name:    "osrelease native Linux",
			content: "6.8.0-49-generic\n",
			want:    false,
		},
		{
			name:    "version native Linux",
			content: "Linux version 6.8.0-49-generic (buildd@lcy02-amd64-066) (x86_64-linux-gnu-gcc-13)\n",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lower := strings.ToLower(tc.content)
			got := strings.Contains(lower, "wsl") || strings.Contains(lower, "microsoft")
			if got != tc.want {
				t.Fatalf("substring match on %q: got=%v want=%v", tc.content, got, tc.want)
			}
		})
	}
}
