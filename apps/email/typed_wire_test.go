package email

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// This file makes the email surface's typed partition a GATE instead of a
// paragraph: a route added tomorrow as a raw func(*zip.Ctx) error would leave the
// claim standing and the route invisible to every projection.

// untypedByDesign is the CLOSED list of email operations that are NOT typed ops,
// each with the wire fact that keeps it raw. The address is written the way the
// DOCUMENT writes it, which is the identity every projection keys on.
var untypedByDesign = map[string]string{
	"POST /v1/email/webhooks/ses": "Amazon SNS posts the SES event as text/plain, which a typed op's JSON " +
		"body refuses, and the caller is Amazon: the only credential is the signature, verified " +
		"before any field is trusted, and every refusal is the one 401.",
	"POST /v1/email/webhooks/sendgrid": "SendGrid signs the timestamp header followed by the BYTES of the " +
		"body, so the signature is checked against the raw body before it is parsed, and the body is an " +
		"array of events, which a typed op's object input cannot read.",
}

// emailOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry.
func emailOps(t *testing.T) (served map[string]bool, typed map[string]string, schemas map[string]any) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	doc, err := spec.Of(app, spec.Info{Title: "email", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return p == "/v1/email" || strings.HasPrefix(p, "/v1/email/") }
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}
	return served, typed, reg.Schemas
}

// TestEveryRouteIsTypedOrNamed fails when an email operation is neither a typed op
// nor named above, and when the two ledgers stop summing to the served surface.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed, _ := emailOps(t)

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no MCP tool, no CLI command and no SDK method. "+
			"Convert it, or add it to untypedByDesign with the wire fact that keeps it raw.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which email no longer serves", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	if len(served) != 10 || len(typed) != 8 {
		t.Errorf("served = %d (want 10), typed = %d (want 8)", len(served), len(typed))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary: it is
// the OpenAPI description and the MCP tool description a model reads.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed, _ := emailOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed email ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/email/...", key)
		}
	}
}

// TestEveryPublishedFieldIsDescribed holds the FIELDS of every published shape to
// the same bar as the operations.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	_, _, schemas := emailOps(t)
	var bare []string
	for name, raw := range schemas {
		sch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		props, ok := sch["properties"].(map[string]any)
		if !ok {
			continue
		}
		for field, praw := range props {
			p, ok := praw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, name+"."+field)
			}
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) with no description: %s", strings.Join(bare, ", "))
	}
}
