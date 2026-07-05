// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package zaptrace is a ZAP-native OTel trace transport: an otlptrace.Client
// whose wire is Hanzo's ZAP transport (github.com/zap-proto/http). Spans are
// marshaled as standard OTLP protobuf but shipped over the ZAP wire (capnp
// frames, X-Wing PQ-KEM handshake), NEVER OTLP-over-HTTP(:4318) or
// OTLP-over-gRPC(:4317). The collector's zapreceiver (:4319) terminates it and
// writes to hanzoai/datastore.
//
// The ExportTraceServiceRequest envelope is hand-encoded from the grpc-free
// trace messages (see UploadTraces) rather than the generated
// collector/trace/v1 request type, whose sibling gRPC service stubs
// (trace_service_grpc.pb.go, no build tag) would pull google.golang.org/grpc
// into the module graph. Hanzo services speak ZAP/HTTP/WS, never gRPC — do NOT
// "simplify" this back to coltrace.ExportTraceServiceRequest.
package zaptrace

import (
	"context"
	"fmt"
	"sync"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// Client implements go.opentelemetry.io/otel/exporters/otlp/otlptrace.Client.
type Client struct {
	endpoint string

	mu sync.Mutex
	tr *zaphttp.Transport
}

// compile-time assertion that Client satisfies the otlptrace contract.
var _ otlptrace.Client = (*Client)(nil)

// New returns an otlptrace.Client that ships spans over the ZAP wire to endpoint
// (host:port of a collector's ZAP receiver, e.g. otel-collector.hanzo.svc:4319).
func New(endpoint string) *Client {
	return &Client{endpoint: endpoint}
}

// Start opens the pooled ZAP transport (connects lazily on first upload).
func (c *Client) Start(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tr = zaphttp.NewTransport(c.endpoint)
	return nil
}

// Stop closes idle ZAP connections.
func (c *Client) Stop(context.Context) error {
	c.mu.Lock()
	tr := c.tr
	c.tr = nil
	c.mu.Unlock()
	if tr != nil {
		tr.CloseIdleConnections()
	}
	return nil
}

// UploadTraces ships one OTLP ExportTraceServiceRequest over the ZAP wire.
func (c *Client) UploadTraces(_ context.Context, protoSpans []*tracepb.ResourceSpans) error {
	c.mu.Lock()
	tr := c.tr
	c.mu.Unlock()
	if tr == nil {
		return fmt.Errorf("zaptrace: client not started")
	}
	// Encode the OTLP ExportTraceServiceRequest wire form directly from the
	// grpc-free trace messages. The request is a single repeated field —
	// `repeated ResourceSpans resource_spans = 1` — so appending each
	// ResourceSpans under field 1 is byte-identical to marshaling a
	// coltrace.ExportTraceServiceRequest, without importing the collector proto
	// package that would drag google.golang.org/grpc in (see package doc).
	var body []byte
	for _, rs := range protoSpans {
		rsBytes, err := proto.Marshal(rs)
		if err != nil {
			return err
		}
		body = protowire.AppendTag(body, 1, protowire.BytesType)
		body = protowire.AppendBytes(body, rsBytes)
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.Header.SetMethod(fasthttp.MethodPost)
	req.SetRequestURI("http://zap/v1/traces")
	req.Header.SetHost(c.endpoint)
	req.Header.SetContentType("application/x-protobuf")
	req.SetBody(body)

	if err := tr.Do(req, resp); err != nil {
		return fmt.Errorf("zaptrace: zap send: %w", err)
	}
	if code := resp.StatusCode(); code/100 != 2 {
		return fmt.Errorf("zaptrace: /v1/traces -> %d: %s", code, resp.Body())
	}
	return nil
}
