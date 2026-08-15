// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package o11y

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The in-pod ear is a SOCKET, on the same directory the call plane uses.
//
// Worth pinning because the failure is silent in both directions: before the
// receiver could take a path it bound a TCP port instead and said nothing, and
// if this ever stops binding, the senders simply fall back to the TCP ear and
// nothing reports that the cheap path went away.
func TestSocketEarBindsBesideTheCallPlane(t *testing.T) {
	// A SHORT directory, deliberately. A unix socket's path is capped by the
	// kernel — 104 bytes on darwin/BSD, 108 on Linux — and t.TempDir() on macOS
	// is already ~90 of them, so binding under it fails with the unhelpful
	// "invalid argument". Production is /var/lib/cloud/run/o11y-spans.sock at 34
	// bytes, nowhere near it; a test that used the long path would be measuring
	// the temp directory rather than the code.
	dir, err := os.MkdirTemp("/tmp", "o11y")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	t.Setenv("ZIP_RUNTIME_DIR", dir)

	dir = runtimeDir()
	if dir == "" {
		t.Fatal("no runtime dir with ZIP_RUNTIME_DIR set")
	}

	for _, name := range []string{planeSpanSocketName, planeLogSocketName} {
		// The receiver binds it; here we only prove the ADDRESS is a path the
		// receiver will read as unix, and that the directory is ours to write.
		path := filepath.Join(dir, name)
		if !filepath.IsAbs(path) {
			t.Fatalf("%s is not an absolute path, so zap would read it as TCP", path)
		}
		l, err := net.Listen("unix", path)
		if err != nil {
			t.Fatalf("cannot bind %s: %v", path, err)
		}
		fi, err := os.Stat(path)
		if err != nil || fi.Mode()&os.ModeSocket == 0 {
			t.Fatalf("%s is not a socket", path)
		}
		_ = l.Close()
	}
}

// No runtime directory means no socket and no error: every TCP ear still works,
// which is what a dev process with nowhere to write should get.
func TestNoRuntimeDirIsNotAFailure(t *testing.T) {
	// A path that cannot be created — MkdirAll fails, so runtimeDir answers "".
	t.Setenv("ZIP_RUNTIME_DIR", "/proc/nonexistent/cannot/create")
	if dir := runtimeDir(); dir != "" {
		t.Fatalf("expected no runtime dir, got %q", dir)
	}
}

// The two ears are not the same ear. A socket path must never be one of the
// TCP addresses, or the senders that cannot share a filesystem lose their door.
func TestTheSocketDoesNotReplaceTheTCPEar(t *testing.T) {
	for _, tcp := range []string{planeSpanListen, planeLogListen} {
		if filepath.IsAbs(tcp) {
			t.Fatalf("%s became a path; off-pod senders would have no address", tcp)
		}
	}
}
