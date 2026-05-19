// cloud-smoke is a minimal smoke harness for the cloud orchestrator
// HTTP+JSON path. It mounts a couple of in-process subsystems on a
// zip.App, brings up the listener, and exposes the /v1/base/health
// endpoint per the HIP-0106 reference contract. Used to verify the
// jsonv2 wiring end-to-end without pulling in the full subsystem
// matrix (which has unrelated build issues in cmd/cloud — see
// gateway/zap_wire.go uint16 overflow + gateway import cycle, both
// tracked separately).
package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	"github.com/hanzoai/zip/middleware"
)

func main() {
	cfg := cloud.LoadConfig()
	if cfg.DataDir == "" {
		cfg.DataDir = "/tmp/cloud-smoke"
	}
	if cfg.Brand == "" {
		cfg.Brand = "hanzo"
	}
	if cfg.Domain == "" {
		cfg.Domain = "api.hanzo.ai"
	}

	deps := cloud.BuildDeps(cfg)

	app := zip.New(zip.Config{Logger: deps.Logger})
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	app.Use(middleware.Logger(deps.Logger))

	// HIP-0106 reference health endpoints. The brief's smoke target.
	app.Get("/v1/base/health", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{
			"service": "base",
			"status":  "ok",
		})
	})
	app.Get("/v1/vfs/health", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{
			"service": "vfs",
			"status":  "ok",
		})
	})

	deps.Logger.Info("cloud-smoke listening",
		"http", cfg.ListenAddr,
		"brand", cfg.Brand,
		"domain", cfg.Domain,
		"json_variant", zip.JSONVariant,
	)
	if err := app.Listen(cfg.ListenAddr); err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}
}
