// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package manifest

import (
	"runtime"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/pin"
)

const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func lockFor(t *testing.T, name, platform string) pin.File {
	t.Helper()
	f, err := pin.Parse(strings.NewReader(name + " hanzoai/" + name + " v1.2.3\n  " + platform + " https://s3.hanzo.ai/p/" + name + " " + digest + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func here() string { return runtime.GOOS + "/" + runtime.GOARCH }

func TestPinned_ResolvesURLAndSumForThisPlatform(t *testing.T) {
	p, ok := App{Name: "dns"}.Locked(lockFor(t, "dns", here()))
	if !ok {
		t.Fatal("expected dns to resolve from the lock")
	}
	if p.URL != "https://s3.hanzo.ai/p/dns" || p.Sum != strings.TrimPrefix(digest, "sha256:") {
		t.Fatalf("url/sum = %q/%q", p.URL, p.Sum)
	}
	if p.Name != "dns" || !p.Lazy || len(p.Args) != 0 {
		t.Fatalf("name/lazy/args = %q/%v/%v", p.Name, p.Lazy, p.Args)
	}
}

func TestPinned_EagerAppIsNotLazyAndNeverIdles(t *testing.T) {
	p, ok := App{Name: "o11y", Eager: true}.Locked(lockFor(t, "o11y", here()))
	if !ok || p.Lazy || p.IdleAfter != 0 {
		t.Fatalf("eager app: ok=%v lazy=%v idle=%s", ok, p.Lazy, p.IdleAfter)
	}
}

func TestPinned_WrongPlatformIsNotAMatch(t *testing.T) {
	if _, ok := (App{Name: "dns"}).Locked(lockFor(t, "dns", "plan9/mips")); ok {
		t.Fatal("resolved a plan9/mips artifact on this host")
	}
}

// An app the lock does not name does not borrow another's artifact.
func TestPinned_NoStanzaNoBinary(t *testing.T) {
	if p, ok := (App{Name: "dns"}).Locked(lockFor(t, "cloud", here())); ok {
		t.Fatalf("dns resolved to %q", p.URL)
	}
}

// The committed lock is empty until a plugin repo releases: every app then
// resolves from disk, exactly as before.
func TestRemote_CommittedLockPinsNothingYet(t *testing.T) {
	f, err := pin.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range f.Entries {
		if _, ok := (App{Name: e.Name}).remote(); !ok && e.Binaries[here()].URL != "" {
			t.Fatalf("%s is pinned for this platform and does not resolve", e.Name)
		}
	}
}

// Explicit operator overrides outrank the lock: naming a binary and silently
// fetching a different one would be a lie.
func TestPlugin_ExplicitBinBeatsLock(t *testing.T) {
	t.Setenv("CLOUD_DNS_BIN", "/opt/mine/dns")
	if p := (App{Name: "dns"}).Plugin(); p.Path != "/opt/mine/dns" || p.URL != "" {
		t.Fatalf("explicit BIN lost: path=%q url=%q", p.Path, p.URL)
	}
}

func TestPlugin_AddrBeatsLock(t *testing.T) {
	t.Setenv("CLOUD_DNS_ADDR", "10.0.0.9:9000")
	if p := (App{Name: "dns"}).Plugin(); p.Addr != "10.0.0.9:9000" || p.URL != "" {
		t.Fatalf("ADDR lost: addr=%q url=%q", p.Addr, p.URL)
	}
}
