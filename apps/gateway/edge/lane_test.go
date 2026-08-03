package edge

// The lane table IS the specification of the differentiator. It is a pure
// function of two values, so this table is total: there is no third input, no
// clock and no I/O that could make the classification depend on anything else.

import "testing"

func TestLane(t *testing.T) {
	quiet := Pattern{Requests: 3, Paths: 2}
	stuffing := Pattern{Requests: 40, Peers: stuffPeers}
	guessing := Pattern{Requests: 40, Failures: guessFailures}
	sweeping := Pattern{Requests: 40, Paths: sweepPaths}

	cases := []struct {
		class string
		p     Pattern
		want  string
		why   string
	}{
		{CredSecret, quiet, AgencyAgent, "a machine credential we issued is attributable — that is the agent lane"},
		{CredSecret, sweeping, AgencyAgent, "an agent walking many endpoints is an agent doing its job, not a scraper"},
		{CredSecret, guessing, AgencyAgent, "a failing agent key is still an agent key; the pattern is the scorer's to weigh"},
		{CredSession, quiet, AgencyHuman, "a browser session is a person"},
		{CredSession, sweeping, AgencyHuman, "a busy person is still a person"},
		{CredAnonymous, quiet, AgencyUnknown, "anonymous is not malicious — every new integration starts here"},
		{CredPublishable, quiet, AgencyUnknown, "a publishable key names a tenant, not a caller; unremarkable use is unremarkable"},
		{CredAnonymous, stuffing, AgencyBot, "one address, many credentials, no attribution: stuffing"},
		{CredAnonymous, guessing, AgencyBot, "a wall of refusals from an unattributable caller: guessing"},
		{CredAnonymous, sweeping, AgencyBot, "an unattributable caller walking the map: scraping"},
		{CredPublishable, stuffing, AgencyBot, "a copied publishable key behaves the same way and is classed the same way"},
		{"", quiet, AgencyUnknown, "an unrecognised class is unknown, never a lane with consequences"},
	}
	for _, tc := range cases {
		if got := Lane(tc.class, tc.p); got != tc.want {
			t.Errorf("Lane(%q, %+v) = %q, want %q — %s", tc.class, tc.p, got, tc.want, tc.why)
		}
	}
}
