package tax

// w8_test.go drives the W-8BEN and W-8BEN-E through the same profile, grant and
// certification as the W-9: wien, an Austrian company, is paid by acme on the
// rail, and every property the forms promise is asked of what the surface
// answers, stores and logs.

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
)

const wienFTIN = "ATU98765432"

// w8bene is wien's W-8BEN-E, claiming 10% on royalties under a treaty article.
func w8bene() map[string]any {
	return map[string]any{
		"form": "w8bene", "name": "Wien Robotik GmbH",
		"address":    map[string]any{"line1": "Ringstrasse 1", "city": "Wien", "zip": "1010", "country": "AT"},
		"foreignTin": wienFTIN,
		"w8": map[string]any{
			"country": "AT", "chapter3": "corporation", "chapter4": "active_nffe", "capacity": "Director",
			"treaty": map[string]any{"country": "AT", "article": "12(2)", "rateBps": 1000, "income": "royalties", "lob": "active_business"},
		},
	}
}

// Instructions for Form W-8BEN (Rev. 10-2021): "a Form W-8BEN signed on September
// 30, 2015, remains valid through December 31, 2018" — through the end of that
// day wherever the signer is, and not a second after it anywhere.
func TestW8ExpiresOnTheInstructionsOwnExample(t *testing.T) {
	signed := time.Date(2015, time.September, 30, 12, 0, 0, 0, time.UTC).Unix()
	got := time.Unix(Expires(signed), 0)
	if d := got.In(latest); d.Year() != 2018 || d.Month() != time.December || d.Day() != 31 {
		t.Fatalf("Expires = %s, want the end of December 31, 2018", d)
	}
	if next := got.Add(time.Second).In(latest); next.Year() != 2019 {
		t.Fatalf("a second after Expires is %s, want January 1, 2019 in the zone it comes to first", next)
	}
}

// A form signed on the evening of December 31 in the Americas is that year's in
// its signer's hand, though the instant is already January 1 in UTC: it expires
// with that year's forms, never a year later.
func TestW8NeverExpiresLate(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	signed := time.Date(2021, time.December, 31, 22, 0, 0, 0, ny).Unix()
	if y := time.Unix(Expires(signed), 0).In(latest).Year(); y != 2024 {
		t.Fatalf("a W-8 signed December 31, 2021 in New York expires in %d, want 2024", y)
	}
}

// A W-8 is refused where the form itself says it is the wrong one, and a treaty
// claim needs a number.
func TestW8Validation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		put := func(mut func(m map[string]any)) int {
			m := w8bene()
			mut(m)
			code, _, _ := e.raw(http.MethodPut, "/v1/tax/profile", admin("wien"), m, nil)
			return code
		}
		w8 := func(m map[string]any) map[string]any { return m["w8"].(map[string]any) }
		for name, mut := range map[string]func(map[string]any){
			"a U.S. entity":        func(m map[string]any) { w8(m)["country"] = "US" },
			"a U.S. residence":     func(m map[string]any) { m["address"].(map[string]any)["country"] = "US" },
			"no chapter 3 status":  func(m map[string]any) { delete(w8(m), "chapter3") },
			"no chapter 4 status":  func(m map[string]any) { delete(w8(m), "chapter4") },
			"no signer capacity":   func(m map[string]any) { delete(w8(m), "capacity") },
			"a W-9 line":           func(m map[string]any) { m["classification"] = "c_corp" },
			"a date of birth":      func(m map[string]any) { w8(m)["birth"] = "1980-01-01" },
			"an SSN on an entity":  func(m map[string]any) { m["tin"], m["tinType"] = "123-45-6789", "ssn" },
			"a treaty elsewhere":   func(m map[string]any) { w8(m)["treaty"].(map[string]any)["country"] = "DE" },
			"a 30% treaty rate":    func(m map[string]any) { w8(m)["treaty"].(map[string]any)["rateBps"] = 3000 },
			"a treaty, no numbers": func(m map[string]any) { delete(m, "foreignTin") },
			"no w8 at all":         func(m map[string]any) { delete(m, "w8") },
			"a treaty, no LOB":     func(m map[string]any) { delete(w8(m)["treaty"].(map[string]any), "lob") },
			"an unknown LOB":       func(m map[string]any) { w8(m)["treaty"].(map[string]any)["lob"] = "vibes" },
			"an other LOB, unsaid": func(m map[string]any) { w8(m)["treaty"].(map[string]any)["lob"] = "other" },
			"a partnership, no treaty claim": func(m map[string]any) {
				w8(m)["chapter3"] = "partnership"
				delete(w8(m), "treaty")
			},
			"a disregarded entity, no treaty claim": func(m map[string]any) {
				w8(m)["chapter3"] = "disregarded"
				delete(w8(m), "treaty")
			},
		} {
			if code := put(mut); code != http.StatusBadRequest {
				t.Errorf("%s: PUT = %d, want 400", name, code)
			}
		}
		// A hybrid entity claiming treaty benefits is the W-8BEN-E's own case.
		hybrid := w8bene()
		w8(hybrid)["chapter3"] = "partnership"
		e.must(200, http.MethodPut, "/v1/tax/profile", admin("hybrid"), hybrid, nil)
		var p Profile
		e.must(200, http.MethodPut, "/v1/tax/profile", admin("wien"), w8bene(), &p)
		if p.Form != FormW8BENE || p.W8 == nil || p.W8.Chapter3 != "corporation" || p.ForeignTIN != "****5432" || p.Valid {
			t.Fatalf("profile = %+v, want a W-8BEN-E, foreign TIN masked, not valid before it is certified", p)
		}
		// An individual's W-8BEN carries none of an entity's lines.
		ben := map[string]any{"form": "w8ben", "name": "Anna Berger",
			"address": map[string]any{"line1": "Hauptplatz 2", "city": "Graz", "country": "AT"},
			"w8":      map[string]any{"country": "AT", "chapter4": "active_nffe"}}
		if code, _, _ := e.raw(http.MethodPut, "/v1/tax/profile", admin("anna"), ben, nil); code != 400 {
			t.Fatalf("a W-8BEN with a chapter 4 status = %d, want 400", code)
		}
		lob := map[string]any{"form": "w8ben", "name": "Anna Berger", "foreignTin": "AT123456789",
			"address": map[string]any{"line1": "Hauptplatz 2", "city": "Graz", "country": "AT"},
			"w8": map[string]any{"country": "AT", "birth": "1984-02-11",
				"treaty": map[string]any{"country": "AT", "article": "12", "rateBps": 0, "income": "royalties", "lob": "no_lob"}}}
		if code, _, _ := e.raw(http.MethodPut, "/v1/tax/profile", admin("anna"), lob, nil); code != 400 {
			t.Fatalf("a W-8BEN with a line 14b limitation on benefits = %d, want 400", code)
		}
		delete(ben["w8"].(map[string]any), "chapter4")
		ben["w8"].(map[string]any)["birth"] = "1984-02-11"
		e.must(200, http.MethodPut, "/v1/tax/profile", admin("anna"), ben, &p)
		if p.Form != FormW8BEN || p.W8.Birth != "1984-02-11" {
			t.Fatalf("W-8BEN = %+v", p)
		}
	})
}

// The payer reads a W-8 under the same grant as a W-9; the payee's own admin
// certifies it through legal; a change of facts ends its validity.
func TestW8ExchangedAndCertified(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.profile("acme", CCorp, EIN, acmeEIN, true)
		e.must(200, http.MethodPut, "/v1/tax/profile", admin("wien"), w8bene(), nil)
		e.payTo("acme", "wien", 50000)
		id := e.share("acme", "wien", Royalties)

		var w W9
		e.must(200, http.MethodGet, "/v1/tax/w9/"+id, admin("acme"), nil, &w)
		if w.Profile == nil || w.Profile.Form != FormW8BENE || w.Profile.W8 == nil || w.Profile.ForeignTIN != "****5432" {
			t.Fatalf("payer's view = %+v, want the W-8BEN-E with its foreign TIN masked", w.Profile)
		}
		if w.Profile.Valid {
			t.Fatal("an uncertified W-8 read as valid")
		}

		// A SuperAdmin inside the org is not a person who can certify for it.
		sa := who{org: "wien", super: true}
		if code, _, _ := e.raw(http.MethodPost, "/v1/tax/profile/certify", sa, nil, nil); code != http.StatusForbidden {
			t.Fatalf("SuperAdmin certify = %d, want 403", code)
		}
		e.certify("wien")
		var p Profile
		e.must(200, http.MethodGet, "/v1/tax/profile", member("wien"), nil, &p)
		signed := time.Unix(p.Certification.Signed, 0).In(earliest)
		if !p.Valid || p.Expires != time.Date(signed.Year()+4, 1, 1, 0, 0, 0, 0, latest).Unix()-1 {
			t.Fatalf("certified W-8 = valid %v, expires %d; want valid through Dec 31 of the third year after %d", p.Valid, p.Expires, signed.Year())
		}
		e.must(200, http.MethodGet, "/v1/tax/w9/"+id, admin("acme"), nil, &w)
		if !w.Profile.Valid || w.Profile.Certification.Status != certCertified {
			t.Fatalf("payer sees %+v, want the certified W-8", w.Profile)
		}

		// A change in circumstances: a new version the signature does not cover.
		m := w8bene()
		m["address"].(map[string]any)["line1"] = "Kaerntner Strasse 5"
		e.must(200, http.MethodPut, "/v1/tax/profile", admin("wien"), m, &p)
		if p.Valid || p.Certification.Status != certNone {
			t.Fatalf("after a change of facts the W-8 = valid %v, %s; want invalid until certified again", p.Valid, p.Certification.Status)
		}
	})
}

// A valid W-8 takes the payee off Form 1099; an uncertified one establishes
// nothing, and the payee is reportable with backup withholding.
func TestForeignPayeeIsNotOn1099(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.profile("acme", CCorp, EIN, acmeEIN, true)
		m := w8bene()
		m["w8"].(map[string]any)["treaty"] = map[string]any{}
		e.must(200, http.MethodPut, "/v1/tax/profile", admin("wien"), m, nil)
		e.payTo("acme", "wien", 300000)
		e.share("acme", "wien", Services)

		var l Ledger
		e.must(200, http.MethodGet, "/v1/tax/payments?year=2026", admin("acme"), nil, &l)
		line := l.Payees[0].Lines[0]
		if !line.Verdict.Reportable || line.Verdict.Rules[len(line.Verdict.Rules)-1].Code != "w8_lapsed" {
			t.Fatalf("an uncertified W-8 = %+v, want reportable under w8_lapsed", line.Verdict)
		}

		e.certify("wien")
		e.must(200, http.MethodGet, "/v1/tax/payments?year=2026", admin("acme"), nil, &l)
		line = l.Payees[0].Lines[0]
		if line.Verdict.Reportable || line.Verdict.Rules[len(line.Verdict.Rules)-1].Code != "foreign" || len(l.Payees[0].Totals) != 0 {
			t.Fatalf("a valid W-8 = %+v, want not reportable under the foreign rule", l.Payees[0])
		}
		var p Prepared
		e.must(200, http.MethodPost, "/v1/tax/forms", admin("acme"), map[string]any{"year": year()}, &p)
		if len(p.Forms) != 0 {
			t.Fatalf("a 1099 was prepared for a foreign payee on a valid W-8: %+v", p.Forms)
		}
	})
}

// A foreign TIN is sealed as a U.S. TIN is: never at rest in the clear, never in
// an answer but the audited full read, never in a log or on the trail.
func TestForeignTINNeverAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.profile("acme", CCorp, EIN, acmeEIN, true)
		e.must(200, http.MethodPut, "/v1/tax/profile", admin("wien"), w8bene(), nil)
		e.payTo("acme", "wien", 50000)
		id := e.share("acme", "wien", Royalties)
		e.certify("wien")

		var full tinOut
		e.must(200, http.MethodGet, "/v1/tax/w9/"+id+"/tin", admin("acme"), nil, &full)
		if full.ForeignTIN != wienFTIN {
			t.Fatalf("the full read answered %q, want the foreign TIN", full.ForeignTIN)
		}
		if !strings.Contains(strings.Join(e.actions("acme"), ","), "tax.w9.tin_read") {
			t.Fatal("the full read of a foreign TIN is not on the trail")
		}
		secrets := []string{wienFTIN, "98765432"}
		leaks := func(where, s string) {
			t.Helper()
			for _, x := range secrets {
				if strings.Contains(s, x) {
					t.Fatalf("the foreign TIN is in %s", where)
				}
			}
		}
		for _, c := range []struct {
			w    who
			path string
		}{
			{member("wien"), "/v1/tax/profile"}, {admin("acme"), "/v1/tax/w9"}, {admin("acme"), "/v1/tax/w9/" + id},
			{admin("wien"), "/v1/tax/w9/" + id}, {admin("wien"), "/v1/legal/documents"},
		} {
			_, b, _ := e.raw(http.MethodGet, c.path, c.w, nil, nil)
			leaks("GET "+c.path, string(b))
		}
		rows, err := e.storeFor("wien").db.Query(`SELECT data, tin, ftin FROM profile`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var data string
			var tin, ftin []byte
			if err := rows.Scan(&data, &tin, &ftin); err != nil {
				t.Fatal(err)
			}
			if len(ftin) == 0 {
				t.Fatal("no sealed foreign TIN was stored")
			}
			leaks("profile.data", data)
			leaks("profile.ftin", string(ftin))
		}
		_ = rows.Close()
		leaks("the log", e.logs.String())
		trail, _, _ := e.rec.Query(context.Background(), audit.Filter{Limit: 1000})
		b, _ := json.Marshal(trail)
		leaks("the audit trail", string(b))
		if err := Shutdown(); err != nil {
			t.Fatal(err)
		}
		_ = filepath.WalkDir(e.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if raw, rerr := os.ReadFile(path); rerr == nil {
				leaks(path, string(raw))
			}
			return nil
		})
	})
}

// A payee's form is asked for before the first payment once the payer has asked
// /v1/principal whether it may pay — and never on no evidence at all.
func TestFormRequestedBeforeThePayment(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		e.profile("acme", CCorp, EIN, acmeEIN, true)
		if code, _, _ := e.raw(http.MethodPost, "/v1/tax/w9", admin("acme"), map[string]any{"payee": "wien"}, nil); code != http.StatusConflict {
			t.Fatalf("a request with neither a payment nor a clearance = %d, want 409", code)
		}
	})
}
