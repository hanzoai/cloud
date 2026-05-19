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

	"github.com/hanzoai/cloud/pkg/cloud"
	"github.com/hanzoai/zip"
	"github.com/hanzoai/zip/middleware"

	// Subsystems — each ships func Mount(*zip.App, cloud.Deps) error.
	// Imports register themselves in cloud.Registry via init().
	//
	// Uncomment + add each as the per-subsystem port lands:
	//
	// _ "github.com/hanzoai/kms/pkg/kms"        // PR pending
	// _ "github.com/hanzoai/amqp/pkg/amqp"      // PR pending
	// _ "github.com/hanzoai/vfs/pkg/vfs"        // already has Mount
	// _ "github.com/hanzoai/mq/pkg/mq"          // PR pending
	// _ "github.com/hanzoai/iam/pkg/iam"        // PR pending
	// _ "github.com/hanzoai/base/pkg/base"      // PR pending
	// _ "github.com/hanzoai/commerce/pkg/commerce" // already has Mount (gin) — adapt to zip
	// _ "github.com/hanzoai/gateway/pkg/gateway"   // already has Mount
	// _ "github.com/hanzoai/o11y/pkg/o11y"       // PR pending
	// _ "github.com/hanzoai/ai/pkg/ai"           // PR pending (was hanzoai/cloud LLM subsystem)
	// _ "github.com/hanzoai/mcp/pkg/mcp"         // PR pending
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
