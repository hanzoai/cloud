// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// gql posts one GraphQL request to an org's graph as the gateway forwards it.
func gql(t *testing.T, base, org, token, query string) (int, map[string]any, string) {
	t.Helper()
	code, body := call(t, http.MethodPost, base+"/v1/base/graphql", org, token, map[string]any{"query": query})
	var out struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(body, &out)
	var msgs []string
	for _, e := range out.Errors {
		msgs = append(msgs, e.Message)
	}
	return code, out.Data, strings.Join(msgs, " | ")
}

// An org's Base answers GraphQL at /v1/base/graphql with its own schema: the
// CRM an org installed is typed there, a write and a read go through that org's
// Base, another org's schema lacks what it never installed, and a token of
// another org is refused by the Base it asked.
func TestAnOrgsBaseAnswersGraphQL(t *testing.T) {
	i := newIAM(t)
	base := serveBases(t, i)
	admin := i.token(t, "ann", "acme", "admin")
	zoo := i.token(t, "zed", "zoo", "owner")

	if code, body := call(t, http.MethodPost, base+"/v1/base/modules/crm", "acme", admin, nil); code != http.StatusOK {
		t.Fatalf("install: %d %s", code, body)
	}

	code, sdl := call(t, http.MethodGet, base+"/v1/base/graphql", "acme", admin, nil)
	if code != http.StatusOK || !strings.Contains(string(sdl), "type BasePerson {") {
		t.Fatalf("acme's schema: %d %.200s", code, sdl)
	}
	code, other := call(t, http.MethodGet, base+"/v1/base/graphql", "zoo", zoo, nil)
	if code != http.StatusOK || strings.Contains(string(other), "BasePerson") {
		t.Fatalf("zoo's schema names what only acme installed: %d %.200s", code, other)
	}

	code, data, errs := gql(t, base, "acme", admin, `mutation { createCompany(data: {name: "Acme", employees: 50}) { id name created_by { name } } }`)
	if code != http.StatusOK || errs != "" {
		t.Fatalf("create: %d %s", code, errs)
	}
	company := data["createCompany"].(map[string]any)
	if company["name"] != "Acme" || company["created_by"].(map[string]any)["name"] != "ann" {
		t.Fatalf("created %v", company)
	}

	_, data, errs = gql(t, base, "acme", admin, `{ companies(filter: {employees: {gte: 10}}) { totalCount edges { node { name } } } }`)
	if errs != "" || data["companies"].(map[string]any)["totalCount"] != float64(1) {
		t.Fatalf("read: %v %s", data, errs)
	}

	// what the graph wrote is the org's Base's own record
	code, body := call(t, http.MethodGet, base+"/v1/base/collections/companies/records/"+company["id"].(string), "acme", admin, nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"name":"Acme"`) {
		t.Fatalf("REST reads %d %s", code, body)
	}

	// refused by REST, and beyond its reach in the graph, which does not even
	// say acme has companies
	if code, body := call(t, http.MethodGet, base+"/v1/base/collections/companies/records", "acme", zoo, nil); code != http.StatusForbidden {
		t.Fatalf("REST answered zoo's token %d %s", code, body)
	}
	_, data, errs = gql(t, base, "acme", zoo, `{ companies { totalCount } }`)
	if !strings.Contains(errs, `no query field "companies"`) || data["companies"] != nil {
		t.Fatalf("acme's graph answered zoo's token %v %q", data, errs)
	}

	if code, _ := call(t, http.MethodGet, base+"/v1/base/graphql", "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("nobody signed in read a schema: %d", code)
	}
}
