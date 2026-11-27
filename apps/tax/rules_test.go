package tax

// rules_test.go holds the law to its sources: every rule in rules.go, every TIN
// format rule in tin.go, and the IRIS file rules in iris.go, each against the
// case that decides it. No I/O.

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// pay is one payment of cents from a to b on rail, as the event plane answers it.
// A chain payment names its chain and transaction; tx puts any other rail's
// payment on a chain.
func pay(rail string, cents int64, tx ...string) client.EconomicEvent {
	usd := money.FromCents(cents).String()
	e := client.EconomicEvent{V: client.EconomicVersion, Buyer: "a", Seller: "b", Currency: "USD",
		Gross: usd, FMV: usd, Rail: rail, Proof: fmt.Sprintf("p_%s_%d", rail, cents), At: 1767225600}
	if rail == client.RailChain {
		tx = append(tx, "0xabc")
	}
	if len(tx) > 0 {
		e.Currency, e.Chain, e.Tx = "USDC", "eip155:8453", tx[0]
	}
	e.ID = client.EconomicID(rail, e.Proof)
	return e
}

func codes(v Verdict) string {
	var out []string
	for _, r := range v.Rules {
		out = append(out, r.Code)
	}
	return strings.Join(out, ",")
}

// Each rule, against the payment and payee that decide it, and the box it lands in.
func TestClassifyEachRule(t *testing.T) {
	corp := Payee{Category: Services, W9: true, Classification: CCorp}
	person := Payee{Category: Services, W9: true, Classification: Individual}
	for _, tc := range []struct {
		name       string
		p          client.EconomicEvent
		pe         Payee
		reportable bool
		box        string
		rules      string
	}{
		// A card or third-party network payment is the settlement entity's to report
		// on 1099-K — whoever the payee is.
		{"card is 1099-K", pay(client.RailCard, 500000), person, false, "", "card"},
		{"network is 1099-K", pay(client.RailNetwork, 500000), person, false, "", "network"},
		// A C corporation is exempt; so is an LLC taxed as one; so is exempt payee code 5.
		{"c corp exempt", pay(client.RailX402, 500000), corp, false, "", "corporation"},
		{"llc taxed as s corp exempt", pay(client.RailX402, 500000), Payee{Category: Services, W9: true, Classification: LLCS}, false, "", "corporation"},
		{"exempt code 5 is a corporation", pay(client.RailX402, 500000), Payee{Category: Services, W9: true, Classification: Partnership, ExemptPayee: "5"}, false, "", "corporation"},
		{"exempt code 1 exempt", pay(client.RailX402, 500000), Payee{Category: Services, W9: true, Classification: TrustEstate, ExemptPayee: "1"}, false, "", "exempt_payee"},
		// …except for legal services, which are reported to a corporation too.
		{"attorney corp reported", pay(client.RailX402, 500000), Payee{Category: Attorney, W9: true, Classification: CCorp}, true, "1099-NEC 1a", "corporation,attorney"},
		// A digital asset is reported at its fair market value on the date paid.
		{"digital asset at FMV", pay(client.RailChain, 500000), person, true, "1099-NEC 1a", "digital_asset,reportable"},
		{"digital asset to a corp still exempt", pay(client.RailChain, 500000), corp, false, "", "digital_asset,corporation"},
		{"an x402 payment settled on a chain is a digital asset", pay(client.RailX402, 500000, "0xdef"), person, true, "1099-NEC 1a", "digital_asset,reportable"},
		// Every other rail is reported like a ledger payment.
		{"wire reportable", pay(client.RailWire, 500000), person, true, "1099-NEC 1a", "reportable"},
		{"ach reportable", pay(client.RailACH, 500000), person, true, "1099-NEC 1a", "reportable"},
		{"ledger reportable", pay(client.RailLedger, 500000), person, true, "1099-NEC 1a", "reportable"},
		{"merchandise not reported", pay(client.RailX402, 500000), Payee{Category: Merchandise, W9: true, Classification: Individual}, false, "", "merchandise"},
		{"no W-9 is reportable", pay(client.RailX402, 500000), Payee{Category: Services}, true, "1099-NEC 1a", "no_w9"},
		{"rents to MISC 1", pay(client.RailX402, 500000), Payee{Category: Rents, W9: true, Classification: Partnership}, true, "1099-MISC 1", "reportable"},
		{"royalties to MISC 2", pay(client.RailX402, 500000), Payee{Category: Royalties, W9: true, Classification: Individual}, true, "1099-MISC 2", "reportable"},
		{"other to MISC 3", pay(client.RailX402, 500000), Payee{Category: Other, W9: true, Classification: LLCP}, true, "1099-MISC 3", "reportable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := Classify(tc.p, tc.pe)
			if v.Reportable != tc.reportable {
				t.Fatalf("reportable = %v, want %v (%s)", v.Reportable, tc.reportable, codes(v))
			}
			if got := codes(v); got != tc.rules {
				t.Fatalf("rules = %s, want %s", got, tc.rules)
			}
			if tc.box != "" && (v.Box == nil || string(v.Box.Kind)+" "+v.Box.Box != tc.box) {
				t.Fatalf("box = %+v, want %s", v.Box, tc.box)
			}
			for _, r := range v.Rules {
				if strings.TrimSpace(r.Reason) == "" {
					t.Fatalf("rule %s states no reason", r.Code)
				}
			}
		})
	}
}

// The threshold is per tax year: $600 through 2025, $2,000 for 2026 (P.L. 119-21
// §70433), royalties $10 throughout, and no guess for a year not yet published.
func TestThresholdByYear(t *testing.T) {
	nec := boxOf[Services]
	roy := boxOf[Royalties]
	for _, tc := range []struct {
		year      int
		total     int64
		box       Box
		reportabl bool
	}{
		{2025, 59999, nec, false},
		{2025, 60000, nec, true},
		{2026, 60000, nec, false}, // the same $600 that was reportable in 2025 is not in 2026
		{2026, 199999, nec, false},
		{2026, 200000, nec, true},
		{2024, 60000, nec, true},
		{2026, 999, roy, false},
		{2026, 1000, roy, true},
	} {
		y, err := YearOf(tc.year)
		if err != nil {
			t.Fatalf("%d: %v", tc.year, err)
		}
		r := thresholdRule(y, tc.box, money.FromCents(tc.total))
		want := "below_threshold"
		if tc.reportabl {
			want = "over_threshold"
		}
		if r.Code != want {
			t.Fatalf("%d %s %s %d¢: %s, want %s (%s)", tc.year, tc.box.Kind, tc.box.Box, tc.total, r.Code, want, r.Reason)
		}
	}
	if _, err := YearOf(2027); err == nil || !strings.Contains(err.Error(), "not published") {
		t.Fatalf("2027 answered %v; the indexed threshold is not published and must not be guessed", err)
	}
}

// Deadlines roll off a weekend: Jan 31 2026 is a Saturday, Jan 31 2027 a Sunday.
func TestDeadlines(t *testing.T) {
	for _, tc := range []struct {
		kind          Kind
		year          int
		furnish, file string
	}{
		{NEC, 2024, "2025-01-31", "2025-01-31"},
		{NEC, 2025, "2026-02-02", "2026-02-02"},
		{MISC, 2025, "2026-02-02", "2026-03-31"},
		{NEC, 2026, "2027-02-01", "2027-02-01"},
		{MISC, 2026, "2027-02-01", "2027-03-31"},
	} {
		d := DeadlineOf(tc.kind, tc.year)
		if d.Furnish != tc.furnish || d.File != tc.file {
			t.Fatalf("%s %d: %+v, want furnish %s file %s", tc.kind, tc.year, d, tc.furnish, tc.file)
		}
	}
}

func TestTINFormat(t *testing.T) {
	for _, tc := range []struct {
		typ TINType
		in  string
		ok  bool
	}{
		{EIN, "12-3456789", true},
		{EIN, "123456789", true},
		{EIN, "07-3456789", false}, // 07 is not an assigned prefix
		{EIN, "00-1234567", false},
		{EIN, "96-1234567", false},
		{EIN, "12-345678", false},
		{EIN, "12-34567a9", false},
		{SSN, "123-45-6789", true},
		{SSN, "000-45-6789", false},
		{SSN, "666-45-6789", false},
		{SSN, "123-00-6789", false},
		{SSN, "123-45-0000", false},
		{SSN, "912-70-1234", true},  // an ITIN
		{SSN, "912-89-1234", false}, // 89 is not an ITIN group
		{SSN, "912-93-1234", false},
		{"itin", "912-70-1234", false},
	} {
		tn, err := ParseTIN(tc.typ, tc.in)
		if (err == nil) != tc.ok {
			t.Fatalf("%s %q: err = %v, want ok = %v", tc.typ, tc.in, err, tc.ok)
		}
		if err != nil && strings.Contains(err.Error(), digits(tc.in)) {
			t.Fatalf("a refusal repeated the number: %v", err)
		}
		if err == nil && strings.Contains(fmt.Sprintf("%v %+v %#v %s", tn, tn, tn, tn), digits(tc.in)[:5]) {
			t.Fatalf("formatting a TIN printed more than its last four digits")
		}
	}
	e, _ := ParseTIN(EIN, "12-3456789")
	s, _ := ParseTIN(SSN, "123-45-6789")
	if e.String() != "**-***6789" || s.String() != "***-**-6789" {
		t.Fatalf("masks = %q %q", e.String(), s.String())
	}
	if e.full() != "12-3456789" || s.full() != "123-45-6789" {
		t.Fatalf("full = %q %q", e.full(), s.full())
	}
}

// A TIN is sealed by the store under its owner's key and bound to its row and column: the
// bytes on disk carry neither the number nor a repeat of another's, the same TIN written
// twice is different bytes, and what is carried to another org's file, another row or
// another column does not open.
func TestATINIsSealedPerOwnerAndPerRow(t *testing.T) {
	master := make([]byte, 32)
	open := func(org string) *store {
		t.Helper()
		ns := principal.MustOrgNamespace(org, "")
		db, err := cloud.OrgDB(t.TempDir(), ns, subsystem)
		if err != nil {
			t.Fatal(err)
		}
		st, err := openStore(master)(ns, db)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
	acme, globex := open("acme"), open("globex")
	ctx := context.Background()
	tn, _ := ParseTIN(EIN, "12-3456789")

	put := func(st *store, id string, in TIN) []byte {
		t.Helper()
		if err := st.insertForm(ctx, formRow{id: id, payee: id, year: 2026, kind: NEC, status: Draft, content: "{}", payeeTIN: in, created: 1, updated: 1}); err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if err := st.db.QueryRow(`SELECT payee_tin FROM forms WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	a, b := put(acme, "f1", tn), put(acme, "f2", tn)
	if strings.Contains(string(a), "123456789") || !strings.HasPrefix(string(a), "seal:") {
		t.Fatalf("the stored bytes carry the TIN or are not a sealed value: %.30q", a)
	}
	// The same number sealed twice is two different blobs: equal ciphertexts would say
	// which payees share a TIN to anyone holding the decrypted pages.
	if bytes.Equal(a, b) {
		t.Fatal("one TIN sealed twice to the same bytes: an equality oracle over TINs")
	}
	if r, err := acme.form(ctx, "f1"); err != nil || r.payeeTIN != tn {
		t.Fatalf("read back = %v %v", r.payeeTIN, err)
	}
	// Carried to another row of the same file, another column, another org's file.
	if _, err := acme.db.Exec(`UPDATE forms SET payee_tin = $1 WHERE id = 'f2'`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.form(ctx, "f2"); err == nil {
		t.Fatal("a TIN carried to another row opened")
	}
	if _, err := acme.db.Exec(`UPDATE forms SET payee_tin = $1, payer_tin = $1 WHERE id = 'f1'`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.form(ctx, "f1"); err == nil {
		t.Fatal("a TIN carried to another column opened")
	}
	g := put(globex, "f1", tn)
	if _, err := acme.db.Exec(`UPDATE forms SET payee_tin = $1, payer_tin = NULL WHERE id = 'f1'`, g); err != nil {
		t.Fatal(err)
	}
	if _, err := acme.form(ctx, "f1"); err == nil {
		t.Fatal("another org's TIN opened under this org's key")
	}
	// No master: the store does not open.
	ns := principal.MustOrgNamespace("initech", "")
	db, err := cloud.OrgDB(t.TempDir(), ns, subsystem)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := openStore(nil)(ns, db); err == nil || !errors.Is(err, errNoMaster) {
		_ = st.Close()
		t.Fatalf("a store opened with no master: %v", err)
	}
}

func record1(k Kind, tinDigits string) irisRecord {
	payer, _ := ParseTIN(EIN, "12-3456789")
	payee, _ := ParseTIN(SSN, tinDigits)
	return irisRecord{
		c: content{Kind: k, Year: 2026, Account: "f1099_1",
			Payer:     Party{Name: "Acme Robotics, Inc.", Address: Address{Line1: "1 Market St. #500", City: "San Francisco", State: "CA", ZIP: "94105", Country: "US"}, Phone: "(415) 555-0100"},
			Recipient: Party{Name: "Ada  Lovelace", Address: Address{Line1: "2 Pine St", City: "Oakland", State: "CA", ZIP: "946120000"}}},
		payer: payer, payee: payee, boxCents: map[string]int64{"1a": 250000, "3": 70000},
	}
}

// The export writes, and the validator checks, every rule Pub 5717 §3.5 sets.
func TestIRISFileRules(t *testing.T) {
	file, err := render(NEC, []irisRecord{record1(NEC, "123-45-6789")})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if errs := validate(NEC, file); len(errs) > 0 {
		t.Fatalf("a rendered file fails its own rules: %v", errs)
	}
	s := string(file)
	for _, want := range []string{"123456789", "Acme Robotics, Inc", "1 Market St 500", "Ada Lovelace", "946120000", "2500.00"} {
		if !strings.Contains(s, want) {
			t.Fatalf("file lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Inc.") || strings.Contains(s, "#") {
		t.Fatalf("file keeps a character the portal rejects:\n%s", s)
	}

	// The validator refuses each broken rule on its own.
	rows, err := csv.NewReader(strings.NewReader(s)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	head, row := rows[0], rows[1]
	encodeCSV := func(rs ...[]string) []byte {
		var b bytes.Buffer
		w := csv.NewWriter(&b)
		_ = w.WriteAll(rs)
		return b.Bytes()
	}
	col := func(name string) int {
		for i, c := range layouts[NEC] {
			if c.name == name {
				return i
			}
		}
		t.Fatalf("no column %s", name)
		return -1
	}
	breaks := map[string]func([]string){
		"period in a name":     func(r []string) { r[col("Recipient Name")] = "Ada L. Lovelace" },
		"double space":         func(r []string) { r[col("Recipient City")] = "San  Jose" },
		"eight-digit TIN":      func(r []string) { r[col("Recipient TIN")] = "12345678" },
		"dashed ZIP":           func(r []string) { r[col("Recipient ZIP")] = "94612-0000" },
		"zero amount":          func(r []string) { r[col("Box 1a Nonemployee Compensation")] = "0.00" },
		"dollar sign":          func(r []string) { r[col("Box 1a Nonemployee Compensation")] = "$2500.00" },
		"check box not Y/N":    func(r []string) { r[col("Corrected")] = "yes" },
		"missing payer TIN":    func(r []string) { r[col("Payer TIN")] = "" },
		"wrong form type":      func(r []string) { r[col("Form Type")] = "1099-MISC" },
		"no amount at all":     func(r []string) { r[col("Box 1a Nonemployee Compensation")] = "" },
		"missing account":      func(r []string) { r[col("Account Number")] = "" },
		"trailing space in id": func(r []string) { r[col("Tax Year")] = "2026 " },
	}
	for what, mutate := range breaks {
		r := append([]string(nil), row...)
		mutate(r)
		if errs := validate(NEC, encodeCSV(head, r)); len(errs) == 0 {
			t.Errorf("%s passed the validator", what)
		}
	}

	// 251 records is more than a file takes.
	var many []irisRecord
	for range maxRecords + 1 {
		many = append(many, record1(NEC, "123-45-6789"))
	}
	if _, err := render(NEC, many); err == nil || !strings.Contains(err.Error(), "250") {
		t.Fatalf("a 251-record file rendered: %v", err)
	}
	// A header that is not the layout's is refused.
	if errs := validate(NEC, []byte(strings.Replace(s, "Payer TIN,", "Payer EIN,", 1))); len(errs) == 0 {
		t.Fatal("a wrong header passed")
	}
}
