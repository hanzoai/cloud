package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/studio"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the studio app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet. The light host loads it as a
// plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `studio openapi`.
//
// Metered: a run's estimate is reserved and each finished step debited by the
// app's own meter (apps/studio/money.go), so the edge charges nothing on top.
// Shutdown stops the run worker, which every open run resumes from on the next
// start.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "studio",
		Price:    cloud.Metered,
		Use:      studio.Use,
		Shutdown: studio.Shutdown,
	}}, []string{"studio"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
