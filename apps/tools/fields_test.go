package tools

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
)

// testMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey):
// it opens an address a file held before KMS.
var testMaster = bytes.Repeat([]byte{0x5a}, 32)

// testKMS is the KMS the stores most tests open keep their addresses in.
var testKMS = kmstest.New()

func rawURL(t *testing.T, s *MCPServerStore, id string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`SELECT url FROM mcp_servers WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatalf("read url: %v", err)
	}
	return v
}

// freshServers is a store over its own KMS, which a test reads what the store keeps from.
func freshServers(t *testing.T) (*MCPServerStore, *kmstest.Mem) {
	t.Helper()
	k := kmstest.New()
	s, err := OpenMCPServerStore(t.TempDir(), testMaster, k)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, k
}

// A server's address may carry its credential in the query, so it is a KMS value: the row
// holds its path and no byte of it, every read gives it back whole, a revise writes the
// new address to the same path, and a delete asks KMS to forget it.
func TestAServerAddressLivesInKMS(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, k := freshServers(t)
		ctx := context.Background()
		const hook = "https://mcp.vendor.example/sse?api_key=sk-live-0f3a9c"
		const ref = "orgs/acme/tools-mcp/m1/url@default"
		srv, err := s.Write(ctx, MCPServer{ID: "m1", Org: "acme", Name: "Vendor", URL: hook, AuthHeader: "Authorization"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if v := rawURL(t, s, "m1"); v != "kms:"+ref {
			t.Fatalf("url column holds %q, want its KMS path", v)
		}
		if held := k.Held(); len(held) != 1 || held[ref] != hook {
			t.Fatalf("KMS holds %v, want the address at its path", held)
		}
		got, err := s.Get(ctx, "acme", "m1")
		if err != nil || got.URL != hook || got.Name != srv.Name {
			t.Fatalf("Get = %+v, %v", got, err)
		}
		if list, err := s.List(ctx, "acme"); err != nil || len(list) != 1 || list[0].URL != hook {
			t.Fatalf("List = %+v, %v", list, err)
		}
		// A revise writes the new address to the same path, and the read that follows is not
		// served the old one.
		const moved = "https://mcp.vendor.example/v2?api_key=sk-live-7b21"
		srv.URL = moved
		if _, err := s.Write(ctx, srv, false); err != nil {
			t.Fatal(err)
		}
		if v := rawURL(t, s, "m1"); v != "kms:"+ref || k.Held()[ref] != moved {
			t.Fatalf("after a revise the column holds %q and KMS %q", v, k.Held()[ref])
		}
		if got, err := s.Get(ctx, "acme", "m1"); err != nil || got.URL != moved {
			t.Fatalf("after a revise: %+v, %v", got, err)
		}
		// The address is kept a minute: a second read asks KMS for nothing.
		reads := k.Reads()
		if _, err := s.List(ctx, "acme"); err != nil || k.Reads() != reads {
			t.Fatalf("a second list read KMS (%d reads, then %d): %v", reads, k.Reads(), err)
		}

		if ok, err := s.Delete(ctx, "acme", "m1"); err != nil || !ok {
			t.Fatalf("Delete = %v, %v", ok, err)
		}
		if refs := k.Refs(); len(refs) != 0 {
			t.Fatalf("a deleted server left %v in KMS", refs)
		}
		if _, err := s.Get(ctx, "acme", "m1"); !errors.Is(err, ErrUnknownTool) {
			t.Fatalf("a deleted server read as %v", err)
		}
	})
}

// A reference names its own server's address: a column pointed at another org's path is
// refused by KMS, and one that names nothing KMS holds is a dangling reference.
func TestAnAddressReferenceReadsOnlyItsOwnValue(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _ := freshServers(t)
		ctx := context.Background()
		for _, w := range []struct{ org, id string }{{"acme", "a"}, {"globex", "g"}} {
			if _, err := s.Write(ctx, MCPServer{ID: w.id, Org: w.org, Name: w.id, URL: "https://" + w.id + ".example/mcp"}, true); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`UPDATE mcp_servers SET url = $1 WHERE id = 'a'`, rawURL(t, s, "g")); err != nil {
			t.Fatal(err)
		}
		s.urls.Clear() // the address a write has just made is kept a minute; this reads the row
		if got, err := s.Get(ctx, "acme", "a"); err == nil || got.URL != "" {
			t.Fatalf("acme's server dialled globex's address: %+v, %v", got, err)
		}
		if _, err := s.db.Exec(`UPDATE mcp_servers SET url = 'kms:orgs/acme/tools-mcp/a/nothing@default' WHERE id = 'a'`); err != nil {
			t.Fatal(err)
		}
		s.urls.Clear()
		if _, err := s.Get(ctx, "acme", "a"); !errors.Is(err, escrow.ErrGone) {
			t.Fatalf("a dangling reference read as %v, want ErrGone", err)
		}
	})
}

// With no KMS an address is neither kept nor read, and the server is not written.
func TestNoKMSKeepsNoAddress(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := OpenMCPServerStore(t.TempDir(), testMaster, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		_, err = s.Write(context.Background(), MCPServer{ID: "a", Org: "acme", Name: "a", URL: "https://a.example/mcp?key=k"}, true)
		if !errors.Is(err, escrow.ErrNoKMS) {
			t.Fatalf("Write with no KMS = %v, want ErrNoKMS", err)
		}
		if n, err := s.Count(context.Background(), "acme"); err != nil || n != 0 {
			t.Fatalf("a refused write left %d rows (%v)", n, err)
		}
	})
}

// A file written before KMS holds the address in plaintext or sealed as a field. The open
// writes each to KMS and replaces the column with its path, once. Built by hand on both
// engines: the rows are the old shapes in the current table.
func TestALegacyAddressMovesToKMSOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, k := freshServers(t)
		ctx := context.Background()
		c, err := tenure.NewCipher(testMaster)
		if err != nil {
			t.Fatal(err)
		}
		f, err := c.Fields(namespace.System(), mcpSubsystem)
		if err != nil {
			t.Fatal(err)
		}
		r := f.Row("mcp_servers", "acme", "sealed")
		sealed := r.Seal("url", "https://sealed.example/sse?key=sealed-key-456")
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
		for _, w := range []struct{ id, url string }{{"plain", "https://plain.example/sse?key=legacy-key-123"}, {"sealed", sealed}} {
			if _, err := s.db.Exec(`INSERT INTO mcp_servers (id, org, name, url, created_at) VALUES ($1, 'acme', $1, $2, 1)`, w.id, w.url); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.moveToKMS(ctx); err != nil {
			t.Fatalf("move: %v", err)
		}
		for id, want := range map[string]string{"plain": "https://plain.example/sse?key=legacy-key-123", "sealed": "https://sealed.example/sse?key=sealed-key-456"} {
			if v := rawURL(t, s, id); v != "kms:orgs/acme/tools-mcp/"+id+"/url@default" || strings.Contains(v, "key") {
				t.Fatalf("%s: the url column holds %q, want its KMS path", id, v)
			}
			if got, err := s.Get(ctx, "acme", id); err != nil || got.URL != want {
				t.Fatalf("%s after the move: %+v, %v, want %q", id, got, err, want)
			}
		}
		before, reads := k.Held(), k.Reads()
		cols := rawURL(t, s, "plain") + rawURL(t, s, "sealed")
		if err := s.moveToKMS(ctx); err != nil {
			t.Fatal(err)
		}
		if after := k.Held(); len(after) != len(before) || k.Reads() != reads {
			t.Fatalf("a second move touched KMS: %d values, then %d; reads %d then %d", len(before), len(after), reads, k.Reads())
		}
		if cols != rawURL(t, s, "plain")+rawURL(t, s, "sealed") {
			t.Fatal("a second move rewrote a column")
		}
	})
}
