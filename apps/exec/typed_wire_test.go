package exec

// typed_wire_test.go is the ledger, plus the one assertion the conversion turns on.
//
// This surface was 0 typed of 56 published operations when it was a transparent
// proxy. It is 2 of 5 now, and the second one — the file listing — is worth reading
// for HOW it converted: its stated reason was that "the client reads a BARE JSON
// ARRAY and an object wrapper would be a wire change on a contract this repo does
// not own". Both halves are true, and the conclusion did not follow. A named SLICE
// type publishes an array and marshals to one, so the wrapper was never required —
// apps/provisioning had already found the same thing for its own listing.

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// untypedByDesign is the CLOSED list. Every entry is a fact about the CALLERS' wire
// — hanzo.chat's code interpreter and @hanzochat/agents — rather than about
// ownership, because the executor moved in-process and the wire did not.
var untypedByDesign = map[string]string{
	"POST /v1/exec/upload": "a MULTIPART upload (c.Fiber().FormFile(\"file\")): the body IS the file. " +
		"zip decodes every non-empty typed body with jsonenc.Unmarshal before the handler is entered, " +
		"so a typed In would answer 400 to every real upload.",
	"GET /v1/exec/download/{wildcard1}": "answers the file's BYTES under its own Content-Type " +
		"(c.Bytes), and a typed op's only response path is c.JSON(out). It is ALSO on a fiber " +
		"wildcard — the identifier is two segments, {session}/{id} — which the typed registry " +
		"publishes verbatim while the router renders {wildcardN}, and Fold refuses the whole document " +
		"on that disagreement.",
	"POST /v1/exec/programmatic": "a different PROTOCOL, not a different shape: a run suspended on " +
		"each tool call and resumed from a continuation token (@hanzochat/agents' programmatic tool " +
		"calling). It answers an unconditional 501 naming what is missing, and a permanent stub " +
		"declares nothing — the apps/books precedent.",
}

// TestTheListingIsStillABareArray is the assertion the conversion turns on.
//
// The client does `response.data.find(...)` over the BODY, so an object wrapper —
// which is the obvious typed shape — would break it silently: the request still
// succeeds and the caller finds nothing, which reads as a session holding no files
// rather than as a wire change. A named slice keeps the array.
//
// It asserts the MARSHALLED BYTES rather than the Go value, because the wire is
// what the client reads, and it drives the empty case as well as the full one: an
// empty listing must be `[]` and never `null`, or a client iterating the answer
// crashes on the one response it is most likely to get first.
func TestTheListingIsStillABareArray(t *testing.T) {
	out := make(listings, 0, 2)
	raw, err := json.Marshal(&out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("an EMPTY listing marshals to %s, want [] — `null` crashes a client that iterates "+
			"the answer, and that is the first response a new session gives", raw)
	}

	out = append(out,
		listing{Name: "s1/b.txt", LastModified: "2026-01-02T03:04:05Z"},
		listing{Name: "s1/a.txt", LastModified: "2026-01-02T03:04:06Z"})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	raw, err = json.Marshal(&out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.HasPrefix(string(raw), "[{") {
		t.Fatalf("the listing is no longer a BARE array: %s — the client reads .find() over the body, "+
			"so an object wrapper makes every lookup miss and reads as an empty session", raw)
	}
	var back []map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the answer does not decode as an array: %v — %s", err, raw)
	}
	if len(back) != 2 || back[0]["name"] != "s1/a.txt" {
		t.Errorf("rows or order moved: %s", raw)
	}
	// `name` carries the {session}/{id} identifier WHOLE, because the client matches
	// on it as a PREFIX. Trimming it to the bare file name is the change that reads
	// as "the file expired".
	if !strings.HasPrefix(back[0]["name"].(string), "s1/") {
		t.Errorf("name lost its session prefix: %v", back[0]["name"])
	}
}
