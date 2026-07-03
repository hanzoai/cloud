// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0

package zaptrace

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestUploadTracesOverZAP proves spans leave cloud over the ZAP wire: a real
// zaphttp.Server terminates the frame and the body decodes back to the
// ExportTraceServiceRequest we sent.
func TestUploadTracesOverZAP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu      sync.Mutex
		gotPath string
		gotName string
	)
	srv := &zaphttp.Server{Handler: func(ctx *fasthttp.RequestCtx) {
		var req coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(ctx.PostBody(), &req); err != nil {
			ctx.SetStatusCode(400)
			return
		}
		mu.Lock()
		gotPath = string(ctx.Path())
		if len(req.ResourceSpans) > 0 && len(req.ResourceSpans[0].ScopeSpans) > 0 &&
			len(req.ResourceSpans[0].ScopeSpans[0].Spans) > 0 {
			gotName = req.ResourceSpans[0].ScopeSpans[0].Spans[0].Name
		}
		mu.Unlock()
		ctx.SetStatusCode(200)
	}}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	c := New(ln.Addr().String())
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	rs := []*tracepb.ResourceSpans{{
		ScopeSpans: []*tracepb.ScopeSpans{{
			Spans: []*tracepb.Span{{Name: "chat zen"}},
		}},
	}}
	if err := c.UploadTraces(context.Background(), rs); err != nil {
		t.Fatalf("upload: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		p, n := gotPath, gotName
		mu.Unlock()
		if p == "/v1/traces" && n == "chat zen" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server did not receive the span over ZAP (path=%q name=%q)", gotPath, gotName)
}
