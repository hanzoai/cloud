package meet

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/zap-proto/zip"
)

// roomID is a room's own id within a space. It carries a hyphen and, in the
// round-trip test below, an UNDERSCORE — because the id half is opaque to the parse
// and only the FIRST separator splits, which is the property that lets a room be
// named anything the collaboration store already allows.
const roomID = "ch-bugfix-1010"

func askCall(t *testing.T, app *zip.App, space, room, bearer string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/meet/call?space="+space+"&room="+room, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET /v1/meet/call: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func decodeCall(t *testing.T, body string) venue {
	t.Helper()
	var out venue
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

// TestRoomNameRoundTripsThroughTheMembershipParse is the invariant the whole
// integration rests on: the name this package COMPOSES for a room is a name it
// PARSES back to the same space.
//
// It matters because the parse is an authorization input. space() picks the
// segment the caller's membership is checked against, so a composition that did not
// round-trip would check the caller against a space that is not the room's — and
// a caller who is a member of the wrong one would be admitted. Two halves of one
// rule, asserted against each other rather than each against a literal.
func TestRoomNameRoundTripsThroughTheMembershipParse(t *testing.T) {
	for _, id := range []string{roomID, "dm-1", "a_b_c", "", "with spaces", "ünïcode"} {
		if got := space(roomName(spaceA, id)); got != spaceA {
			t.Errorf("space(roomName(%q, %q)) = %q, want %q", spaceA, id, got, spaceA)
		}
	}
}

// TestSplitsRefusesASpaceThatWouldNotParseBack pins the refusal that MAKES the
// round trip above total rather than usually-true.
//
// A space carrying the separator parses back as a PREFIX of itself, so the
// membership question would be asked about a different space than the one named
// — "a_b" checked as "a" — and a member of the prefix would be seated in a room of a
// space they are not in. The controls are the reason this is a refusal and not a
// fold: folding the character maps two spaces onto one media room, which is the
// same defect wearing a repair.
func TestSplitsRefusesASpaceThatWouldNotParseBack(t *testing.T) {
	for _, bad := range []string{"a_b", "_", "_leading", "trailing_", ""} {
		if splits(bad) {
			t.Errorf("splits(%q) = true, but it does not round-trip", bad)
		}
	}
	for _, ok := range []string{spaceA, "plain", "with-hyphens"} {
		if !splits(ok) {
			t.Errorf("splits(%q) = false, but it round-trips", ok)
		}
	}
}

// TestCallResolvesARoomToItsMediaName is the integration: a member of the space
// holding a room is told which media room that room's call happens in, and the
// answer is exactly what the existing mint takes as roomName.
func TestCallResolvesARoomToItsMediaName(t *testing.T) {
	app := mount(t, "devkey", "devsecret")
	t.Setenv(wsEnv, "wss://meet.example/rtc")

	code, body := askCall(t, app, spaceA, roomID, access(t, "person-42"))
	if code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", code, body)
	}
	got := decodeCall(t, body)
	if want := spaceA + "_" + roomID; got.Name != want {
		t.Errorf("name = %q, want %q", got.Name, want)
	}
	if !got.Ready {
		t.Error("ready = false on a configured office")
	}

	// The resolved name is not merely well-formed — it is a name THIS deployment
	// will mint a seat for. Feeding it to the real mint is what proves the two
	// surfaces agree about what a room is; asserting the string alone would pass
	// against a composition getToken refuses.
	if code, out := ask(t, app, got.Name, "person-42", access(t, "person-42")); code != http.StatusOK {
		t.Fatalf("the resolved name does not mint: %d (%s)", code, out)
	}
}

// TestCallRefusesANonMember: the address of a room's call is told only to someone
// who could join it. Without this the operation is a space-membership oracle for
// anyone able to guess a room id.
func TestCallRefusesANonMember(t *testing.T) {
	app := mountWith(t, keyFileWith(t, keyBody("devkey", "devsecret")),
		holds(map[string]string{spaceA: token.RoleMember}))

	other := "22222222-2222-4222-8222-222222222222"
	code, body := askCall(t, app, other, roomID, access(t, "person-42"))
	if code != http.StatusUnauthorized {
		t.Fatalf("got %d (%s), want 401 for a space the caller is not in", code, body)
	}
	if strings.Contains(body, other+"_") {
		t.Errorf("the refusal echoes a composed room name: %s", body)
	}
}

// TestCallRefusesAnAnonymousCaller: admits reads the identity boundary's own
// attestation, so a caller who presented nothing is refused before any membership
// question is asked.
func TestCallRefusesAnAnonymousCaller(t *testing.T) {
	app := mount(t, "devkey", "devsecret")
	if code, body := askCall(t, app, spaceA, roomID, ""); code != http.StatusUnauthorized {
		t.Fatalf("got %d (%s), want 401 for an anonymous caller", code, body)
	}
}

// TestCallAnswersTheNameOnAnUnconfiguredDeployment is the honest-degraded case, and
// it is the one that separates this operation from the mint.
//
// A room's media name is a property of the ROOM; the signing key is a property of
// the DEPLOYMENT. So a deployment holding no key still knows which call a channel's
// is — it just cannot seat anyone in it. Answering `ready:false` with the true name
// lets a surface say "calling is unavailable here" instead of rendering a join
// button that 503s, and it is why this route does not sit behind state.ready().
func TestCallAnswersTheNameOnAnUnconfiguredDeployment(t *testing.T) {
	app := mountWithKeyFile(t, keyFileWith(t, ""))
	if load().reason == "" {
		t.Fatal("the fixture is CONFIGURED — this test proves nothing")
	}
	code, body := askCall(t, app, spaceA, roomID, access(t, "person-42"))
	if code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200 — the name is knowable without a key", code, body)
	}
	got := decodeCall(t, body)
	if want := spaceA + "_" + roomID; got.Name != want {
		t.Errorf("name = %q, want %q", got.Name, want)
	}
	if got.Ready {
		t.Error("ready = true on a deployment that cannot mint")
	}
	// The mint is still refused, which is what makes `ready:false` a report of a
	// real condition rather than a decoration.
	if code, _ := ask(t, app, got.Name, "person-42", access(t, "person-42")); code != http.StatusServiceUnavailable {
		t.Fatalf("the mint answered %d on an unconfigured office, want 503", code)
	}
}

// TestCallRefusesAComposedName: the caller supplies the PAIR, never the composition.
// A space carrying the separator is the one way a caller could smuggle a
// composed name in through the space half, and it is refused with a 400 rather
// than parsed.
func TestCallRefusesAComposedName(t *testing.T) {
	app := mount(t, "devkey", "devsecret")
	code, body := askCall(t, app, spaceA+"_"+roomID, roomID, access(t, "person-42"))
	if code != http.StatusBadRequest {
		t.Fatalf("got %d (%s), want 400", code, body)
	}
}
