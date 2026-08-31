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

import (
	"context"
	"os"
	"strings"
)

type Held interface {
	// Lost is closed when the pin is lost (lease expiry, partition, eviction).
	// A SingleWriter pin never loses; its Lost channel stays open forever.
	Lost() <-chan struct{}

	// Release relinquishes the pin. Idempotent. After Release, the holder must
	// not write. Safe to call from a defer.
	Release()
}

// Pin is the election surface. Acquire blocks until this process holds the pin
// (or ctx is cancelled). A writer calls Acquire before opening the RWO stores; a
// promoted reader calls Acquire before flipping to writer mode.
type Pin interface {
	// Acquire blocks until the pin is held or ctx is done. On success the caller
	// is the sole writer until Held.Lost() fires or Held.Release() is called.
	Acquire(ctx context.Context) (Held, error)

	// Kind identifies the implementation for logs/metrics.
	Kind() string
}

// --- SingleWriter: the production-correct default for replicas:1 -------------

// SingleWriter grants the pin immediately and never revokes it, reflecting the
// Kubernetes guarantee that the writer StatefulSet has exactly one pod. Use this
// until consensus election is wired.
type SingleWriter struct{}

// NewSingleWriter returns the default single-writer pin.
func NewSingleWriter() *SingleWriter { return &SingleWriter{} }

func (*SingleWriter) Kind() string { return "single-writer" }

func (*SingleWriter) Acquire(ctx context.Context) (Held, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &neverLost{lost: make(chan struct{})}, nil
}

// neverLost is a Held whose Lost() channel is never closed (until Release).
type neverLost struct{ lost chan struct{} }

func (h *neverLost) Lost() <-chan struct{} { return h.lost }
func (h *neverLost) Release() {
	select {
	case <-h.lost:
	default:
		close(h.lost)
	}
}

// ResolveWithReason picks the pin and the sentence explaining the choice, so boot
// can log WHICH pin is in force and why. A silent fallback is how a cluster ends
// up believing it is electing when it is not.
func ResolveWithReason() (Pin, string) { return resolve(os.Getenv) }

// resolve is the testable core: env in, pin + reason out, no I/O.
func resolve(getenv func(string) string) (Pin, string) {
	if !truthy(getenv("CLOUD_WRITER_LEASE")) {
		return NewSingleWriter(), "single-writer: CLOUD_WRITER_LEASE unset (correct at replicas:1 — Kubernetes is the elector)"
	}
	// Asking for an election and silently getting a single writer is the corruption
	// this pin exists to prevent, so an operator who asks is told plainly that no
	// build elects rather than being pointed at configuration that would not help.
	return NewSingleWriter(), "single-writer: CLOUD_WRITER_LEASE set but this build cannot elect"
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
