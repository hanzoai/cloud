// Package main is the entrypoint for the unified Hanzo Cloud binary per HIP-0106.
//
// One binary. Many subsystems. Deployment configuration determines which
// subsystems are enabled at startup.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "hanzo-cloud: scaffold; full Mount() integration per HIP-0106 in progress")
	os.Exit(0)
}
