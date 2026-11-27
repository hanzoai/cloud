package host

import (
	"time"

	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
)

// The host answers the framework's questions: its program, the process that serves
// it, and the middleware, metrics and billing the framework's own steps end in.
// Every plugin main and cmd/cloud links this package, so the framework always has
// them (sdk/hooks.go).
func init() {
	cloud.SetHooks(cloud.Hooks{
		App:       buildApp,
		Listen:    listenAll,
		BuildDeps: buildDeps,

		PlatformPush:    platformPush,
		PlatformRelease: platformRelease,
		PlatformJob:     platformJob,
		X402Fee:         x402Fee,

		Declared:  declared,
		PeerCount: func(spec string) int { return len(parsePeers(spec)) },
		PeerHas:   func(spec, id string) bool { return peersContain(parsePeers(spec), id) },

		ReportFault:     reportFault,
		Ceiling:         Ceiling,
		Toll:            Toll,
		IsDoor:          isDoor,
		Nobody:          nobody,
		ObserveRequest:  func(product, org string, status int, d time.Duration) { observeRequest(product, org, status, d) },
		ProductFromPath: productFromPath,
		Billable:        Billable,
		PlaneServed:     planeServed,
		ObserveDebit: func(product string, u meter.Usage, res *meter.RecordResult, err error) {
			observeDebit(product, u, res, err)
		},
		ObserveGate: observeGate,
	})
}
