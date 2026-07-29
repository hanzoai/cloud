package cloud

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/apps/metering"
	luxlog "github.com/luxfi/log"
)

// The gate's two failure shapes need OPPOSITE answers, and getting them the
// wrong way round is how a fleet either 503s a deployment that never billed or
// hands out free work in one that does. Both directions are pinned here.

func unconfiguredMeter(t *testing.T) *ResourceMeter {
	t.Helper()
	m, err := metering.New(metering.Config{}) // empty BaseURL ⇒ !Enabled()
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return &ResourceMeter{m: m, provider: "test", log: luxlog.NewNoOpLogger()}
}

// NO SOCKET: this deployment has no money plane. The gate is inert, which is
// what "billing not configured" always meant — refusing would break a
// deployment that never intended to bill.
func TestGate_NoMoneyPlaneIsInert(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	if err := unconfiguredMeter(t).Gate(context.Background(), "acme", "", false, "fn", 500); err != nil {
		t.Fatalf("a deployment with no money plane refused a priced act: %v", err)
	}
}

// SOCKET PRESENT, NOTHING ANSWERING: commerce runs here and is not reachable.
// That is a fault, and the gate must FAIL CLOSED — this is the exact shape that
// once turned every priced act free, silently.
func TestGate_UnreachableBillerFailsClosed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZIP_RUNTIME_DIR", dir)

	// A real socket that accepts and says nothing: present, but no answer.
	ln, err := net.Listen("unix", filepath.Join(dir, "commerce.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close() // accept, then hang up without answering
		}
	}()

	if !PeerPresent("commerce") {
		t.Fatal("a live socket was not seen as present")
	}
	err = unconfiguredMeter(t).Gate(context.Background(), "acme", "", false, "fn", 500)
	if err == nil {
		t.Fatal("an unreachable biller ALLOWED a priced act — this is the free-work hole")
	}
}

// A plain file where the socket should be is not a peer. Presence must mean a
// socket, not merely a name on disk.
func TestPeerPresent_OnlyASocketCounts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "commerce.sock"), []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if PeerPresent("commerce") {
		t.Fatal("a regular file was read as a live peer")
	}
}
