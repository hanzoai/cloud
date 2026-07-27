package cloud

import (
	"strings"

	"github.com/hanzoai/cloud/internal/org"
	luxlog "github.com/luxfi/log"
)

// Peers discovers the live sibling pods matching selector, and the namespace
// watched. An error means "not in a cluster". apps/ installs the Kubernetes
// implementation; nil means static membership, which always works.
//
// A static peer set makes ha.Owner elect pods that are draining or gone, and
// the shard router forwards to a dead pod. A ready-gated live set drops a pod
// the moment it starts terminating.
var Peers func(selector, port string) (org.Source, string, error)

func membershipSource(staticPeers []org.Member, selector, port string, log luxlog.Logger) org.Source {
	static := org.StaticSource(staticPeers...)
	if strings.TrimSpace(selector) == "" || Peers == nil {
		return static
	}
	src, ns, err := Peers(selector, port)
	if err != nil {
		// Configured but unreachable: keep serving on the static set, loudly.
		if log != nil {
			log.Warn("live membership unavailable — using static peer set", "selector", selector, "err", err)
		}
		return static
	}
	if log != nil {
		log.Info("live membership enabled", "namespace", ns, "selector", selector)
	}
	return src
}

// httpPortOf extracts the port from a listen address (":8080" → "8080"). A
// value with no colon is already a bare port.
func httpPortOf(listenAddr string) string {
	listenAddr = strings.TrimSpace(listenAddr)
	if i := strings.LastIndex(listenAddr, ":"); i >= 0 {
		return listenAddr[i+1:]
	}
	return listenAddr
}
