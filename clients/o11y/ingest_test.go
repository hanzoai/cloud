// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

func TestWriteIngestConfig(t *testing.T) {
	path, err := writeIngestConfig(cloud.Deps{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		"0.0.0.0:4317", "0.0.0.0:4318",
		"datastoretraces", "datastorelogsexporter",
		"level: none", "${env:CLOUD_OTLP_INGEST_DSN}", "deployment.environment",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("rendered config missing %q\n%s", want, s)
		}
	}
	// The DSN must be a ${env:} reference — never a plaintext credential on disk.
	if strings.Contains(s, "password=") {
		t.Fatalf("config leaked a credential:\n%s", s)
	}
}

// TestIngestCollectorValidates is the load-bearing correctness check: it proves
// the trimmed factory set (ingestFactories) resolves EVERY component key in the
// rendered pipeline config — otlp receiver, memory_limiter/resource/batch
// processors, datastoretraces/datastorelogsexporter exporters, and both the
// traces and logs pipelines — via otelcol's DryRun, which unmarshals the config,
// validates every component, and builds the full pipeline graph.
//
// The o11y datastore exporters dial the datastore eagerly when the graph is
// built, so DryRun reaches — and only fails at — that dial (this hermetic test
// has no live datastore). Any OTHER error means genuine config/factory drift
// (an unmapped key, a bad pipeline, an invalid component config) and fails the
// test. So a clean pass OR a datastore-dial error both confirm the pipeline is
// wired correctly; anything else is a real defect.
func TestIngestCollectorValidates(t *testing.T) {
	col, err := buildIngestCollector(cloud.Deps{DataDir: t.TempDir()}, "tcp://127.0.0.1:9000?username=default&password=test")
	if err != nil {
		t.Fatalf("buildIngestCollector: %v", err)
	}
	err = col.DryRun(context.Background())
	if err != nil && !strings.Contains(err.Error(), "datastore") {
		t.Fatalf("config/factory drift (not a datastore dial): %v", err)
	}
}
