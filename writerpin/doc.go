// Package writerpin abstracts WHO holds the single-writer pin — the exclusive
// right to open the RWO stores for write. It is the dynamic counterpart to the
// static role package: role says "I am configured as the writer"; writerpin says
// "I actually hold the pin right now, and here is when I lose it."
//
// Exactly one process in the cluster may hold the pin at a time. This is the
// mechanism that lets a reader be promoted to writer on writer loss WITHOUT ever
// permitting two concurrent writers (which would double-open the ZapDB/SQLite
// stores and corrupt the audit chain's in-memory head).
//
// One implementation: SingleWriter. The writer runs as a StatefulSet with
// replicas:1 + Recreate, so Kubernetes already guarantees at most one writer pod
// and the pin is held immediately and never lost. This is not a fake — it is the
// correct pin for the current topology. An election would be a second one.
//
// This package imports nothing from cloud, so it is free of import cycles and
// unit-testable in isolation.
package writerpin
