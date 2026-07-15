// Command bundlecmd is the offline connector-ingest step: it esbuild-bundles
// ONE ActivePieces connector source tree (its index.ts) into a single
// CommonJS program with the framework packages left external, and writes the
// blob. Run it per connector to vendor the JS the runtime executes natively.
//
//	go run ./clients/connectorruntime/internal/bundlecmd <index.ts> <out.js> [extraExternal...]
package main

import (
	"fmt"
	"os"

	connectorruntime "github.com/hanzoai/cloud/clients/connectorruntime"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: bundlecmd <entry index.ts> <out.js> [extraExternal...]")
		os.Exit(2)
	}
	entry, out := os.Args[1], os.Args[2]
	js, err := connectorruntime.Bundle(entry, os.Args[3:]...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bundle:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, js, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", out, len(js))
}
