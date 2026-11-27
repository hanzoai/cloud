// Package catalog is the generated connector catalog: every piece's
// declaration (catalog.json), its logo (logo/), and the program of every piece
// that runs in-process (program/). cmd/piece writes all three from the pieces
// source at the revision catalog.json names; nothing else does.
//
// It is data, and only the processes that serve connectors link it — the
// provider app, which connects and runs them, and the automation engine, which
// composes them into flows. Each wires it in at its own composition root.
package catalog
