// Package iamtest is a REAL IAM issuer for tests: a keypair, a JWKS endpoint, and
// tokens signed with it.
//
// It exists because a stubbed validator cannot observe the bugs that live in the
// claim mapping. What makes an IAM lane safe is HOW a verified token becomes a
// principal — which claim is read for the subject, what happens when `sub` is
// absent, whether an id_token is distinguishable from an access token — and a stub
// that returns a pre-built identity has already made every one of those decisions
// itself. A test built on one asserts its own fixture.
//
// It is one package rather than a copy per caller for the same reason everything
// else here is: two fixtures for one wire drift, and the one that drifts is the one
// whose test then passes against code that would fail in production.
//
// SIGNING HERE GRANTS NOTHING. The key is generated per test and its JWKS is served
// on a loopback address the test itself owns; no deployment trusts either. This is
// the "forging a bad token is how a verifier gets tested" case the token gate names
// as out of scope, made explicit and shared instead of retyped.
package iamtest
