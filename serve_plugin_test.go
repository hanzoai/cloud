// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"reflect"
	"slices"
	"testing"

	"github.com/zap-proto/zip"
)

// ZIP_ADDR is the entire plugin contract: the host created that socket, told
// the child about it, and blocks until it accepts. A child that serves cfg's
// ports instead is never seen to come up — and because apps.Wire() loads
// plugins lazily, the host does not fail at boot but 502s the first real
// request to the prefix. RED before Serve honoured zip.Addr.
func TestListenOn_PluginServesTheSocketItWasGiven(t *testing.T) {
	sock := t.TempDir() + "/wallets.sock"
	t.Setenv(zip.AddrEnv, sock)

	t.Setenv(runDirEnv, t.TempDir())
	addrs, ops := listenOn(testListenCfg(), []MountSpec{{Name: "wallets"}})
	// The host's socket comes FIRST: it blocks in waitListening on that one, and a
	// peer socket bound ahead of it would let the host see "up" before the address
	// it actually waits on accepts.
	if len(addrs) == 0 || addrs[0] != sock {
		t.Fatalf("addrs = %q, want the host socket %q first — that is the one it waits on", addrs, sock)
	}
	// The peer socket is NOT here: rpc.Listen binds it (Serve), and listing it
	// again would double-bind the same path. A plugin serves the host's socket
	// and nothing else.
	if want := PeerSocket("wallets"); slices.Contains(addrs, want) {
		t.Fatalf("addrs = %q double-binds the peer socket %q — rpc.Listen owns it", addrs, want)
	}
	// Second-order bug: one ops port, N children. Binding it here means every
	// plugin after the first dies on "address already in use", and the host's
	// own probes answer from whichever child won the race.
	if ops != "" {
		t.Fatalf("plugin bound the host's ops port %q", ops)
	}
}

// The common path. A regression here breaks production, so it is pinned
// separately from the plugin case.
func TestListenOn_StandaloneBindsTheConfiguredPorts(t *testing.T) {
	t.Setenv(zip.AddrEnv, "") // zip.Addr treats empty as unset — started directly

	t.Setenv(runDirEnv, t.TempDir())
	addrs, ops := listenOn(testListenCfg(), []MountSpec{{Name: "wallets"}})
	// The peer UDS is absent by design: rpc.Listen binds it in Serve. What is
	// left here is the machine TCP (ZAP) and HTTP for the edge, which also
	// carries WS/SSE.
	want := []string{":9653", "http://:8080"}
	if !reflect.DeepEqual(addrs, want) {
		t.Fatalf("addrs = %q, want %q", addrs, want)
	}
	if ops != ":9090" {
		t.Fatalf("ops = %q, want :9090", ops)
	}
}

func testListenCfg() *Config {
	return &Config{ListenAddr: ":8080", ZAPListenAddr: ":9653", HealthListenAddr: ":9090"}
}
