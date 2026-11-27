// Command reach answers: does the DEPLOYED api answer every address this
// document publishes?
//
// The document is a build-time compose of each app's own projection of its own
// router (plugin/embed.go). `check` proves that compose equals the source.
// Neither proves the thing a caller actually needs: that the address is
// reachable in production. Three things break that and nothing else checks any
// of them —
//
//	a stale subset      ONE build serves a renamed route under its new name
//	                    while still publishing the name it was renamed away from
//	a missing mount     manifest/apps.go is a third, hand-maintained source of
//	                    truth and must be a superset of what each app registers
//	an edge interceptor a worker in front of the origin answering /v1/models*
//	                    and /v1/pricing* that no repo in this fleet can see
//
// THE RATCHET. Every line in the ratchet file is `METHOD /path`: an address this
// document publishes that production does not route, each one a named defect
// owned by someone. The file may only SHRINK. A 404 that is not in it fails the
// release; a line in it that now answers is a line to delete. That is what keeps
// a defect being closed from blocking every release while it is open, without
// letting a new one in — and unlike an allowlist it names what is wrong instead
// of hiding that anything is.
//
// PRODUCTION IS ASKED ABOUT LITERAL ADDRESSES ONLY. A sentinel in a {param} is
// not a harmless read there: a handler that opens a store for the org a path
// names made an org called zzz-reach-probe in production. So against any base
// but the scratch instance (*.hanzo.lol) or this machine, reach probes the
// literal addresses, which are also the ones an edge can intercept, and leaves
// the parameterised half of the document to a run against scratch.
//
// This was openapi/reach.py. It is Go now for one reason: cloud is a Go service
// — 2000+ .go files and no Python in the shipped image — and the only thing that
// needed a Python runtime was this gate. On Ubuntu 24.04 that turned into a
// dependency the runner refuses to install (PEP 668: `pip install` answers
// "externally-managed-environment"), so a gate about routing kept failing over
// package management. gopkg.in/yaml.v3 is already a direct dependency here, so
// this costs nothing to build and nothing to provision.
//
// Usage: reach <spec.yaml> <base-url> <ratchet-file>
package main
