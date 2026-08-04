package company

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/apps/metering"
	"github.com/zap-proto/zip"
)

// Typing moved facts out of the handlers and into the mount, where nothing in the
// app's own flow tests would notice them going wrong. These pin them.
//
//   - The 1 MiB JSON body cap used to be a line in every handler's decode(). It
//     is now the ONE group middleware limitBody, which is registered AFTER the
//     deck upload precisely so the deck — document BYTES, bounded by the edge —
//     keeps the ceiling it always had. Registration order is the whole mechanism,
//     and registration order is exactly what a refactor silently reverses.
//
//   - begin answers 201 on a first registration and 200 on the idempotent
//     repeat. zip.WithStatus declares ONE status and cannot express that, so the
//     201 rides cloud.Created through the Bridge — which means it is a fact about
//     the middleware being installed, not about the handler.
//
//   - POST /payment stays UNTYPED, and the reason is a wire nothing asserted: a
//     billing denial answers the fleet-wide {"error":{"code","message"}} contract,
//     which zip's error type (a flat {status,code,error}) cannot express. That
//     reason was a comment, so the next pass could type the route, reshape the
//     money-path error for every metered client, and stay green. It is a wire now
//     — see TestPaymentDenialWire.

// raw issues a request with an arbitrary body and content type — what the deck
// upload takes, and what do() (which marshals JSON) cannot express.
func raw(t *testing.T, app *zip.App, method, path, org, contentType string, body []byte) (int, map[string]any) {
	t.Helper()
	rq := httptest.NewRequest(method, path, bytes.NewReader(body))
	rq.Header.Set("Content-Type", contentType)
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", "u_"+org)
	resp, err := app.Test(rq, zip.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

// toCompany drives a fresh org to the terminal company stage by the IMPORT path,
// which is where the fundraising routes become available.
func toCompany(t *testing.T, app *zip.App, org string) {
	t.Helper()
	do(t, app, http.MethodPost, "/v1/company", org, map[string]any{"alreadyIncorporated": true})
	do(t, app, http.MethodPost, "/v1/company/skip", org, nil)
	do(t, app, http.MethodPost, "/v1/company/import/documents", org, map[string]any{"folderId": "F"})
	do(t, app, http.MethodPost, "/v1/company/import/captable", org, map[string]any{"spreadsheetId": "S"})
	if code, m := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "company"}); code != http.StatusOK {
		t.Fatalf("advance to company want 200, got %d (%v)", code, m)
	}
}

// TestDeckTakesRawBytes pins the OTHER route that stays untyped, and pins the
// reason: the deck is the raw request BODY of any content type, named by ?name=.
// A typed In would declare a JSON document this route does not take — the JSON
// decoder would refuse a PDF with 400 — and a typed op cannot bind a body to
// bytes, so there is no In that describes this wire.
//
// fakeDocs.Ingest echoes the name it was given, which is what makes the query
// binding and its default observable from outside.
func TestDeckTakesRawBytes(t *testing.T) {
	app, _, _ := mountFake(t)
	const org = "deck"
	toCompany(t, app, org)

	// A PDF: bytes that are not JSON, accepted and ingested. 201 with the data
	// room's id for the document.
	code, m := raw(t, app, http.MethodPost, "/v1/company/fundraise/deck?name=seed-deck.pdf", org,
		"application/pdf", []byte("%PDF-1.4 not json"))
	if code != http.StatusCreated {
		t.Fatalf("deck want 201, got %d (%v) — the deck body is bytes, not a JSON document", code, m)
	}
	if got := m["documentId"]; got != "doc_seed-deck.pdf" {
		t.Fatalf("documentId want doc_seed-deck.pdf, got %v — ?name= names the document", got)
	}

	// No ?name=: the handler's own default, not an empty name.
	if code, m := raw(t, app, http.MethodPost, "/v1/company/fundraise/deck", org,
		"application/pdf", []byte("%PDF-1.4")); code != http.StatusCreated || m["documentId"] != "doc_pitch-deck" {
		t.Fatalf("unnamed deck want 201 doc_pitch-deck, got %d %v", code, m)
	}

	// An empty body is the one refusal: there is no deck to ingest.
	if code, _ := raw(t, app, http.MethodPost, "/v1/company/fundraise/deck", org, "application/pdf", nil); code != http.StatusBadRequest {
		t.Fatalf("empty deck want 400, got %d", code)
	}
}

// TestBodyCapCoversJSONNotTheDeck proves the cap sits exactly where it always
// sat: over the JSON actions, and never over the deck upload.
func TestBodyCapCoversJSONNotTheDeck(t *testing.T) {
	app, _, _ := mountFake(t)
	const org = "cap"
	// Fast-forward to company via the skip path, so the deck route is reachable.
	toCompany(t, app, org)

	over := bytes.Repeat([]byte("x"), maxBody+1)

	// A JSON action over the cap: 413, the same answer decode() gave.
	if code, _ := raw(t, app, http.MethodPost, "/v1/company/founders", org, "application/json", over); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized JSON body want 413, got %d — limitBody must precede every JSON leaf", code)
	}
	// The surface ROOT too. It is declared on the App with its whole path (a group
	// leaf of "" would register "/v1/company/"), so this is the assertion that the
	// group's middleware — limitBody here, and Bridge with it — still reaches it.
	if code, _ := raw(t, app, http.MethodPost, "/v1/company", org, "application/json", over); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body at the surface root want 413, got %d — the group middleware does not reach /v1/company", code)
	}

	// The deck, over the same cap: ingested, because a deck is bytes and its only
	// ceiling has always been the edge's BodyLimit.
	if code, _ := raw(t, app, http.MethodPost, "/v1/company/fundraise/deck", org, "application/pdf", over); code != http.StatusCreated {
		t.Fatalf("oversized deck want 201, got %d — the deck must be registered BEFORE limitBody", code)
	}
}

// TestBeginIsConditionallyCreated proves the 201/200 split survives typing: the
// status rides cloud.Created through the Bridge the group installs, so this fails
// if that middleware is dropped or registered after the leaves.
func TestBeginIsConditionallyCreated(t *testing.T) {
	app, _, _ := mountFake(t)
	const org = "twice"

	if code, _ := do(t, app, http.MethodPost, "/v1/company", org, map[string]any{
		"structure": "c-corp", "jurisdiction": "DE", "name": "Twice Inc.",
	}); code != http.StatusCreated {
		t.Fatalf("first begin want 201, got %d", code)
	}
	if code, m := do(t, app, http.MethodPost, "/v1/company", org, map[string]any{
		"structure": "llc", "jurisdiction": "WY", "name": "Ignored",
	}); code != http.StatusOK {
		t.Fatalf("repeat begin want 200 (idempotent), got %d", code)
	} else if f, _ := m["formation"].(map[string]any); f["name"] != "Twice Inc." {
		t.Fatalf("repeat begin must return the EXISTING formation, got name %v", f["name"])
	}
}

// toPayment drives a fresh org to the payment stage over HTTP — the only stage
// POST /payment is available at. TestHTTPFormationFlow walks the same path
// asserting every step; this one only needs to arrive.
func toPayment(t *testing.T, app *zip.App, org string) {
	t.Helper()
	do(t, app, http.MethodPost, "/v1/company", org, map[string]any{
		"structure": "c-corp", "jurisdiction": "DE", "name": "Payer Inc.",
	})
	do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "founders"})
	do(t, app, http.MethodPost, "/v1/company/founders", org, map[string]any{
		"founders": []map[string]any{{"name": "Ada", "email": "ada@acme.com", "equityBps": 10000}},
	})
	do(t, app, http.MethodPost, "/v1/company/kyc", org, nil)
	do(t, app, http.MethodPost, "/v1/company/kyc/refresh", org, nil)
	if code, m := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "payment"}); code != http.StatusOK {
		t.Fatalf("advance to payment want 200, got %d (%v)", code, m)
	}
}

// denial reads the fleet-wide billing-denial body: the code and message NESTED
// under "error". A flat body fails here, which is the whole point — zip's
// HTTPError is flat.
func denial(t *testing.T, m map[string]any) (code, message string) {
	t.Helper()
	e, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("denial body must nest {code,message} under \"error\" — the fleet billing contract; got %v", m)
	}
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	return code, message
}

// TestPaymentDenialWire pins the money wire of the ONE action on this surface
// that a billing gate can refuse. The $999 formation fee runs through the shared
// ResourceMeter, so a refusal must answer the SAME contract the edge gate answers
// — 402 insufficient_balance, 402 spend_cap_exceeded, 503 balance_unavailable,
// each a {"error":{"code","message"}} body — because a metered client reads one
// shape across every Hanzo surface.
//
// This is what keeps POST /payment out of the typed registry: a typed op's error
// renders zip's flat {status,code,error}, so typing the route as it stands would
// silently reshape all three of these. The test is the guard on that decision.
func TestPaymentDenialWire(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gate   error
		status int
		code   string
	}{
		// Out of funds: the canonical 402.
		{"insufficient", metering.ErrInsufficientBalance, http.StatusPaymentRequired, "insufficient_balance"},
		// Funded but over a per-scope cap: a DISTINCT 402, never the out-of-funds
		// shape — a client that retries on 503 would storm a cap that will not
		// clear until the period rolls over.
		{"spendcap", metering.ErrSpendCapExceeded, http.StatusPaymentRequired, "spend_cap_exceeded"},
		// Commerce unreachable: 503, so the caller retries rather than believing
		// the org is broke.
		{"unavailable", errors.New("commerce unreachable"), http.StatusServiceUnavailable, "balance_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, charge, _ := mountFake(t)
			const org = "deny"
			toPayment(t, app, org)

			charge.err = tc.gate
			status, body := do(t, app, http.MethodPost, "/v1/company/payment", org, nil)
			if status != tc.status {
				t.Fatalf("denied payment want %d, got %d (%v)", tc.status, status, body)
			}
			code, message := denial(t, body)
			if code != tc.code {
				t.Fatalf("denial code want %q, got %q", tc.code, code)
			}
			if message == "" {
				t.Fatal("denial must carry a message — it is what the client shows")
			}

			// A refused charge leaves the formation UNPAID, so the machine's
			// payment guard stays shut. Money state must not advance on a denial.
			_, m := do(t, app, http.MethodGet, "/v1/company", org, nil)
			f, _ := m["formation"].(map[string]any)
			if paid, _ := f["paid"].(bool); paid {
				t.Fatal("a denied charge must not mark the formation paid")
			}
		})
	}
}

// TestPaymentChargesLast pins the ORDER of the payment handler's checks, which is
// the fact that makes the billing gate un-liftable into middleware: a gate that
// ran before the handler would charge a caller the machine is about to refuse.
//
// Both cases arm the charger to DENY, then assert the answer is not a denial —
// which can only be true if the charge was never attempted.
func TestPaymentChargesLast(t *testing.T) {
	t.Run("wrong stage is refused before the charge", func(t *testing.T) {
		app, charge, _ := mountFake(t)
		const org = "early"
		// A formation at the structure stage — payment is not available yet.
		do(t, app, http.MethodPost, "/v1/company", org, map[string]any{
			"structure": "c-corp", "jurisdiction": "DE", "name": "Early Inc.",
		})
		charge.err = metering.ErrInsufficientBalance
		if status, body := do(t, app, http.MethodPost, "/v1/company/payment", org, nil); status != http.StatusConflict {
			t.Fatalf("payment at the structure stage want 409, got %d (%v) — the stage check must precede the charge", status, body)
		}
	})

	t.Run("already paid is idempotent without a second charge", func(t *testing.T) {
		app, charge, _ := mountFake(t)
		const org = "again"
		toPayment(t, app, org)

		if status, _ := do(t, app, http.MethodPost, "/v1/company/payment", org, nil); status != http.StatusOK {
			t.Fatalf("first payment want 200, got %d", status)
		}
		if charge.charged != formationFeeCents {
			t.Fatalf("first payment charged %d, want %d", charge.charged, formationFeeCents)
		}
		// A repeat must short-circuit on f.Paid — never reach the charger, which
		// is armed to deny.
		charge.err = metering.ErrInsufficientBalance
		if status, body := do(t, app, http.MethodPost, "/v1/company/payment", org, nil); status != http.StatusOK {
			t.Fatalf("repeat payment want 200 (idempotent), got %d (%v) — a paid formation must not be charged twice", status, body)
		}
	})
}
