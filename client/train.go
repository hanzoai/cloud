// Copyright © 2026 Hanzo AI. MIT License.

package client

import (
	"encoding/json"
	"strings"
)

// ---- train: a decision's captured states and its corrections, from ai --------
//
// ai answers /v1/decisions and sees what Kai captured of each one; train keeps
// the org's captures, corrections and capabilities (apps/train capabilities.go).
// ai hands a decision's states over and relays /v1/decisions/:id/corrections,
// which is ai's address by the Decisions wire, to the store that holds them. The
// org rides the caller in every op.

const (
	// TrainCapturing answers whether the caller's org captures its decisions'
	// states. ai asks before it forwards a decision to Kai.
	TrainCapturing = "train_capturing"
	// TrainCapture files one decision's captured states for the caller's org.
	TrainCapture = "train_capture"
	// TrainCorrect files a correction of one of the caller's org's decisions.
	TrainCorrect = "train_correct"
	// TrainCorrections lists one decision's corrections.
	TrainCorrections = "train_corrections"
	// TrainUncorrect deletes one correction of a decision.
	TrainUncorrect = "train_uncorrect"
	// TrainCapabilityOwned answers whether the caller's org owns a publishable
	// Kai capability of the given name, with its artifact metadata and evaluation.
	TrainCapabilityOwned = "train_capability_owned"
	// TrainCapabilityInstall installs or uninstalls a shared Kai capability from
	// the marketplace into the caller's org's published set.
	TrainCapabilityInstall = "train_capability_install"
	// TrainCapabilityWeights reads a published Kai capability's .tar weights for
	// open-source download.
	TrainCapabilityWeights = "train_capability_weights"
)

const capabilityResourcePrefix = "capability:"

// CapabilityResource names one Kai capability artifact as an x402 resource and
// marketplace reference: the publisher's scope and the artifact's SHA-256.
func CapabilityResource(source, sha256 string) string {
	return capabilityResourcePrefix + source + ":" + sha256
}

// CapabilityOf reads one back. ok=false means the resource is not a capability.
func CapabilityOf(resource string) (source, sha256 string, ok bool) {
	rest, ok := strings.CutPrefix(resource, capabilityResourcePrefix)
	if !ok {
		return "", "", false
	}
	source, sha256, ok = strings.Cut(rest, ":")
	return source, sha256, ok && source != "" && sha256 != ""
}

// Capturing is whether the caller's org captures its decisions' states.
type Capturing struct {
	On bool `json:"on"`
}

// CaptureIn is one decision's captured states, as Kai answered them beside its
// answers (`capture`), under the id the caller was answered.
type CaptureIn struct {
	// Decision is the decision's id.
	Decision string `json:"decision"`
	// Request is the request id it was answered under.
	Request string `json:"request,omitempty"`
	// Model is the model the caller asked for.
	Model string `json:"model,omitempty"`
	// States is Kai's `capture` object.
	States json.RawMessage `json:"states"`
}

// Kept says whether a capture was kept.
type Kept struct {
	Stored bool `json:"stored"`
}

// CorrectIn is a correction of one question of a decision: the right answer, an
// option label of that question, and why.
type CorrectIn struct {
	// Decision is the decision's id.
	Decision string `json:"decision"`
	// Question is the question's key.
	Question string `json:"question"`
	// Answer is the right option label; a score's level may be named by its
	// index.
	Answer string `json:"answer"`
	// Note says why, for the person reading it later.
	Note string `json:"note,omitempty"`
	// Cohort groups corrections the org files together.
	Cohort string `json:"cohort,omitempty"`
}

// Correction is a correction as the org's store holds it.
type Correction struct {
	ID       string   `json:"id"`
	Decision string   `json:"decision"`
	Question string   `json:"question"`
	Type     string   `json:"type"`
	Labels   []string `json:"labels"`
	// Answer is the right answer; Base what Kai answered.
	Answer string `json:"answer"`
	Base   string `json:"base"`
	Note   string `json:"note,omitempty"`
	Cohort string `json:"cohort,omitempty"`
	// State is captured when the decision's state is held and the correction can
	// teach, missing when the decision was made with capture off.
	State string `json:"state"`
	By    string `json:"by"`
	At    int64  `json:"at"`
}

// DecisionIn names one of the caller's org's decisions.
type DecisionIn struct {
	Decision string `json:"decision"`
}

// Corrections is a decision's corrections, and the questions Kai captured of it:
// what a correction may name.
type Corrections struct {
	Data []Correction `json:"data"`
	// Questions are the decision's captured questions, by key; none when it was
	// made with capture off.
	Questions []Captured `json:"questions"`
}

// Captured is one question of a captured decision: its options, and the label
// the caller was answered.
type Captured struct {
	Key    string   `json:"key"`
	Type   string   `json:"type"`
	Labels []string `json:"labels"`
	// Answered is the label the caller was answered: the org's capability's when
	// one answered, else Kai's own.
	Answered string `json:"answered"`
}

// UncorrectIn names one correction of one decision.
type UncorrectIn struct {
	Decision string `json:"decision"`
	ID       string `json:"id"`
}

// CapabilityOwnedIn asks whether the caller's org owns a publishable Kai
// capability of the given name.
type CapabilityOwnedIn struct {
	Name string `json:"name" validate:"required"`
}

// CapabilityOwned is train's answer to a marketplace ownership check for a Kai
// capability: its artifact hashes, shared representation, route and verified
// evaluation summary.
type CapabilityOwned struct {
	Mine           bool     `json:"mine"`
	Name           string   `json:"name,omitempty"`
	SHA256         string   `json:"sha256,omitempty"`
	BaseSHA256     string   `json:"baseSha256,omitempty"`
	Representation string   `json:"representation,omitempty"`
	Form           string   `json:"form,omitempty"`
	Rank           int      `json:"rank,omitempty"`
	Bytes          int64    `json:"bytes,omitempty"`
	QuestionKey    string   `json:"questionKey,omitempty"`
	QuestionType   string   `json:"questionType,omitempty"`
	Labels         []string `json:"labels,omitempty"`
	Repairs        int      `json:"repairs,omitempty"`
	Damage         int      `json:"damage,omitempty"`
	FireRate       float64  `json:"fireRate,omitempty"`
	Specificity    float64  `json:"specificity,omitempty"`
	OODMoved       int      `json:"oodMoved,omitempty"`
	LatencyUs      float64  `json:"latencyUs,omitempty"`
}

// CapabilityInstallIn installs or uninstalls a shared Kai capability from the
// marketplace into the caller's org's published set.
type CapabilityInstallIn struct {
	// Ref is CapabilityResource(source, sha256).
	Ref  string `json:"ref" validate:"required"`
	Name string `json:"name" validate:"required"`
	On   bool   `json:"on"`
}

// CapabilityInstalled is a capability's installation state after the write.
type CapabilityInstalled struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256,omitempty"`
	Installed bool   `json:"installed"`
}

// CapabilityWeightsIn asks for a published Kai capability's .tar weights.
type CapabilityWeightsIn struct {
	// Ref is CapabilityResource(source, sha256).
	Ref  string `json:"ref" validate:"required"`
	Name string `json:"name" validate:"required"`
}

// CapabilityWeights carries a capability's .tar weights (base64) and its
// representation metadata for open-source distribution.
type CapabilityWeights struct {
	Name           string `json:"name"`
	Version        int    `json:"version"`
	SHA256         string `json:"sha256"`
	BaseSHA256     string `json:"base_sha256"`
	Representation string `json:"representation"`
	Form           string `json:"form"`
	Bytes          int64  `json:"bytes"`
	Tar            string `json:"tar"`
}
