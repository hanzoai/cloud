// Package ci mounts the CI dashboard as a capability of this binary.
//
// It answers the board: the recent builds of every repository the caller may see.
// What one repository ran, on one ref, with its jobs and their logs, is
// /v1/git/runs, beside the repository it belongs to. What is running is
// /v1/deploy/live, which reads it from Hanzo CD together with the pin and the
// commit each image was built from, so one question has one operation.
//
// It sits beside the CD half rather than behind a prefix of its own. /v1/deploy
// already sits behind the gateway's IAM identity; this surface answered a valid
// hanzo.id bearer with a bare 401, because it sat behind a gate that reads an
// X-Org-Id header instead. Mounting it here is what makes one identity read the
// whole plane — and, since the CLI's command groups are generated from the
// document this binary emits, what gives `hanzo ci` its verbs.
//
// The surface itself stays in hanzo.ai/ci: it is deployed standalone too, and a
// fork of it here would be the second source HIP-0106 §4.2 names.
package ci
