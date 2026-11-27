// Package attest holds the token a browser echoes to show that a change was
// asked for, and nothing else.
//
// It is a LEAF so that both halves of the control can reach it. The DECISION
// (which requests must show one) belongs to the request tier, because that is
// where the caller's credentials are read; the ROUTE that hands a browser its
// first token belongs to the account surface, because that is the caller's own
// surface. Those two live in packages that import each other's direction, so
// the value they share cannot live in either — it lives here, where both already
// look.
//
// TOKEN — base64url( ts_be64(8) || mac(16) ), where
//
//	mac = KeyedBLAKE3(key, domain \x00 uid \x00 org \x00 ts)[:16]  (luxfi/crypto).
//
// It is BOUND to the validated principal (X-User-Id + X-Org-Id) so a token minted
// for one identity cannot authorize a change as another, and it EXPIRES after TTL.
//
// THE KEY NO LONGER HAS TO BE SHARED. cloud.Intended admits a cookie-authenticated
// change on Sec-Fetch-Site, which the browser states on the request and script cannot
// set, so nothing outside this package reads a token and no two processes have to
// agree on a key. A process holding KeyEnv mints against it; one without mints
// against a random key of its own and checks only what it minted. Both are correct,
// because the token is no longer what admits the change.
package attest
