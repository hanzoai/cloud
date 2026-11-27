package client_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/zap-proto/zip"
)

const orgSDL = `schema { query: BaseQuery mutation: BaseMutation }

type BasePerson {
  id: ID!
  name: String
}

type BaseQuery {
  people: [BasePerson!]!
}

type BaseMutation {
  createPerson(name: String): BasePerson
}
`

// orgBase stands in for apps/base: the org's schema on GET, and on POST the
// document it was sent, answered with a person whose name failed.
func orgBase(t *testing.T) (addr string, asked *[]string) {
	t.Helper()
	addr = filepath.Join(kit.Sockets(t), "base.sock")
	var docs []string
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw(zip.MethodAll, "/*", func(c *zip.Ctx) error {
		fc := c.Fiber()
		if string(fc.Path()) != "/v1/base/graphql" {
			return c.Bytes(404, []byte(`{"message":"no"}`))
		}
		if fc.Method() == http.MethodGet {
			return c.Bytes(200, []byte(orgSDL))
		}
		docs = append(docs, string(fc.Body()))
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(200, []byte(`{"data":{"people":[{"__typename":"BasePerson","id":"p1","name":null}]},`+
			`"errors":[{"message":"name: not yours","path":["people",0,"name"]}]}`))
	})
	go func() { _ = app.Listen(addr) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, addr)
	return addr, &docs
}

// The surface graph carries the caller's org Base as `base`: its schema under
// Query and Mutation, and a `base { … }` subtree sent there as one document,
// answered with the Base's own type names and its errors at their paths.
func TestTheOrgBaseIsTheBaseField(t *testing.T) {
	addr, docs := orgBase(t)
	h := endpointTo(t, "base", addr)

	req := httptest.NewRequest(http.MethodGet, openapi.GraphPath, nil)
	req.Header.Set("X-Org-Id", "acme")
	resp, err := h.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	sdl := string(raw)
	if err := zip.CheckSchema(sdl); err != nil {
		t.Fatalf("the composed schema does not check: %v", err)
	}
	for _, want := range []string{"base: BaseQuery", "base: BaseMutation", "type BasePerson {"} {
		if !strings.Contains(sdl, want) {
			t.Errorf("the schema lacks %q", want)
		}
	}
	if strings.Contains(sdl, "schema {") {
		t.Error("the Base's schema block reached the surface's schema")
	}

	res := run(t, h, `{ base { __typename people { __typename id name } } other: base { people { id } } }`, nil)
	if len(*docs) != 2 || !strings.Contains(strings.Join(*docs, " "), `query { __typename people{ __typename id name } }`) {
		t.Fatalf("the Base was asked %q", *docs)
	}
	b := res.Data["base"].(map[string]any)
	person := b["people"].([]any)[0].(map[string]any)
	if b["__typename"] != "BaseQuery" || person["__typename"] != "BasePerson" || person["id"] != "p1" {
		t.Fatalf("answered %v", res.Data)
	}
	var paths []string
	for _, e := range res.Errors {
		paths = append(paths, e.Message+" at "+fmt.Sprint(e.Path))
	}
	if all := strings.Join(paths, "; "); len(paths) != 1 || all != "name: not yours at [base people 0 name]" {
		t.Fatalf("errors %+v", res.Errors)
	}

	run(t, h, `mutation { base { createPerson(name: "Ada \"Countess\"") { id } } }`, nil)
	if got := (*docs)[2]; !strings.Contains(got, `mutation { createPerson(name: \"Ada \\\"Countess\\\"\"){ id } }`) {
		t.Fatalf("the mutation went as %s", got)
	}
}
