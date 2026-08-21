package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/amqp"
)

// Standalone entry for the amqp app.
//
// This is the app's OWN composition root — it links only apps/amqp and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `amqp openapi`.
//
// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned.
// Shutdown is stated here because this app owns a TCP listener on :5672 and the
// bus connection under it — neither of which a process exit drops cleanly, and
// an AMQP client deserves a connection.close naming a reason rather than a
// reset socket.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "amqp",
		Price:    cloud.Free,
		Mount:    amqp.Mount,
		Shutdown: amqp.Shutdown,
	}}, []string{"amqp"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
