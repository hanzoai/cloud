// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// The Replicator, single-writer election (Rendezvous/HRW), Store/DB interfaces,
// and DBPath were PROMOTED out of this package into the shared HA-SQLite substrate
// hanzoai/vfs/replica, so EVERY Hanzo service adopts ONE implementation (one and one
// way). cloud re-exports them here as aliases so this package's own API (membership,
// cipher, vfsstore) is unchanged, while the logic lives in exactly one place.
//
// cloud-SPECIFIC pieces that stay in this package: membership.go (the live IAM
// membership source + polling) and cipher.go (the KMS-master per-org envelope
// encryption — it already satisfies replica.Cipher). vfsstore.go implements the
// replica.Store over cloud's deps.VFS.

import "github.com/hanzoai/vfs/replica"

type (
	// Member is one replica in the live membership set (HRW election input).
	Member = replica.Member
	// Replicator binds one per-org SQLite to its object-store slot.
	Replicator = replica.Replicator
	// Store is the object-store surface (satisfied by vfsstore.go over deps.VFS).
	Store = replica.Store
	// DB is the local per-org SQLite (Snapshot/Restore).
	DB = replica.DB
	// Option configures a Replicator.
	Option = replica.Option
)

var (
	// NewReplicator binds the DB at key to its store slot.
	NewReplicator = replica.NewReplicator
	// WithEncryption seals every pushed snapshot with the org's per-org key.
	WithEncryption = replica.WithEncryption
	// Owner returns the single writer for orgID (HRW), fail-closed on empty.
	Owner = replica.Owner
	// IsOwner reports whether selfID owns the writer for orgID.
	IsOwner = replica.IsOwner
	// Replicas returns the owner then ordered failover successors.
	Replicas = replica.Replicas
	// DBPath is the canonical object-store location of an org's SQLite DB.
	DBPath = replica.DBPath
)
