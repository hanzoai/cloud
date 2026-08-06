package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sandboxes"
)

// Standalone entry for the sandboxes app.
//
// Without this file apps/sandboxes compiles and serves nothing: the host loads a
// sibling binary per manifest row, so an app with a Mount and no plugin/<app> is
// a package nothing links. That is not hypothetical — it is exactly the state
// apps/sandbox sat in while four consumers pointed at it and `curl
// api.hanzo.ai/v1/sandbox/boxes` answered 404.
//
// Free at the edge because the SANDBOX is what gets metered, not the call that
// asks for one. Charging per request here and again for the machine-seconds it
// runs would bill twice for one thing, and the lease is the honest unit: a
// caller pays for how long they held a sandbox, not for how many times they
// asked it a question.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "sandboxes",
		Price: cloud.Free,
		Mount: sandboxes.Mount,
	}}, []string{"sandboxes"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
