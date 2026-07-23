package cloudflare

// d1.go — Cloudflare D1, WIRED (account-scoped; upstream path is singular
// /accounts/{id}/d1/database, our route is plural /d1/databases). List is a read;
// create/delete are mutations (authWrite → org admin); a query is a mutation too — a
// D1 query can INSERT/UPDATE/DROP, so it takes the write gate, not the read gate.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// d1Path is the upstream account-scoped D1 base (singular "database", per the CF API).
func d1Path(acct string) string { return "/accounts/" + acct + "/d1/database" }

func d1DatabaseList(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctClient(s, c)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, d1Path(acct)+query(c, "page", "per_page", "name"), nil)
}

func d1DatabaseCreate(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctWrite(s, c)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	name := strings.TrimSpace(in.Name)
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("database name is invalid")
	}
	return cl.pass(c, http.MethodPost, d1Path(acct), map[string]string{"name": name})
}

func d1DatabaseDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctWrite(s, c)
	if err != nil {
		return err
	}
	db, err := pathSeg(c, "database", nameRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodDelete, d1Path(acct)+"/"+db, nil)
}

// d1Query runs a SQL statement against a database. The body ({sql, params}) is
// validated for a non-empty sql then forwarded VERBATIM (preserving params and any
// batch fields), so the full CF query shape reaches D1 without field loss.
func d1Query(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctWrite(s, c)
	if err != nil {
		return err
	}
	db, err := pathSeg(c, "database", nameRE)
	if err != nil {
		return err
	}
	var in struct {
		SQL string `json:"sql"`
	}
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	if strings.TrimSpace(in.SQL) == "" {
		return zip.ErrBadRequest("sql is required")
	}
	return cl.pass(c, http.MethodPost, d1Path(acct)+"/"+db+"/query", json.RawMessage(c.Body()))
}
