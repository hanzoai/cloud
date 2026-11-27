// Package plugin is what each host is running, and how to change it: enable,
// disable, reload, or pin a service to a version.
//
// It is the control plane for the host's zip-native plugins, and a pin rolls
// back as readily as it rolls forward.
//
// A plugin here is a service that ships as its OWN binary and is composed in at
// run time by zip.Load, one child process per app on a private unix socket. The
// authoritative app->prefixes table is the hand-authored manifest.Apps; the
// authoritative VERSION is the artifact's SHA-256, because that is the only
// identifier that cannot drift from the bits actually serving. This package
// invents neither — it reports the first and moves the second.
//
// It used to be something else: a second plugin registry read from a
// CLOUD_PLUGINS JSON manifest, mounting wasm/goa modules and reverse proxies.
// Nothing in this repo, in universe, or in any chart ever set CLOUD_PLUGINS, so
// that lane mounted nothing in production while publishing an untyped
// GET /v1/plugins that reported the empty set. That lane is gone from here.
//
// GET /v1/tool/plugins still exists, in apps/tools, and it answers the SAME question
// from a different source: cloud.Subsystems(), the snapshot taken at boot. It
// therefore cannot see the effect of the enable/disable/reload below, which is
// why this surface — live, per host, keyed on the running artifact's digest — is
// the one to read when the answer has to be true right now.
//
// Every route below can take production down, so every one of them is
// SuperAdmin-gated and every mutation is written to the hash-chained audit
// trail BEFORE it is reported as done. A deployment with no audit store refuses
// to mutate at all, the same way a credit grant does.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
package plugin
