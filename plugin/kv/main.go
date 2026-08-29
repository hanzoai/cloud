package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/kv"
)

// Standalone entry for the kv app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `kv describe`. Hand-owned — edit the spec below directly.
//
// NO Shutdown. The one thing this process holds on the plane is a NATS client
// connection, opened on first use by pubsub.Bus, and closing a client socket is
// what exiting already does. pubsub's own Shutdown exists because that process
// ALSO owns the embedded server and re-mounts in place; this one owns neither,
// so a hook here would be a second way to say what exit says.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "kv",
		Price: cloud.Free,
		Use:   kv.Use,
	}}, []string{"kv"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
