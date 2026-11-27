// Command gen-app-cmds keeps the per-app command stubs in step with the fleet
// manifest.
//
// manifest.Apps (manifest/apps.go) is the hand-authored SOURCE OF TRUTH for the
// fleet — name, prefixes, eager. Each app ALSO ships a standalone binary at
// plugin/<name>/main.go: its own one-app composition root, which states that app's
// Mount/Shutdown/OwnsHealth/Price once, where they are used. This tool binds the
// two views. It reads the manifest as a VALUE (it imports the package and ranges
// manifest.Apps — not a re-parse of the source text) and:
//
//	SCAFFOLDS  a plugin/<name>/main.go for a manifest app that has none, the lean
//	           one-app form (import apps/<name>, mount <name>.Mount, Free), and
//	           its doc.go,
//	WRITES     that app's apps/<pkg>/Makefile, the two lines naming which apps the
//	           package backs and including the one build contract, and
//	VALIDATES  the two sets are in bijection — every app has a command, and every
//	           app command is a manifest app — so the light host can never route
//	           to a binary that is not there, nor a binary go unrouted.
//
// The Makefile is written here because it is the SAME manifest row that decides
// it, and because the alternative was measured: `sandbox` was added with a main
// and no Makefile, so `make describe` had no rule to run for it, so it published
// no OpenAPI subset, so `make openapi` failed on an app that was otherwise
// complete. Every app Makefile already CLAIMED this file generated it. That
// claim was false for as long as a human had to remember the second file, and a
// generator's header comment that is false is worse than no comment, because the
// next person believes it.
//
// It does NOT rewrite an existing main: that file is SOURCE, hand-edited the
// moment an app needs a Shutdown hook, an OwnsHealth, a whole-app App grant, a
// metered Price, or a shared package (account serves two apps by two funcs) —
// none of which a scaffold can guess. So idempotency is structural: a clean,
// consistent tree is left untouched, which is what lets CI diff it.
//
//	run:  go run ./plugin/gen-app-cmds
package main
