package main

// idempotency_test.go — the Idempotency-Key header is the request id: one value, read
// from the body's `requestId` or the header, on every op that can be retried.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// probed registers an op that takes In and answers the request id it was handed, and
// asks it with the body and the header each given or not.
func probed[In any](t *testing.T, name string, id func(*In) string) {
	t.Helper()
	for _, c := range []struct {
		name, body, header, want string
	}{
		{"the body alone", `{"requestId":"from-body","text":"x","seconds":1}`, "", "from-body"},
		{"the header alone", `{"text":"x","seconds":1}`, "from-header", "from-header"},
		{"both: the header wins", `{"requestId":"from-body","text":"x","seconds":1}`, "from-header", "from-header"},
	} {
		t.Run(name+"/"+c.name, func(t *testing.T) {
			got := ""
			app := zip.New(zip.Config{AppName: "agent", DisableStartupMessage: true})
			app.Post("/v1/agent/probe/:session", func(_ context.Context, in *In) (*client.CodingAck, error) {
				got = id(in)
				return &client.CodingAck{OK: true}, nil
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/agent/probe/sess_1", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			if c.header != "" {
				req.Header.Set("Idempotency-Key", c.header)
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if body, _ := io.ReadAll(resp.Body); resp.StatusCode >= 300 {
				t.Fatalf("answered %d %s", resp.StatusCode, body)
			}
			if got != c.want {
				t.Fatalf("the op was handed request id %q, want %q", got, c.want)
			}
		})
	}
}

// Every op that can be retried reads the header as its request id. The five inputs are
// the ones the routes in codingEndpoint take.
func TestTheIdempotencyKeyHeaderIsTheRequestID(t *testing.T) {
	probed(t, "start", func(in *client.CodingStartIn) string { return in.RequestID })
	probed(t, "steer", func(in *codingInstruction) string { return in.RequestID })
	probed(t, "extend", func(in *client.CodingExtendIn) string { return in.RequestID })
	probed(t, "pull", func(in *client.CodingPullIn) string { return in.RequestID })
	probed(t, "merge", func(in *client.CodingMergeIn) string { return in.RequestID })
}
