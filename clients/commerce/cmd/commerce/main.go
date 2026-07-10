// Copyright © 2026 Hanzo AI. MIT License.
//
// commerce is the canonical entry-point. Two boot modes — chosen at
// startup, same binary:
//
//	legacy (default):  flag --cloud absent, env COMMERCE_MODE unset/!=cloud
//	                   → bootLegacy(): direct-Gin behind net/http listener,
//	                     wires api.Route(/v1) imperatively. The shape the
//	                     production deployment runs today.
//
//	cloud:             flag --cloud OR env COMMERCE_MODE=cloud
//	                   → bootCloud(): zip.App with commerce mounted via
//	                     zip.AdaptNetHTTP(ginEngine) — gin is sealed as an
//	                     inner handler, no public route surface of its own.
//	                     Requires a build with `-tags cloud`; without the
//	                     tag the cloud entry returns a clear error and the
//	                     process exits non-zero.
//
// Default stays legacy until cloud-mount is validated in production.
// Phase 1 of the staged Gin → zip migration.

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
		cloudMode       = flag.Bool("cloud", envCloudMode(), "boot via cloud-mount (zip.App with gin sealed inside); requires -tags cloud build")
	)
	flag.Parse()

	if *cloudMode {
		if err := bootCloud(*dataDir, *httpAddr, *dev, *requireIdentity); err != nil {
			fmt.Fprintf(os.Stderr, "commerce: cloud boot: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := bootLegacy(*dataDir, *httpAddr, *dev, *requireIdentity); err != nil {
		fmt.Fprintf(os.Stderr, "commerce: legacy boot: %v\n", err)
		os.Exit(1)
	}
}

// envCloudMode is true iff COMMERCE_MODE=cloud (case-insensitive trim
// not needed — env values are exact in production). Used as the default
// for --cloud so operators can flip modes via env without changing argv.
func envCloudMode() bool {
	return os.Getenv("COMMERCE_MODE") == "cloud"
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
