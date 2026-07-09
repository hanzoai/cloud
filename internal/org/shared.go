// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// Two orthogonal substrates, re-exported here as aliases so this package's own API
// (membership, cipher, vfsstore) is unchanged while each concern lives in exactly
// ONE place (one and one way):
//
//   - single-writer election (Rendezvous/HRW: Member, Owner, IsOwner, Replicas) →
//     github.com/hanzoai/ha. It decides WHO writes; pure Go, no storage.
//   - per-org SQLite replication (Replicator, Store, DB, DBPath) →
//     github.com/hanzoai/vfs/replica. It decides HOW state ships to the object store.
//
// cloud-SPECIFIC pieces that stay in this package: membership.go (the live IAM
// membership Source + polling, the input to election) and cipher.go (the KMS-master
// per-org envelope encryption — it satisfies replica.Cipher). vfsstore.go implements
// the replica.Store over cloud's deps.VFS.

import (
	"github.com/hanzoai/ha"
	"github.com/hanzoai/vfs/replica"
)

type (
	// Member is one replica in the live membership set (HRW election input).
	Member = ha.Member
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
	Owner = ha.Owner
	// IsOwner reports whether selfID owns the writer for orgID.
	IsOwner = ha.IsOwner
	// Replicas returns the owner then ordered failover successors.
	Replicas = ha.Replicas
	// DBPath is the canonical object-store location of an org's SQLite DB.
	DBPath = replica.DBPath
)
