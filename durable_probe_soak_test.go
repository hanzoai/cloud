// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package cloud

// durable_probe_soak_test.go answers a question the single-shot staging gate
// cannot: how OFTEN does the store fail the atomicity probe?
//
// That matters because the verdict is not stable. In one production pod, three
// seconds apart, the host process logged
//
//	durability disabled — ... update-race admitted 2 writers (want 1) — If-Match not atomic
//
// while the co-resident o11y plugin logged `durability enabled ... atomic_cas: true`.
// The two do not collide (probeKey mints a random key per probe), so one of those
// verdicts is wrong about the store. A single green probe therefore proves nothing,
// and neither does a single red one — only a rate does.
//
// This runs the REAL org.ProbeCAS, over the REAL client, in the REAL sequence
// (create-race → read back → update-race), which is what distinguishes it from an
// S3-level race harness: a harness that seeds with a plain PUT and races on the
// PUT's ETag skips the read-back the probe conditions on.
//
//	CLOUD_DURABLE_IT=1 CLOUD_PROBE_RUNS=200 \
//	S3_SECURE=false S3_ADMIN_ENDPOINT=localhost:18333 S3_ADMIN_ACCESS_KEY=… S3_ADMIN_SECRET_KEY=… \
//	go test ./ -run TestProbeCASSoak_Staging -v

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/hanzoai/cloud/clients/s3admin"
	"github.com/hanzoai/cloud/internal/org"
)

func TestProbeCASSoak_Staging(t *testing.T) {
	if os.Getenv("CLOUD_DURABLE_IT") != "1" {
		t.Skip("staging-only: set CLOUD_DURABLE_IT=1 and S3_ADMIN_* to run against a live gateway")
	}
	runs := 100
	if v, err := strconv.Atoi(os.Getenv("CLOUD_PROBE_RUNS")); err == nil && v > 0 {
		runs = v
	}
	admin := s3admin.New()
	if !admin.Configured() {
		t.Fatal("S3_ADMIN_ACCESS_KEY/SECRET_KEY not set")
	}
	client, err := admin.Client()
	if err != nil {
		t.Fatalf("s3 client: %v", err)
	}
	ctx := context.Background()
	if err := ensureDurableBucket(ctx, admin, client); err != nil {
		t.Fatalf("ensure bucket %q: %v", durableBucket, err)
	}
	cond := org.NewS3ConditionalStore(client, durableBucket)

	nonAtomic, hard := 0, 0
	var firstNonAtomic, firstHard error
	for i := 0; i < runs; i++ {
		err := org.ProbeCAS(ctx, cond, durableProbePrefix)
		switch {
		case err == nil:
		case errors.Is(err, org.ErrNonAtomicStore):
			nonAtomic++
			if firstNonAtomic == nil {
				firstNonAtomic = err
			}
		default:
			hard++
			if firstHard == nil {
				firstHard = err
			}
		}
	}
	t.Logf("ProbeCAS ×%d against %s: non-atomic verdicts=%d hard errors=%d", runs, durableBucket, nonAtomic, hard)
	if firstNonAtomic != nil {
		t.Errorf("store failed the atomicity probe %d/%d times — first: %v", nonAtomic, runs, firstNonAtomic)
	}
	if firstHard != nil {
		t.Errorf("probe hit %d/%d hard errors — first: %v", hard, runs, firstHard)
	}
}
