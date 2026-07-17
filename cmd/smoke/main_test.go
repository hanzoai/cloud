package main

import (
	"net/http"
	"testing"
)

// TestEvaluate locks in the smoke's contract — above all that a READ is NEVER allowed
// to pass on a 402 (the balance-gate-over-blocks bug) or a 5xx (a crash), on any class.
func TestEvaluate(t *testing.T) {
	cases := []struct {
		name     string
		class    class
		status   int
		hasToken bool
		strict   bool
		want     bool
	}{
		// The two universal regressions fail on every class.
		{"402 fails on public", classPublic, http.StatusPaymentRequired, false, false, false},
		{"402 fails on authed+token", classAuthed, http.StatusPaymentRequired, true, false, false},
		{"402 fails on tolerant", classTolerant, http.StatusPaymentRequired, true, false, false},
		{"500 fails on authed", classAuthed, http.StatusInternalServerError, true, false, false},
		{"502 fails on public", classPublic, http.StatusBadGateway, false, false, false},

		// 503 (subsystem staged) is tolerated on data classes, not on liveness.
		{"503 tolerated on public", classPublic, http.StatusServiceUnavailable, false, false, true},
		{"503 tolerated on authed", classAuthed, http.StatusServiceUnavailable, false, false, true},
		{"503 fails on health", classHealth, http.StatusServiceUnavailable, false, false, false},

		// health must be 200.
		{"health 200 passes", classHealth, http.StatusOK, false, false, true},

		// public must be 200.
		{"public 200 passes", classPublic, http.StatusOK, false, false, true},
		{"public 404 fails", classPublic, http.StatusNotFound, false, false, false},
		{"public 401 fails", classPublic, http.StatusUnauthorized, false, false, false},

		// authed: gates without a token, 2xx with one.
		{"authed anon 401 passes", classAuthed, http.StatusUnauthorized, false, false, true},
		{"authed anon 403 passes", classAuthed, http.StatusForbidden, false, false, true},
		{"authed anon 200 fails (should gate)", classAuthed, http.StatusOK, false, false, false},
		{"authed token 200 passes", classAuthed, http.StatusOK, true, false, true},
		{"authed token 403 tolerated (non-strict)", classAuthed, http.StatusForbidden, true, false, true},
		{"authed token 403 fails (strict)", classAuthed, http.StatusForbidden, true, true, false},
		{"authed token 200 passes (strict)", classAuthed, http.StatusOK, true, true, true},

		// tolerant: any of 200/401/403.
		{"tolerant 200 passes", classTolerant, http.StatusOK, true, false, true},
		{"tolerant 403 passes", classTolerant, http.StatusForbidden, false, false, true},
		{"tolerant 404 fails", classTolerant, http.StatusNotFound, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := evaluate(probe{class: tc.class}, tc.status, tc.hasToken, tc.strict)
			if got != tc.want {
				t.Errorf("evaluate(class=%d,status=%d,token=%v,strict=%v) = %v (%q), want %v",
					tc.class, tc.status, tc.hasToken, tc.strict, got, reason, tc.want)
			}
		})
	}
}
