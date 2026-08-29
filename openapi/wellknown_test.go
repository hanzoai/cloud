package openapi_test

// WHO ANSWERS /.well-known/openapi.json — and the test has to SERVE to ask.
//
// The address is not vacant. zip auto-mounts a document of its own there from its
// own typed-op registry, and on a process whose surface is mostly untyped routes
// that document is nearly empty: api.hanzo.ai publishes 841 bytes titled
// "cloud 0.0.0", describing /healthz and /readyz, at the address every SDK
// generator and IDE probes first. So the question is not "does a document answer"
// — one always did — but WHICH.
//
// That is also why this file listens on a socket instead of using App.Test.
// zip installs its projections in prepare(), which runs from Serve and from
// nothing else, so under App.Test zip's competing route DOES NOT EXIST and any
// assertion about precedence passes for the wrong reason. The first draft of this
// test did exactly that and was green while proving nothing.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func app() *zip.App {
	return zip.New(zip.Config{Logger: luxlog.New("wellknown"), DisableStartupMessage: true})
}

type probeOut struct {
	OK bool `json:"ok"`
}

// served starts app on a loopback port and returns a reader for its answers. The
// port is taken by binding and releasing, which races with nothing else in a
// package that runs one of these at a time.
func served(t *testing.T, a *zip.App) func(path string) (int, []byte) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	host, err := zip.Serve(a, "http://"+addr)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })

	get := func(path string) (int, []byte) {
		t.Helper()
		resp, err := http.Get("http://" + addr + path)
		if err != nil {
			return 0, nil
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if code, _ := get(openapi.Path); code == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never answered on %s", openapi.Path, addr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func(path string) (int, []byte) {
		code, body := get(path)
		if code == 0 {
			t.Fatalf("GET %s: no answer", path)
		}
		return code, body
	}
}

func title(t *testing.T, body []byte) string {
	t.Helper()
	var doc struct {
		Info struct {
			Title string `json:"title"`
		} `json:"info"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return doc.Info.Title
}

// TestTheAliasOutranksZipsOwnDocument: the point of the whole change.
//
// The assertion is on the TITLE and not on "is this a document", because zip's
// answer is a valid document too — that is precisely what makes the defect
// invisible. Only whose document it is separates the two.
func TestTheAliasOutranksZipsOwnDocument(t *testing.T) {
	a := app()
	// A typed op, so zip's own projection is INSTALLED and actually competing.
	// installOpenAPIRoutes returns early on an empty registry, so without this
	// the test would once again be asking a question nobody is answering.
	zip.Get(a, "/v1/probe/thing", func(context.Context, *struct{}) (*probeOut, error) {
		return &probeOut{OK: true}, nil
	})
	openapi.Use(a, openapi.Info{Title: "Hanzo Cloud", Version: "v1"})

	get := served(t, a)
	code, body := get(openapi.WellKnown)
	if code != 200 {
		t.Fatalf("GET %s = %d, want 200", openapi.WellKnown, code)
	}
	if got := title(t, body); got != "Hanzo Cloud" {
		t.Fatalf("GET %s is served by %q, not this API's own document (%d bytes). "+
			"zip's auto-mounted projection won the address; a generator reading it "+
			"emits an empty client and reports success.", openapi.WellKnown, got, len(body))
	}
}

// TestBothAddressesAreOneDocument: alias, not second document. They are served
// from one render, so a difference here means the render ran twice and the two
// answers are free to diverge as the route table does.
func TestBothAddressesAreOneDocument(t *testing.T) {
	a := app()
	openapi.Use(a, openapi.Info{Title: "Hanzo Cloud", Version: "v1"})

	get := served(t, a)
	_, canonical := get(openapi.Path)
	_, alias := get(openapi.WellKnown)
	if string(canonical) != string(alias) {
		t.Fatalf("%s and %s describe different APIs (%d vs %d bytes)",
			openapi.Path, openapi.WellKnown, len(canonical), len(alias))
	}
}

// TestTheAliasIsPublishedAndOpen: it is a second address in the contract, so it
// owes the two declarations Path owes — prose, and no credential. Silence on
// either is what an SDK method that cannot say what it is, or a discovery
// endpoint a client must already hold a token to read, is made of.
func TestTheAliasIsPublishedAndOpen(t *testing.T) {
	a := app()
	openapi.Use(a, openapi.Info{Title: "Hanzo Cloud", Version: "v1"})

	doc, err := openapi.Spec(a, openapi.Info{Title: "Hanzo Cloud", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	item, ok := doc.Paths[openapi.WellKnown]
	if !ok {
		t.Fatalf("%s is not in the document this process publishes", openapi.WellKnown)
	}
	op, ok := item["get"]
	if !ok {
		t.Fatalf("%s publishes no GET", openapi.WellKnown)
	}
	if op.Description == "" || op.Summary == "" {
		t.Fatalf("%s publishes summary=%q description=%q", openapi.WellKnown, op.Summary, op.Description)
	}
	if op.Security == nil || len(*op.Security) != 0 {
		t.Fatalf("%s inherits the fleet credential requirement — a client must be able to "+
			"read the contract before it holds a credential", openapi.WellKnown)
	}
}
