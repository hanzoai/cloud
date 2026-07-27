package platform

import (
	"strings"

	"github.com/hanzoai/cloud/clients/cd"
)

// handoff.go is the CI→CD seam.
//
// THE COMPLECTION
//
// releaseFor builds a four-stage plan — build → smoke → tag → notify — and only
// the first three answer CI's question ("is this good, and what did it produce?").
// The fourth, rolloutRelease, answers CD's ("what is live?"), and it answers it
// TWICE: once by patching the Service CR directly, once by mirroring an image
// update into universe, with a fallback between them. So "what is live" has two
// writers that can disagree, and when they do the fallback hides it — the CR
// patch fails, the mirror succeeds, and the cluster and git now describe
// different production states with nothing reporting a problem.
//
// That is also why a release cannot be rolled back: neither writer records WHICH
// artifact was placed, only that it was pushed somewhere. Rollback needs the set
// of placements, and no one was keeping it.
//
// THE SEPARATION
//
// A release run produces exactly one durable fact — a verified, immutable
// artifact at a known reference. That fact is where CI ends. Everything after it
// (where it goes, what it replaces, how to undo it) is one lifecycle, and it is
// clients/cd's, for images and bundles alike.
//
// Artifact is that fact, expressed in the vocabulary cd already speaks. It is
// deliberately the ONLY thing this package hands over: a smaller seam than
// "call the deployer" means the runner cannot grow deployment opinions again.
//
// NOT YET LOAD-BEARING: rolloutRelease still runs. This seam exists so the
// switch is a one-line change at the call site once clients/cd is mounted and a
// workload Target is registered — and so the two-writer problem is named in the
// code rather than rediscovered later.

// Artifact describes what a completed release run produced. It is built only
// AFTER smoke passes, because an unverified image is not a release — handing one
// to cd would let a broken build become a Placement and, worse, a rollback target
// someone later selects during an incident.
//
// Digest is the image reference pinned by content, when the builder reports one;
// an empty Digest means the tag is the only identity we have. cd stores whatever
// it is given, so recording the weaker identity honestly beats inventing a
// stronger-looking one.
func Artifact(image, digest string) cd.Artifact {
	image = strings.TrimSpace(image)
	digest = strings.TrimSpace(digest)
	ref := image
	if digest != "" {
		// Prefer the immutable form: a tag can be moved, a digest cannot, and a
		// rollback that resolves a moved tag would quietly deploy the wrong bytes.
		if at := strings.LastIndex(image, "@"); at >= 0 {
			ref = image[:at] + "@" + digest
		} else if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
			ref = image[:colon] + "@" + digest
		} else {
			ref = image + "@" + digest
		}
	}
	return cd.Artifact{Kind: cd.KindImage, Ref: ref, Digest: digest}
}
