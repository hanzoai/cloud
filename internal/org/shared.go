// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// Two orthogonal substrates carry the per-org store, and neither is this
// package's:
//
//   - single-writer election (Rendezvous/HRW) → github.com/hanzoai/ha. It decides
//     WHO writes; pure Go, no storage.
//   - per-org SQLite replication → github.com/hanzoai/vfs/replica. It decides HOW
//     state ships to the object store.
//
// cloud-SPECIFIC pieces that stay in this package: membership.go (the live IAM
// membership Source + polling, the input to election) and cipher.go (the KMS-master
// per-org envelope encryption — it satisfies replica.Cipher). vfsstore.go implements
// the replica.Store over cloud's deps.VFS.
//
// The four names below are the substrates' vocabulary that cloud's OWN callers
// speak — the HRW election, whose input is a membership this package builds and
// whose answer build.go and every durable subsystem reads. Everything else is
// spelled `ha.` or `replica.` at the point of use, which is why there are four
// aliases here and not seventeen: a second spelling that says nothing new is a
// second name for one thing.

import (
	"github.com/hanzoai/ha"
	"github.com/hanzoai/vfs/replica"
)

type (
	// Member is one replica in the live membership set (HRW election input).
	Member = ha.Member
	// Store is the object-store surface (satisfied by vfsstore.go over deps.VFS).
	Store = replica.Store
)

var (
	// Owner returns the single writer for orgID (HRW), fail-closed on empty.
	Owner = ha.Owner
	// IsOwner reports whether selfID owns the writer for orgID.
	IsOwner = ha.IsOwner
)
