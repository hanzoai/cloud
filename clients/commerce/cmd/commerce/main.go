// Copyright © 2026 Hanzo AI. MIT License.
//
// commerce is the standalone commerce entry-point: bootLegacy() — direct-Gin
// behind a net/http listener, wiring api.Route(/v1) imperatively. This is the shape
// the standalone commerce pod runs.
//
// The unified cloud-mount path (zip.App with commerce mounted in-process) now lives
// in the ONE cloud binary (cmd/cloud), where clients/commerce self-registers as a
// subsystem (commerce.Mount) — there is no second "--cloud" boot mode on this
// binary. One way to serve commerce inside cloud: the folded subsystem.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	// Register the EVM ERC-20 transfer implementation into util/blockchain so
	// the contributor payout path can execute on-chain HUSD payouts
	// (PayoutMethod="crypto"). Without this blank import,
	// blockchain.TransferToken returns ErrNoTokenTransfer and crypto payouts
	// are skipped. This is the single wiring point that links luxfi/geth into
	// the production commerce binary.
)

func main() {
	var (
		dataDir         = flag.String("data", envStr("COMMERCE_DIR", "./commerce_data"), "data directory")
		httpAddr        = flag.String("http", envStr("COMMERCE_HTTP", "127.0.0.1:8090"), "HTTP listen address")
		dev             = flag.Bool("dev", envBool("COMMERCE_DEV", false), "enable development mode")
		requireIdentity = flag.Bool("require-identity", envBool("COMMERCED_REQUIRE_IDENTITY", false), "refuse requests without X-Org-Id/X-User-Id (gateway trust)")
	)
	flag.Parse()

	if err := bootLegacy(*dataDir, *httpAddr, *dev, *requireIdentity); err != nil {
		fmt.Fprintf(os.Stderr, "commerce: legacy boot: %v\n", err)
		os.Exit(1)
	}
}

func envStr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
