// Command scratch is a plugin that answers its own version. The release-lane
// test builds it twice with different versions: two releases of one plugin.
package main

import (
	"context"
	"log"

	"github.com/zap-proto/zip"
)

// version is set at build time: -ldflags "-X main.version=v1.0.0".
var version = "unset"

// Version is the plugin's answer.
type Version struct {
	Version string `json:"version"`
}

func main() {
	app := zip.New(zip.Config{AppName: "billing"})
	app.Get("/v1/billing/scratch", func(_ context.Context, _ *struct{}) (*Version, error) {
		return &Version{Version: version}, nil
	})
	log.Fatal(app.Listen(zip.Addr(":9999")))
}
