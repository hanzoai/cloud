// Package testdeadline holds one source gate: every app.Test call in a test of this
// repository states its own deadline. It has no code; the gate is its test.
//
// A bare app.Test(req) gives the handler Fiber's default of one second and fails the
// test past it. On a CI runner testing 32 packages at once a handler has taken two,
// and the email tests failed that way in runs 17680 and 17683 with "i/o timeout" on
// code that was correct. The repository's answer, written out at almost every call,
// is zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true} (fiber.TestConfig
// on a *fiber.App), so the bound is a test's statement and not the router's default.
package testdeadline
