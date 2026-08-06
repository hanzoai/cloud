// Copyright © 2026 Hanzo AI. MIT License.

package cloud_test

// plane_encodable_test.go — a plane type that cannot be ENCODED is a door that is
// shut while every other signal says it is open.
//
// ObsErrorIn.Headers was a map[string]string. zapenc carries scalars, strings,
// byte slices, structs, pointers and slices, and REFUSES anything else at encode
// so a field can never silently fail to arrive — so every ObsErrorPost call died
// inside zip.Call, before the socket, in dur_ms=0. The Sentry envelope door
// answered 503 for 24h+ with the peer up, the socket bound and the op registered,
// which is why it read as an outage with no failing component: the request never
// left the caller.
//
// Nothing already in the suite could see it. The op's own tests call the handler
// directly (no encode), and the sibling op that shared the SAME socket — the
// obs_event_claim wire, two scalar fields — kept working, so POST /v1/event stayed
// 200 the whole time and pointed every investigation at routing. That op has since
// been retired with the dead LLM-obs write path it fed, so it is no longer walked
// below; the property it demonstrated is the reason this test walks KINDS and not a
// list of types, and it outlives any individual op.

import (
	"context"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The BEHAVIOURAL half: the Sentry envelope's input must survive a real crossing.
// This fails on the map — zip.Call refuses to encode it — and passes on the list.
func TestObsErrorInCrossesThePlane(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())

	app := zip.New(zip.Config{AppName: "obsecho"})
	zip.Post[plane.ObsErrorIn, plane.ObsErrorOut](app, "/obs/error/post",
		func(_ context.Context, in *plane.ObsErrorIn) (*plane.ObsErrorOut, error) {
			// Echo one header back as the body so a DROPPED header fails loudly
			// rather than passing as an empty map would.
			var got string
			for _, h := range in.Headers {
				if h.Name == "X-Sentry-Auth" {
					got = h.Value
				}
			}
			return &plane.ObsErrorOut{Status: 401, Body: []byte(got)}, nil
		}, zip.WithOperationID("obs_error_post"))
	go func() { _ = app.Listen(zip.SocketPath("obsecho")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "obsecho")

	out, err := cloud.Ask[plane.ObsErrorIn, plane.ObsErrorOut](context.Background(),
		"obsecho", "obs_error_post", &plane.ObsErrorIn{
			Path:    "/v1/event/6ba7b810-9dad-11d1-80b4-00c04fd430c8/envelope/",
			Headers: []plane.Header{{Name: "X-Sentry-Auth", Value: "Sentry sentry_key=abc"}},
			Body:    []byte("{}"),
		})
	if err != nil {
		t.Fatalf("the envelope input did not cross the plane: %v", err)
	}
	if out == nil {
		t.Fatal("no answer crossed back")
	}
	// The runtime's status must arrive VERBATIM — a 401 "invalid ingest key" is
	// the SDK's signal to stop retrying, and reshaping it into a 503 is what made
	// every Sentry client retry a door that would never open.
	if out.Status != 401 {
		t.Fatalf("status %d crossed, want 401", out.Status)
	}
	if string(out.Body) != "Sentry sentry_key=abc" {
		t.Fatalf("the DSN header did not survive the crossing: %q", out.Body)
	}
}

// The STRUCTURAL half, and the one that keeps holding: no type on this plane may
// carry a kind zapenc refuses. Written as a field-kind walk rather than a list of
// known-bad types, because the failure is a property of the KIND — the next map
// added to any of these is the same 24h outage.
func TestNoPlaneTypeCarriesAnUnencodableKind(t *testing.T) {
	types := []any{
		plane.AuthorizeIn{}, plane.RecordIn{}, plane.BalanceIn{},
		plane.SecretIn{}, plane.FilesIn{}, plane.Visibility{}, plane.ReserveIn{},
		plane.ObsErrorIn{}, plane.ObsErrorOut{}, plane.Header{},
		// ScopeRules is walked, not ScopeRule: the walk descends a slice of
		// structs, so the row type is reached through the reply that carries it
		// — which is also the only way it ever crosses.
		plane.ScopeRules{},
		plane.SlackSendIn{},
		plane.StartIn{}, plane.Started{},
	}
	for _, v := range types {
		walkEncodable(t, reflect.TypeOf(v), reflect.TypeOf(v).Name())
	}
}

// The same rule as the walk above, made TOTAL rather than list-driven.
//
// The walk can only inspect the types someone remembered to add to it, and the failure it
// guards against is a type nobody thought about — so the guarantee is taken one level up:
// package plane cannot NAME metering.Usage, because it does not import the package the
// type lives in. That is a property of every plane type at once, including the ones
// written after this test.
//
// It is also the leaf rule the plane already lives by for its own reasons (plane/ask.go:
// cloud drags 576 packages, this leaf 75) — apps/metering pulls the finance ledger, its
// SQLite store and the envelope keys behind it. One import would take the money's private
// vocabulary and its whole dependency tree into the package every peer call links.
func TestThePlaneCannotNameTheMetersUsage(t *testing.T) {
	const forbidden = `"github.com/hanzoai/cloud/apps/metering"`
	fset := token.NewFileSet()
	err := filepath.WalkDir("plane", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		for _, imp := range f.Imports {
			if imp.Path.Value == forbidden {
				t.Errorf("%s imports apps/metering — the plane must not be able to name "+
					"metering.Usage, whose Ref is the ledger's key and whose `json:\"-\"` tag "+
					"zapenc does not honour", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk plane/: %v", err)
	}
}

// meterUsage is the type no plane value may reach. See walkEncodable.
var meterUsage = reflect.TypeOf(metering.Usage{})

// walkEncodable asserts every field a plane type reaches is a kind zapenc carries, and
// that none of them reaches the METER's usage value.
func walkEncodable(t *testing.T, typ reflect.Type, path string) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	// THE METER'S USAGE MUST NEVER BE A PLANE TYPE, and the reason is a field that does
	// not appear on any wire a reviewer would read. metering.Usage.Ref is the LEDGER'S
	// IDEMPOTENCY KEY and is tagged `json:"-"` precisely so no caller can send one — but
	// zapenc encodes by FIELD, not by tag, so the moment such a value became a plane op's
	// input the key would cross the plane and a caller could name it. The plane's own
	// Usage carries a Ref deliberately and separately: it is the SENDING PROCESS's
	// server-assigned identity, not a client's.
	//
	// The tag makes this invisible to inspection, so it is asserted rather than reviewed.
	if typ == meterUsage {
		t.Errorf("%s reaches metering.Usage — its Ref is the ledger's idempotency key and is "+
			"`json:\"-\"` to keep callers out of it, but zapenc ignores tags and would carry it "+
			"across the plane. Send plane.Usage, whose Ref is the sender's own act name.", path)
		return
	}
	if typ.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		where := path + "." + f.Name
		switch ft.Kind() {
		case reflect.Map, reflect.Interface, reflect.Chan, reflect.Func, reflect.Complex64, reflect.Complex128:
			t.Errorf("%s is a %s — zapenc refuses it AT ENCODE, so every call carrying this "+
				"type fails before the socket and the peer looks healthy while the door is shut. "+
				"Carry it as a slice of structs.", where, ft.Kind())
		case reflect.Slice, reflect.Array:
			el := ft.Elem()
			for el.Kind() == reflect.Pointer {
				el = el.Elem()
			}
			if el.Kind() == reflect.Uint8 { // []byte is carried whole
				continue
			}
			walkEncodable(t, el, where+"[]")
		case reflect.Struct:
			walkEncodable(t, ft, where)
		}
	}
}
