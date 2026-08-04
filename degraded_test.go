package cloud

import (
	"errors"
	"strings"
	"testing"
)

// These pin the distinction the outage turned on: a plane that was never enabled
// and a plane that was enabled and died both answer 503, and until now nothing
// could tell them apart from outside.

func TestDegraded_RecordsAndReports(t *testing.T) {
	resetDegradedForTest()
	t.Cleanup(resetDegradedForTest)

	if len(Degradations()) != 0 {
		t.Fatal("a fresh process must not be degraded")
	}
	Degraded("commerce", errors.New("invalid KV_URL: kv: invalid URL scheme: redis"))

	got := Degradations()
	if len(got) != 1 {
		t.Fatalf("Degradations = %v, want exactly [commerce]", got)
	}
	if got["commerce"] == "" || !strings.Contains(got["commerce"], "invalid URL scheme") {
		t.Fatalf("reason lost: %q — the gate must say WHY, not merely that something broke", got["commerce"])
	}
}

// The FIRST reason is the cause; later ones are usually its echo, and overwriting
// would replace the diagnosis with a symptom.
func TestDegraded_KeepsFirstReason(t *testing.T) {
	resetDegradedForTest()
	t.Cleanup(resetDegradedForTest)

	Degraded("commerce", errors.New("root cause"))
	Degraded("commerce", errors.New("downstream echo"))
	if got := Degradations()["commerce"]; got != "root cause" {
		t.Fatalf("reason = %q, want the first one (%q)", got, "root cause")
	}
}

// A nil error is not a failure, and an unnamed subsystem is not reportable.
// Recording either would make the gate cry wolf and get itself ignored.
func TestDegraded_IgnoresNonFailures(t *testing.T) {
	resetDegradedForTest()
	t.Cleanup(resetDegradedForTest)

	Degraded("commerce", nil)
	Degraded("", errors.New("boom"))
	if d := Degradations(); len(d) != 0 {
		t.Fatalf("registry took a non-failure: %v", d)
	}
}

// Degradations returns a copy; a caller must not be able to edit the registry
// through the map it was handed.
func TestDegraded_ReturnsCopy(t *testing.T) {
	resetDegradedForTest()
	t.Cleanup(resetDegradedForTest)

	Degraded("commerce", errors.New("boom"))
	Degradations()["commerce"] = "tampered"
	if got := Degradations()["commerce"]; got != "boom" {
		t.Fatalf("registry mutated through the returned map: %q", got)
	}
}
