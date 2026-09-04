// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sock"
	"github.com/zap-proto/zip"
)

// ZIP_ADDR is the entire plugin contract: the host created that socket, told
// the child about it, and blocks until it accepts. A child that serves cfg's
// ports instead is never seen to come up — and because apps.Wire() loads
// plugins lazily, the host does not fail at boot but 502s the first real
// request to the prefix. RED before Serve honoured zip.Addr.
func TestListenOn_PluginServesTheSocketItWasGiven(t *testing.T) {
	sock := sock.Dir(t) + "/wallets.sock"
	t.Setenv(zip.AddrEnv, sock)

	addrs, ops := listenOn(testListenCfg())
	// THE SOCKET IT WAS GIVEN, AND ONLY THAT.
	//
	// A plugin does listen twice — the ZAP socket carries framed ops, and an
	// upgrade cannot cross that framing, so there is a plain-HTTP leg beside it.
	// But zip makes that leg itself: plain(addr) is addr+".http" for a unix
	// address and plainSibling serves it (transport.go:375). Naming it here as
	// well asked for the same listener twice, and the second bind failed
	// "address already in use" — taking pubsub down, then kafka and amqp
	// fail-closed behind it, then the host.
	//
	// So this asserts the ABSENCE. The derivation has one home, and a test that
	// wanted both names would be a second place to keep it in step — which is the
	// thing this whole function exists to avoid.
	want := []string{sock}
	if !reflect.DeepEqual(addrs, want) {
		t.Fatalf("addrs = %q, want %q — the host waits on that socket and nothing it was not told about", addrs, want)
	}
	for _, a := range addrs {
		if strings.HasSuffix(a, ".http") {
			t.Errorf("addrs names %q; zip derives the plain leg, so naming it binds it twice", a)
		}
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

	addrs, ops := listenOn(testListenCfg())
	// The app's own socket is NOT here: rpc.Listen serves the internal plane on it
	// with the zaprpc framing, and a second server on the same path answers those
	// callers in a framing they cannot parse.
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
