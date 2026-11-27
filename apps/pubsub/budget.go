package pubsub

// budget.go — the embedded server says what it can hold.
//
// A JetStream server has a file store of some size, and by default that size is the
// free space under its store directory at the moment it boots: 18 GB on production's
// volume, a few on a laptop's microVM. The account every client uses states no limit
// of its own, so AccountInfo answers "unlimited" whatever the server can hold, and a
// stream owner that sizes itself from that answer (sdk/bus) asks for its production
// ceiling everywhere. On a small node the server then refuses it, 10047, for as long
// as the owner keeps asking.
//
// So the server states its limit on the account, in two cases:
//
//   - the deployment declares a budget (CLOUD_PUBSUB_MAX_STORE, in bytes): the plane
//     holds at most that, on a node of any size;
//   - it declares none and the store cannot hold the platform's ceilings together
//     (bus.Need): the plane holds what the node has.
//
// A node with room for the ceilings and no declared budget is left as it was.

import (
	"fmt"
	"strconv"

	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/pubsub/server"
)

const storeEnv = "CLOUD_PUBSUB_MAX_STORE"

// declared reads the deployment's budget: zero when it states none.
func declared() (int64, error) {
	v := env.Or(storeEnv, "")
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("bad %s %q (want a positive byte count)", storeEnv, v)
	}
	return n, nil
}

// limit is the file store the account is told it has, or zero to leave it unlimited.
// `store` is what the server holds, `reserved` what streams restored from disk have
// already taken of it, and `want` the declared budget.
//
// The server counts an account's limit beside the reservations already made under
// it, and refuses a limit when the two exceed its store. So the limit never passes
// the store less what is reserved, nor the part of the store whose own reservations
// (bus.Reserved percent of it) still fit beside it. The second bound is what keeps
// the limit the same from one boot to the next: without it, a restart that restores
// last boot's streams would be granted less, and the streams would shrink to match.
//
// A store with no room left states nothing: the streams it restored stand, and a
// new one is refused for the reason that is true, which is a full disk.
func limit(store, reserved, want int64) int64 {
	if store <= 0 {
		return 0
	}
	if want <= 0 && store >= bus.Need() {
		return 0
	}
	room := min(store-reserved-1, (store-1)/int64(100+bus.Reserved())*100)
	if want > 0 {
		room = min(room, want)
	}
	return max(room, 0)
}

// budget states the account's file limit on the running server and returns it; zero
// means the account was left unlimited.
func budget(ns *server.Server, want int64) (int64, error) {
	cfg := ns.JetStreamConfig()
	if cfg == nil {
		return 0, fmt.Errorf("jetstream is not enabled")
	}
	_, reserved, err := ns.JetStreamReservedResources()
	if err != nil {
		return 0, err
	}
	n := limit(cfg.MaxStore, reserved, want)
	if n == 0 {
		return 0, nil
	}
	// Only the file store is stated. Every other limit keeps the value an account
	// has by default, which is "none of its own".
	err = ns.GlobalAccount().UpdateJetStreamLimits(map[string]server.JetStreamAccountLimits{"": {
		MaxMemory: -1, MaxStore: n, MaxStreams: -1, MaxConsumers: -1, MaxAckPending: -1,
		MemoryMaxStreamBytes: -1, StoreMaxStreamBytes: -1,
	}})
	if err != nil {
		return 0, fmt.Errorf("a file store of %d bytes for the account (the server holds %d, %d reserved): %w", n, cfg.MaxStore, reserved, err)
	}
	return n, nil
}
