package openapi_test

// The address ratchet, tested on the shape of the failure it exists to catch: a
// route landing under another capability's name with every other gate green.

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
)

const misfiledPath = "misfiled.txt"

func filed(paths map[string]string) *openapi.Document {
	d := &openapi.Document{Paths: map[string]openapi.PathItem{}}
	for path, app := range paths {
		d.Paths[path] = openapi.PathItem{"get": &openapi.Operation{App: app}}
	}
	return d
}

func TestMisfileReadsTheRuleOffTheAddress(t *testing.T) {
	got := openapi.Misfile(filed(map[string]string{
		"/v1/todo/projects":                 "todo",      // the owner's name: clean
		"/v1/machines":                      "visor",     // another app's noun
		"/v1/billing/invoices":              "commerce",  // a shared address
		"/v1/{wildcard1}":                   "ai",        // the bare remainder
		"/git/hanzoai/cloud":                "git",       // outside /v1 entirely
		"/v1/chat/completions":              "ai",        // the wire, served by ai: exempt
		"/v1/chat/completions/x":            "exec",      // the wire, served by anyone else: not
		"/.well-known/openid-configuration": "iam",       // RFC 8615: exempt
		"/v1/admin/pricing/catalog":         "pricing",   // the operator's view of pricing: exempt
		"/v1/admin/apps":                    "admin",     // admin's own: clean
		"/v1/admin/author":                  "referrals", // the operator's view of a DIFFERENT app
	}))
	want := openapi.Misfiled{
		"/git git",
		"/v1 ai",
		"/v1/admin referrals",
		"/v1/billing commerce",
		"/v1/chat exec",
		"/v1/machines visor",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Misfile =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestShrinkRefusesAPairTheFileDoesNotCarry(t *testing.T) {
	was := openapi.Misfiled{"/v1/machines visor"}
	_, err := was.Shrink(openapi.Misfiled{"/v1/machines visor", "/v1/gpus visor"})
	if err == nil {
		t.Fatal("Shrink accepted a new misfiled address — the file grew silently")
	}
	for _, want := range []string{"/v1/gpus visor", "misfiled.txt", "HIP-0139"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not name %q:\n%s", want, err)
		}
	}
}

func TestShrinkKeepsOnlyWhatIsStillMeasured(t *testing.T) {
	was := openapi.Misfiled{"/v1/gpus visor", "/v1/machines visor"}
	kept, err := was.Shrink(openapi.Misfiled{"/v1/machines visor"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kept, openapi.Misfiled{"/v1/machines visor"}) {
		t.Fatalf("kept = %v; a pair the document no longer has leaves the file", kept)
	}
}

// TestNoOperationIsMisfiled is the gate, on the real fleet: every (address, app)
// pair the woven document carries is in misfiled.txt, and every line there is
// still measured. With -weave it regenerates the file (shrinking only); without,
// it verifies. Same dual mode as the golden, for the same reason.
func TestNoOperationIsMisfiled(t *testing.T) {
	subsets, err := openapi.Subsets(manifest.Names(), fromTree, manifest.StageOf)
	if err != nil {
		t.Fatal(err)
	}
	woven, err := openapi.Fleet(subsets)
	if err != nil {
		t.Fatalf("weave: %v", err)
	}
	was, err := openapi.ReadMisfiled(misfiledPath)
	if err != nil {
		t.Fatal(err)
	}
	now := openapi.Misfile(woven)
	// The first regeneration SEEDS the file with what is measured; every one
	// after may only shrink it. An absent file is the seed case, an empty one is
	// a clean fleet, and only the second refuses growth.
	if _, err := os.Stat(misfiledPath); os.IsNotExist(err) && *weaveOut != "" {
		was = now
	}
	kept, err := was.Shrink(now)
	if err != nil {
		t.Fatal(err)
	}
	if *weaveOut != "" {
		if err := kept.Write(misfiledPath); err != nil {
			t.Fatalf("write %s: %v", misfiledPath, err)
		}
		t.Logf("wrote %s (%d misfiled address/app pairs, was %d)", misfiledPath, len(kept), len(was))
		return
	}
	// slices.Equal, not reflect.DeepEqual: the two carry the same LINES or they
	// do not, and a nil slice and an empty one are the same set of lines. DeepEqual
	// says otherwise, and the one state where that difference exists is the state
	// this ratchet is FOR — an empty file reads back as nil, a clean document
	// measures as an empty slice, and the gate failed "carries 0 line(s)" the first
	// time the fleet reached zero. A gate that cannot pass at its own goal is a
	// gate that would have been switched off there.
	if !slices.Equal(was, kept) {
		t.Fatalf("%s carries %d line(s) the document no longer has — run `make openapi` and commit it:\n  %s",
			misfiledPath, len(was)-len(kept), strings.Join(was, "\n  "))
	}
}

// TestTheRatchetPassesAtZero pins the terminal state directly, because it is the
// one the fleet is now IN and the one the gate has the least practice at: no
// misfiled pairs measured, and a committed file that is all header. Both sides of
// that comparison arrive by a different route to the same emptiness.
func TestTheRatchetPassesAtZero(t *testing.T) {
	clean, err := (openapi.Misfiled)(nil).Shrink(openapi.Misfile(filed(map[string]string{
		"/v1/todo/projects": "todo",
	})))
	if err != nil {
		t.Fatalf("a document with nothing misfiled was refused: %v", err)
	}
	if len(clean) != 0 {
		t.Fatalf("Shrink = %v, want nothing", clean)
	}
	if !slices.Equal(openapi.Misfiled(nil), clean) {
		t.Fatal("an empty file and a clean document compare unequal — the ratchet cannot reach zero")
	}
}
