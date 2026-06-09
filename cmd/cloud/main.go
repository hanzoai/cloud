// cloud is the unified Hanzo Cloud binary per HIP-0106.
//
// One binary. Many subsystems. Deployment configuration determines
// which subsystems mount at startup. Same artifact powers
// api.hanzo.ai, api.osage.cloud, api.lux.cloud, api.zoo.cloud, and
// every other white-label resold cloud surface.
package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	"github.com/hanzoai/zip/middleware"

	// Subsystems — each ships func Mount(*zip.App, cloud.Deps) error and
	// registers itself via init() in cloud.Registry. Import paths reflect
	// where each subsystem's Mount lives.
	_ "github.com/hanzoai/ai"          // order 150
	_ "github.com/hanzoai/amqp"        // order 30
	_ "github.com/hanzoai/authz"       // order 70
	_ "github.com/hanzoai/base"        // order 60
	_ "github.com/hanzoai/commerce"    // order 100
	_ "github.com/hanzoai/gateway"     // order 80
	_ "github.com/hanzoai/iam/pkg/iam" // order 50 (Mount lives in pkg/iam submodule)
	_ "github.com/hanzoai/ingress"     // order 90
	_ "github.com/hanzoai/kms"         // order 10
	_ "github.com/hanzoai/licensing"   // order 110 (after iam + commerce)
	_ "github.com/hanzoai/mcp/go"      // order 160 (Mount lives in go submodule)
	_ "github.com/hanzoai/o11y"        // order 70 (mounts alongside authz)
	_ "github.com/hanzoai/vfs"         // order 20

	// Node-service subsystems hosted in-process via base+goja (HIP-0106).
	// Each loads its service repo's goja/bundle.js into a goja runtime and
	// registers /v1/* routes. The JS + catalog data live in the service repos
	// (hanzoai/plans, hanzoai/pricing); these wrappers are glue in cloud.
	_ "github.com/hanzoai/cloud/clients/plansvc"    // order 111 — /v1/plans/*
	_ "github.com/hanzoai/cloud/clients/pricingsvc" // order 112 — /v1/pricing/*
)

func main() {
	cfg := cloud.LoadConfig()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	deps := cloud.BuildDeps(cfg)

	app := zip.New(zip.Config{Logger: deps.Logger})

	// Canonical middleware pipeline. Order matters:
	//  1. Recover   — panic → JSON 500
	//  2. RequestID — generate / propagate X-Request-Id
	//  3. Logger    — request-line log via luxfi/log
	//  4. Telemetry — OTel span; depends on deps.O11y if enabled
	//  5. Auth      — JWT validation; strips client identity, mints from JWT
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	app.Use(middleware.Logger(deps.Logger))
	// app.Use(middleware.Telemetry(deps.O11y))  // enable once o11y mounted
	// app.Use(middleware.Auth(deps.IAM))         // enable once iam mounted

	// Per-deployment subsystem mount.
	if err := cloud.MountAll(app, cfg, deps); err != nil {
		fmt.Fprintf(os.Stderr, "mount: %v\n", err)
		os.Exit(1)
	}

	deps.Logger.Info("listening",
		"http", cfg.ListenAddr,
		"zap", cfg.ZAPListenAddr,
		"brand", cfg.Brand,
		"domain", cfg.Domain,
	)
	if err := app.Listen(cfg.ListenAddr); err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
}
