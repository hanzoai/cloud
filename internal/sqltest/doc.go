// Package sqltest runs a store's tests on both engines it can be deployed on.
//
// A store is ported to hanzo-sql when its suite is green on PostgreSQL, so the suite
// is the proof and this is how one is written once and run twice. SQLite always
// runs. PostgreSQL runs when CLOUD_TEST_SQL_DSN names a server (scripts/postgres.sh
// starts one for a command, and CI runs the gate under it); without it the
// postgres subtest is a visible skip on a workstation and a failure when CI is set,
// never a silent absence.
//
// Wrapping an existing test is one line and an indent:
//
//	func TestThing(t *testing.T) {
//		sqltest.Engines(t, func(t *testing.T, open sqltest.Open) {
//			s, err := openStore(open(ns, "thing"))
//			...
//		})
//	}
//
// A test that reaches its store through cloud.OrgStore or cloud.OrgDB need not call
// open at all: on PostgreSQL the body runs with CLOUD_SQL_DSN naming this test's
// database, so they follow the engine by themselves.
package sqltest
