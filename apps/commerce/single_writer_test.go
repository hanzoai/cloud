// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// single_writer_test.go — CLOUD HOLDS NO PAYMENT PROCESSING AND WRITES NO SETTLEMENT.
//
// Commerce is the one orchestration layer for money: it calls Stripe, Square and PayPal,
// verifies their webhooks, settles, and posts the spendable credit to the ledger. This
// binary is the host: it reads the ledger and consumes commerce's answers. Two writers
// of one payment's credit is how a balance ends up depending on which ran, so the rule
// is held by the source, not by review: this reads every non-test Go file of the module
// and fails when one imports a processor's SDK, names a processor's API host, or
// credits the ledger with money a payment funded outside the one adapter that commerce
// itself drives (ledger.go).
//
// It reads syntax, not text, so a comment that explains why cloud no longer does
// something does not trip it, and a string a binary would actually dial does.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// processors are the import paths and hosts that mean "this code takes a payment".
var processors = []string{
	"github.com/stripe/", "stripe.com", "squareup.com", "squareupsandbox.com",
	"github.com/square/", "paypal.com", "github.com/plutov/paypal", "braintreegateway.com",
}

// settlers are the one adapter allowed to carry a payment's credit into the ledger.
var settlers = map[string]bool{"apps/commerce/ledger.go": true}

// notPayments are the files that name a processor for something that is not taking a
// payment for Hanzo, each with the reason. Adding to this list is the decision this
// test exists to force into review.
var notPayments = map[string]string{
	"apps/provider/payments.go": "verifies a customer's own API key; no Hanzo payment",
	"idv/config.go":             "Stripe Identity as an ID-verification vendor; no Hanzo payment",
}

// A reason that outlives its file is a hole nobody can see: every entry names a file
// that exists, by exact path, and there are exactly two.
func TestTheAllowlistNamesOnlyFilesThatExist(t *testing.T) {
	root := moduleRoot(t)
	if len(notPayments) != 2 {
		t.Fatalf("the allowlist holds %d entries, want exactly 2", len(notPayments))
	}
	for path := range notPayments {
		if strings.ContainsAny(path, "*?[") || strings.HasSuffix(path, "/") || !strings.HasSuffix(path, ".go") {
			t.Errorf("%q is not an exact file path", path)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Errorf("the allowlist names %s, which does not exist: %v", path, err)
		}
	}
}

// credits reports whether t names a ledger credit's input.
func credits(t ast.Expr) bool {
	switch v := t.(type) {
	case *ast.SelectorExpr:
		return v.Sel.Name == "DepositInput" || v.Sel.Name == "CreditInput"
	case *ast.Ident:
		return v.Name == "DepositInput" || v.Name == "CreditInput"
	}
	return false
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if parent := filepath.Dir(dir); parent != dir {
			dir = parent
			continue
		}
		t.Fatal("no go.mod above the test")
	}
}

// offences reads src (named name, relative to the module) and says what it does that
// this host must not.
func offences(name string, src any) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		return []string{name + ": does not parse: " + err.Error()}
	}
	var out []string
	hit := func(pos token.Pos, what string) {
		out = append(out, name+":"+strconv.Itoa(fset.Position(pos).Line)+": "+what)
	}
	named := func(s string) bool {
		s = strings.ToLower(s)
		for _, p := range processors {
			if strings.Contains(s, p) {
				return true
			}
		}
		return false
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.ImportSpec:
			if p, _ := strconv.Unquote(v.Path.Value); named(p) {
				hit(v.Pos(), "imports a payment processor: "+p)
			}
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				if s, _ := strconv.Unquote(v.Value); strings.Contains(s, "://") && named(s) {
					hit(v.Pos(), "names a payment processor's address: "+s)
				}
			}
		case *ast.CompositeLit:
			// A credit that says a PAYMENT funded it: a DepositInput or CreditInput with
			// Cash: true. (Cash on a spend-lane grant is a different fact.)
			if !credits(v.Type) || settlers[name] {
				break
			}
			for _, e := range v.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Cash" {
					if val, ok := kv.Value.(*ast.Ident); ok && val.Name == "true" {
						hit(v.Pos(), "credits the ledger with money a payment funded (Cash: true); commerce posts settlements")
					}
				}
			}
		}
		return true
	})
	return out
}

func TestCloudHoldsNoPaymentProcessingAndWritesNoSettlement(t *testing.T) {
	root := moduleRoot(t)
	var all []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if _, ok := notPayments[filepath.ToSlash(rel)]; ok {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		all = append(all, offences(filepath.ToSlash(rel), src)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range all {
		t.Error(o)
	}
}

// The check itself sees what it is for: each of these is the shape of a regression,
// and a check that passes them is a check that proves nothing.
func TestTheSingleWriterCheckSeesEachRegression(t *testing.T) {
	for name, src := range map[string]string{
		"imports the SDK":       `package x; import _ "github.com/stripe/stripe-go/v79"`,
		"dials Stripe":          `package x; var u = "https://api.stripe.com/v1/payment_intents"`,
		"dials Square":          `package x; var u = "https://connect.squareup.com/v2/payments"`,
		"dials PayPal":          `package x; var u = "https://api-m.paypal.com/v2/checkout/orders"`,
		"deposits a settlement": `package x; var d = money.DepositInput{Ref: "r", Cash: true}`,
	} {
		if got := offences("apps/foo/x.go", src); len(got) != 1 {
			t.Errorf("%s: %v, want one offence", name, got)
		}
	}
	if got := offences("apps/commerce/ledger.go", `package x; var d = money.DepositInput{Cash: true}`); len(got) != 0 {
		t.Errorf("the ledger adapter is the one allowed carrier: %v", got)
	}
	if got := offences("apps/foo/x.go", `package x; var g = Grant{Cash: true}`); len(got) != 0 {
		t.Errorf("a spend-lane grant is not a credit: %v", got)
	}
	if got := offences("apps/foo/x.go", "package x\n// https://api.stripe.com is commerce's now\nvar a = \"a\""); len(got) != 0 {
		t.Errorf("a comment tripped it: %v", got)
	}
}
