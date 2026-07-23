// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package cloud

// durable_integration_test.go is the MANDATORY staging gate (Red H2): the entire
// single-writer fence rests on the SeaweedFS S3 gateway evaluating If-None-Match /
// If-Match preconditions ATOMICALLY, server-side. That property cannot be tested from
// a box with no live gateway (the in-process fakes model it by construction), so this
// test runs ONLY against the real deployed gateway and MUST pass before durability
// fences real tenant data on a new SeaweedFS version.
//
// Run in staging:
//
//	CLOUD_DURABLE_IT=1 \
//	S3_ADMIN_ENDPOINT=s3.hanzo.svc:9000 S3_ADMIN_ACCESS_KEY=… S3_ADMIN_SECRET_KEY=… \
//	go test ./ -run TestSeaweedFSConditionalStoreAtomic_Staging -v

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/s3admin"
	"github.com/hanzoai/cloud/internal/org"
	s3 "github.com/hanzoai/s3-go"
	"github.com/hanzoai/vfs/replica"
)

// TestSeaweedFSConditionalStoreAtomic_Staging proves the two CAS preconditions the
// fence depends on, against the REAL gateway: (1) a create-only race (If-None-Match:*)
// admits EXACTLY ONE writer; (2) a version-conditioned race (If-Match) admits EXACTLY
// ONE writer per version. If either admits two, the fence is unsound on this gateway
// and split-brain is possible — do NOT ship durability against it.
func TestSeaweedFSConditionalStoreAtomic_Staging(t *testing.T) {
	if os.Getenv("CLOUD_DURABLE_IT") != "1" {
		t.Skip("staging-only: set CLOUD_DURABLE_IT=1 and S3_ADMIN_* to run against a live SeaweedFS gateway")
	}
	admin := s3admin.New()
	if !admin.Configured() {
		t.Fatal("S3_ADMIN_ACCESS_KEY/SECRET_KEY not set — cannot reach the SeaweedFS gateway")
	}
	client, err := admin.Client()
	if err != nil {
		t.Fatalf("s3 client: %v", err)
	}
	ctx := context.Background()
	if err := ensureDurableBucket(ctx, admin, client); err != nil {
		t.Fatalf("ensure bucket %q: %v", durableBucket, err)
	}
	cs := org.NewS3ConditionalStore(client, durableBucket)
	key := fmt.Sprintf("orgs/_durable_it/probe-%d.bin", time.Now().UnixNano())
	t.Cleanup(func() { _ = client.RemoveObject(ctx, durableBucket, key, s3.RemoveObjectOptions{}) })

	const racers = 12

	// (1) Create-only race: every racer PutIfVersion(key, data, "") — the gateway's
	// If-None-Match:* must admit exactly one create.
	created := countWinners(t, racers, func(i int) error {
		_, err := cs.PutIfVersion(ctx, key, []byte(fmt.Sprintf("create-%d", i)), "")
		return err
	})
	if created != 1 {
		t.Fatalf("create-only race admitted %d writers, want exactly 1 — If-None-Match is not atomic on this gateway", created)
	}

	// Read the winning version to condition the next race on.
	_, ver, err := cs.Get(ctx, key)
	if err != nil {
		t.Fatalf("get after create: %v", err)
	}

	// (2) Version-conditioned race: every racer PutIfVersion(key, data, ver) — the
	// gateway's If-Match must admit exactly one update against a given version.
	updated := countWinners(t, racers, func(i int) error {
		_, err := cs.PutIfVersion(ctx, key, []byte(fmt.Sprintf("update-%d", i)), ver)
		return err
	})
	if updated != 1 {
		t.Fatalf("version-conditioned race admitted %d writers, want exactly 1 — If-Match is not atomic on this gateway", updated)
	}
}

// countWinners runs op across n goroutines and returns how many returned nil; any
// error that is NOT ErrConflict fails the test (a real gateway/transport fault must
// not be mistaken for a lost precondition).
func countWinners(t *testing.T, n int, op func(i int) error) int {
	t.Helper()
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) { defer wg.Done(); errs[i] = op(i) }(i)
	}
	wg.Wait()
	wins := 0
	for i, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, replica.ErrConflict):
			// expected loser
		default:
			t.Fatalf("racer %d: unexpected (non-conflict) error: %v", i, err)
		}
	}
	return wins
}
