// Package writerlease answers ONE question for ONE process: must I take the
// pod's writer lease before anything on this volume is opened for write?
//
// The lease states a POD-level fact — "this pod owns this volume" — using a
// PROCESS-level primitive, flock(2) on {DataDir}/.writer.lock. Those are only
// the same sentence when exactly one process per pod ever reaches for the lock,
// and cloud is a plugin host: the router in cmd/cloud spawns every subsystem
// (kms, pubsub, kafka, …) as its own child process, all sharing one DataDir.
//
// On 2026-08-04 they were not the same sentence. The acquire lived in
// cloud.Listen — the body every PLUGIN binary runs, and the one thing the router
// never calls. Turning CLOUD_WRITER_LEASE on therefore aimed the interlock at
// the siblings instead of at the other pod: kms won the flock, pubsub and kafka
// blocked until the 90s fail-closed deadline, nothing bound the listener, the
// liveness probe killed the pod, and its replacement deadlocked identically.
// api.hanzo.ai served 503 for four minutes at the hands of its own safety
// mechanism.
//
// The repair that followed excluded the children but never handed the lock to
// the router, so no process in the pod took it at all: an interlock that reads
// as armed and holds nothing. That is the more dangerous of the two states. The
// deadlock announced itself; a lock held by nobody is quiet right up until
// someone believes the log line, switches the Deployment to RollingUpdate, and
// two pods open the same files.
//
// So the duty is decided here, once, from the process's own position in the
// tree, and every caller does as it is told:
//
//	Take    — this process is the root of its pod. Take the lock BEFORE it
//	          spawns anything; release it AFTER the last child is gone.
//	Inherit — a parent already holds it for this whole pod. Do not touch it.
//	Off     — this deployment asked for no lease. The default, and correct
//	          under strategy: Recreate, where Kubernetes retires the old pod
//	          before starting the new one, so no two writers ever coexist.
//
// This package imports only the stdlib and role, so the light router binary can
// link it without dragging in the request tier it deliberately does not build.
package writerlease
