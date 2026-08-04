package cloud

// Version is the API contract/build version emitted as the X-Api-Version
// response header — the support-correlation build signal (the brand-neutral
// analog of the image tag). Stamped at link time by the release build:
//
//	-ldflags "-X github.com/hanzoai/cloud.Version=<image tag>"
//
// and overridable at runtime by CLOUD_VERSION, which the operator sets from the
// deployed image tag without a rebuild (the same channel as CLOUD_BRAND).
// Defaults to "dev" for an untagged go build / go test.
var Version = "dev"

// revision is the commit these BYTES were built from, written by the linker and
// by nothing else:
//
//	-ldflags "-X github.com/hanzoai/cloud.revision=<40-hex>"
//
// Unexported, and with no environment fallback ON PURPOSE — that is the whole
// difference between it and Version. Version answers "what did someone CALL this
// build": an operator's label, which CLOUD_VERSION can restate on a pod running
// any image at all. revision answers "what source is actually in here", and a
// value that can be restated from outside cannot answer that, because an env var
// can name a commit it was never built from.
//
// v1.801.426 was pinned, rolled out and served traffic while the job meant to
// build it sat Failed: the tag was right, the image was built from older source,
// and both fixes it was supposed to carry were missing. Nothing detected it,
// because nothing could ask the process — /v1/health returned {"status":"ok"}
// and nothing else, /v1/version was a 404, and establishing the truth took
// exec-ing into the pod to read a panic out of a second binary.
var revision string

// Revision reports the commit this binary was built from, or "unknown".
//
// UNKNOWN IS A VALUE, NOT A BLANK. Only a full 40-char lowercase hex object name
// is ever reported; an unexpanded "${REVISION}", a branch name, "dev", a short
// sha and the empty string all read "unknown". A near-miss is worse than no
// answer at all, because someone acts on it.
//
// That matters here specifically: `-X` naming a symbol the linker cannot resolve
// is not an error, it is dropped SILENTLY. A stamp that reached nothing must
// therefore read as the loud, useless "unknown" rather than as something
// plausible — and the image build proves the stamp landed by grepping its own
// linked binaries, because a flag that was merely REQUESTED still appears in
// `go version -m` output when the symbol was never set.
func Revision() string {
	if !IsCommit(revision) {
		return "unknown"
	}
	return revision
}

// IsCommit reports whether s names a git commit: a full 40-char lowercase hex
// object name, exactly.
//
// It is the ONE rule for that, applied at BOTH ends of the same wire — the
// builder refuses to pass build-arg:REVISION unless it holds (apps/platform),
// and this process refuses to report a value unless it holds — so the two cannot
// drift into disagreeing about what they hand each other. A builder rule laxer
// than the reader's would pass a branch name that silently became "unknown",
// which is this outage's failure mode wearing a different hat.
func IsCommit(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
