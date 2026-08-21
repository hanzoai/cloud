package cloud

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// The fleet's handlers wrap an upstream's refusal into a sentence of their own —
// "generate: %s", "custody: %v", "scan extraction failed: %s" — and a provider
// that refuses a call quotes the credential it refused. mapError passes a CHOSEN
// sentence through verbatim, deliberately, so the credential rode out with it.
//
// ErrorHandler is the one place every propagated error is rendered, so that is
// where the scrub belongs. These drive the real handler through a real app.
const relayedKey = "sk-live-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789xy"

func renderErr(t *testing.T, err error) (int, string) {
	t.Helper()
	app := zip.New(zip.Config{ErrorHandler: ErrorHandler})
	app.Get("/boom", func(c *zip.Ctx) error { return err })
	resp, rerr := app.Test(httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rerr != nil {
		t.Fatalf("drive the app: %v", rerr)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// A chosen sentence keeps its words and loses the credential inside them.
func TestAChosenSentenceIsRenderedWithoutItsCredential(t *testing.T) {
	status, body := renderErr(t, zip.Errorf(http.StatusBadGateway, "generate: upstream refused: Invalid API key: %s", relayedKey))
	if status != http.StatusBadGateway {
		t.Fatalf("the handler's chosen status must survive, got %d (%s)", status, body)
	}
	if !strings.Contains(body, "generate: upstream refused") {
		t.Fatalf("the handler's own words must survive the scrub: %s", body)
	}
	if strings.Contains(body, relayedKey) {
		t.Fatalf("a credential quoted by an upstream reached the caller: %s", body)
	}
	if !strings.Contains(body, "[REDACTED-TOKEN]") {
		t.Fatalf("neither relayed nor redacted, so the scrub did not run: %s", body)
	}
}

// The control: an ordinary sentence is rendered byte for byte, so the scrub is
// shown to touch credential-shaped tokens and nothing else.
func TestAnOrdinarySentenceIsUnchanged(t *testing.T) {
	const msg = "Billing temporarily unavailable"
	status, body := renderErr(t, zip.Errorf(http.StatusServiceUnavailable, "%s", msg))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status must survive, got %d", status)
	}
	if !strings.Contains(body, msg) {
		t.Fatalf("an ordinary sentence must be rendered unchanged, got %s", body)
	}
	if strings.Contains(body, "REDACTED") {
		t.Fatalf("the scrub altered a sentence carrying no credential: %s", body)
	}
}

// An UNDECIDED error still gets the generic fault sentence — the scrub composes
// over mapError's provenance rule rather than replacing it.
func TestAnUndecidedErrorStillGetsTheGenericSentence(t *testing.T) {
	status, body := renderErr(t, errors.New("dial iam.hanzo.svc: connection refused: token="+relayedKey))
	if status != http.StatusInternalServerError {
		t.Fatalf("an undecided error is a 500, got %d", status)
	}
	if strings.Contains(body, relayedKey) || strings.Contains(body, "iam.hanzo.svc") {
		t.Fatalf("an undecided error must not put our internals on the wire: %s", body)
	}
	if !strings.Contains(body, "X-Request-Id") {
		t.Fatalf("expected the generic fault sentence, got %s", body)
	}
}
