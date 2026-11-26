// SPDX-License-Identifier: Apache-2.0

package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"time"
)

const (
	// StatementTypeV1 is the canonical in-toto v1 statement type URI.
	StatementTypeV1 = "https://in-toto.io/Statement/v1"

	// SLSAProvenanceV12 is the canonical SLSA v1.2 provenance predicate URI.
	SLSAProvenanceV12 = "https://slsa.dev/provenance/v1.2"

	// InTotoPayloadType is the canonical DSSE payloadType for in-toto statements.
	InTotoPayloadType = "application/vnd.in-toto+json"

	// DefaultBuildType is the default Hanzo forge agent buildType URI.
	DefaultBuildType = "https://hanzo.ai/forge/agent-build/v1"

	// BundleMediaType identifies a serialized Hanzo SLSA provenance bundle.
	BundleMediaType = "application/vnd.hanzo.provenance.bundle+json;version=0.1"
)

// DigestSet maps lowercase algorithm names (such as "sha256") to lowercase hex digests.
type DigestSet map[string]string

// Clone returns a deep copy of the DigestSet.
func (d DigestSet) Clone() DigestSet {
	if d == nil {
		return nil
	}
	return maps.Clone(d)
}

// ResourceDescriptor identifies a software artifact or dependency per in-toto v1.
type ResourceDescriptor struct {
	Name        string            `json:"name"`
	URI         string            `json:"uri,omitempty"`
	Digest      DigestSet         `json:"digest"`
	MediaType   string            `json:"mediaType,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Clone returns a deep copy of the ResourceDescriptor.
func (r ResourceDescriptor) Clone() ResourceDescriptor {
	out := r
	out.Digest = r.Digest.Clone()
	if r.Annotations != nil {
		out.Annotations = maps.Clone(r.Annotations)
	}
	return out
}

// SourceRef identifies the exact source repository and Git revision built.
type SourceRef struct {
	URI    string    `json:"uri"`
	Branch string    `json:"branch,omitempty"`
	Commit string    `json:"commit"`
	Tree   string    `json:"tree,omitempty"`
	Digest DigestSet `json:"digest,omitempty"`
}

// Clone returns a deep copy of SourceRef.
func (s SourceRef) Clone() SourceRef {
	out := s
	out.Digest = s.Digest.Clone()
	return out
}

// ToolchainComponent pins a compiler, runtime, or build tool by cryptographic digest.
type ToolchainComponent struct {
	Name    string    `json:"name"`
	Version string    `json:"version"`
	URI     string    `json:"uri,omitempty"`
	Digest  DigestSet `json:"digest"`
}

// Clone returns a deep copy of ToolchainComponent.
func (t ToolchainComponent) Clone() ToolchainComponent {
	out := t
	out.Digest = t.Digest.Clone()
	return out
}

// ExternalParameters carries caller/trigger-controlled inputs to the build per SLSA v1.2.
type ExternalParameters struct {
	Source          SourceRef            `json:"source"`
	BuilderIdentity string               `json:"builderIdentity"`
	Toolchain       []ToolchainComponent `json:"toolchain"`
	BuildConfig     map[string]string    `json:"buildConfig,omitempty"`
	InvocationOrg   string               `json:"invocationOrg,omitempty"`
}

// Clone returns a deep copy of ExternalParameters.
func (e ExternalParameters) Clone() ExternalParameters {
	out := e
	out.Source = e.Source.Clone()
	if e.Toolchain != nil {
		out.Toolchain = make([]ToolchainComponent, len(e.Toolchain))
		for i, tc := range e.Toolchain {
			out.Toolchain[i] = tc.Clone()
		}
	}
	if e.BuildConfig != nil {
		out.BuildConfig = maps.Clone(e.BuildConfig)
	}
	return out
}

// BuildDefinition describes the build type, external/internal parameters, and resolved inputs.
type BuildDefinition struct {
	BuildType            string               `json:"buildType"`
	ExternalParameters   ExternalParameters   `json:"externalParameters"`
	InternalParameters   map[string]string    `json:"internalParameters,omitempty"`
	ResolvedDependencies []ResourceDescriptor `json:"resolvedDependencies,omitempty"`
}

// Clone returns a deep copy of BuildDefinition.
func (b BuildDefinition) Clone() BuildDefinition {
	out := b
	out.ExternalParameters = b.ExternalParameters.Clone()
	if b.InternalParameters != nil {
		out.InternalParameters = maps.Clone(b.InternalParameters)
	}
	if b.ResolvedDependencies != nil {
		out.ResolvedDependencies = make([]ResourceDescriptor, len(b.ResolvedDependencies))
		for i, dep := range b.ResolvedDependencies {
			out.ResolvedDependencies[i] = dep.Clone()
		}
	}
	return out
}

// Builder identifies the trusted build platform or agent runner.
type Builder struct {
	ID                  string               `json:"id"`
	BuilderDependencies []ResourceDescriptor `json:"builderDependencies,omitempty"`
	Version             map[string]string    `json:"version,omitempty"`
}

// Clone returns a deep copy of Builder.
func (b Builder) Clone() Builder {
	out := b
	if b.BuilderDependencies != nil {
		out.BuilderDependencies = make([]ResourceDescriptor, len(b.BuilderDependencies))
		for i, dep := range b.BuilderDependencies {
			out.BuilderDependencies[i] = dep.Clone()
		}
	}
	if b.Version != nil {
		out.Version = maps.Clone(b.Version)
	}
	return out
}

// BuildMetadata records execution timestamps and invocation identity.
type BuildMetadata struct {
	InvocationID string    `json:"invocationId"`
	StartedOn    time.Time `json:"startedOn"`
	FinishedOn   time.Time `json:"finishedOn"`
}

// RunDetails records build execution details per SLSA v1.2.
type RunDetails struct {
	Builder    Builder              `json:"builder"`
	Metadata   BuildMetadata        `json:"metadata"`
	Byproducts []ResourceDescriptor `json:"byproducts,omitempty"`
}

// Clone returns a deep copy of RunDetails.
func (r RunDetails) Clone() RunDetails {
	out := r
	out.Builder = r.Builder.Clone()
	if r.Byproducts != nil {
		out.Byproducts = make([]ResourceDescriptor, len(r.Byproducts))
		for i, bp := range r.Byproducts {
			out.Byproducts[i] = bp.Clone()
		}
	}
	return out
}

// ProvenancePredicate is the SLSA v1.2 provenance predicate body.
type ProvenancePredicate struct {
	BuildDefinition BuildDefinition `json:"buildDefinition"`
	RunDetails      RunDetails      `json:"runDetails"`
}

// Clone returns a deep copy of ProvenancePredicate.
func (p ProvenancePredicate) Clone() ProvenancePredicate {
	return ProvenancePredicate{
		BuildDefinition: p.BuildDefinition.Clone(),
		RunDetails:      p.RunDetails.Clone(),
	}
}

// Statement is a canonical in-toto v1 statement carrying an SLSA v1.2 provenance predicate.
type Statement struct {
	Type          string               `json:"_type"`
	Subject       []ResourceDescriptor `json:"subject"`
	PredicateType string               `json:"predicateType"`
	Predicate     ProvenancePredicate  `json:"predicate"`
}

// Clone returns a deep copy of Statement.
func (s Statement) Clone() Statement {
	out := s
	if s.Subject != nil {
		out.Subject = make([]ResourceDescriptor, len(s.Subject))
		for i, sub := range s.Subject {
			out.Subject[i] = sub.Clone()
		}
	}
	out.Predicate = s.Predicate.Clone()
	return out
}

// NewStatement constructs a validated SLSA v1.2 in-toto Statement.
func NewStatement(subjects []ResourceDescriptor, buildDef BuildDefinition, runDetails RunDetails) (*Statement, error) {
	if buildDef.BuildType == "" {
		buildDef.BuildType = DefaultBuildType
	}
	stmt := &Statement{
		Type:          StatementTypeV1,
		Subject:       make([]ResourceDescriptor, len(subjects)),
		PredicateType: SLSAProvenanceV12,
		Predicate: ProvenancePredicate{
			BuildDefinition: buildDef.Clone(),
			RunDetails:      runDetails.Clone(),
		},
	}
	for i, sub := range subjects {
		stmt.Subject[i] = sub.Clone()
	}
	if err := ValidateStatement(stmt); err != nil {
		return nil, err
	}
	return stmt, nil
}

// ValidateStatement enforces canonical SLSA v1.2 structural and digest invariants.
func ValidateStatement(stmt *Statement) error {
	if stmt == nil {
		return fmt.Errorf("%w: nil statement", ErrStatementInvalid)
	}
	if stmt.Type != StatementTypeV1 {
		return fmt.Errorf("%w: unsupported statement _type %q (want %q)", ErrStatementInvalid, stmt.Type, StatementTypeV1)
	}
	if stmt.PredicateType != SLSAProvenanceV12 {
		return fmt.Errorf("%w: unsupported predicateType %q (want %q)", ErrStatementInvalid, stmt.PredicateType, SLSAProvenanceV12)
	}
	if len(stmt.Subject) == 0 {
		return fmt.Errorf("%w: statement must contain at least one subject", ErrStatementInvalid)
	}
	for i, sub := range stmt.Subject {
		if strings.TrimSpace(sub.Name) == "" {
			return fmt.Errorf("%w: subject[%d] name is required", ErrStatementInvalid, i)
		}
		if err := ValidateSHA256Hex(sub.Digest["sha256"]); err != nil {
			return fmt.Errorf("%w: subject[%d] (%s) invalid sha256 digest: %v", ErrStatementInvalid, i, sub.Name, err)
		}
	}
	bd := stmt.Predicate.BuildDefinition
	if strings.TrimSpace(bd.BuildType) == "" {
		return fmt.Errorf("%w: buildDefinition.buildType is required", ErrStatementInvalid)
	}
	src := bd.ExternalParameters.Source
	if strings.TrimSpace(src.URI) == "" {
		return fmt.Errorf("%w: externalParameters.source.uri is required", ErrStatementInvalid)
	}
	if err := ValidateGitRevision(src.Commit); err != nil {
		return fmt.Errorf("%w: externalParameters.source.commit invalid: %v", ErrStatementInvalid, err)
	}
	if src.Tree != "" {
		if err := ValidateGitRevision(src.Tree); err != nil {
			return fmt.Errorf("%w: externalParameters.source.tree invalid: %v", ErrStatementInvalid, err)
		}
	}
	if strings.TrimSpace(bd.ExternalParameters.BuilderIdentity) == "" {
		return fmt.Errorf("%w: externalParameters.builderIdentity is required", ErrStatementInvalid)
	}
	for i, tc := range bd.ExternalParameters.Toolchain {
		if strings.TrimSpace(tc.Name) == "" || strings.TrimSpace(tc.Version) == "" {
			return fmt.Errorf("%w: toolchain[%d] requires name and version", ErrStatementInvalid, i)
		}
		if err := ValidateSHA256Hex(tc.Digest["sha256"]); err != nil {
			return fmt.Errorf("%w: toolchain[%d] (%s) invalid sha256 digest: %v", ErrStatementInvalid, i, tc.Name, err)
		}
	}
	rd := stmt.Predicate.RunDetails
	if strings.TrimSpace(rd.Builder.ID) == "" {
		return fmt.Errorf("%w: runDetails.builder.id is required", ErrStatementInvalid)
	}
	if strings.TrimSpace(rd.Metadata.InvocationID) == "" {
		return fmt.Errorf("%w: runDetails.metadata.invocationId is required", ErrStatementInvalid)
	}
	if rd.Metadata.StartedOn.IsZero() || rd.Metadata.FinishedOn.IsZero() {
		return fmt.Errorf("%w: runDetails.metadata.startedOn and finishedOn are required", ErrStatementInvalid)
	}
	if rd.Metadata.FinishedOn.Before(rd.Metadata.StartedOn) {
		return fmt.Errorf("%w: runDetails.metadata.finishedOn precedes startedOn", ErrStatementInvalid)
	}
	return nil
}

// ValidateSHA256Hex verifies s is a canonical 64-character lowercase hexadecimal SHA-256 digest.
func ValidateSHA256Hex(s string) error {
	if len(s) != 64 {
		return errors.New("sha256 digest must be 64 lowercase hex characters")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return errors.New("sha256 digest must be lowercase hex")
		}
	}
	return nil
}

// ValidateGitRevision verifies s is a canonical lowercase hex Git SHA-1 (40) or SHA-256 (64) object ID.
func ValidateGitRevision(s string) error {
	if len(s) != 40 && len(s) != 64 {
		return errors.New("git revision must be 40 or 64 lowercase hex characters")
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return errors.New("git revision must be lowercase hex")
		}
	}
	return nil
}

// DigestBytes returns the canonical lowercase hexadecimal SHA-256 digest of data.
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DigestReader streams r through SHA-256 without buffering the full payload in memory.
func DigestReader(r io.Reader) (digest string, size int64, err error) {
	if r == nil {
		return "", 0, errors.New("nil reader")
	}
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// DigestFile computes the streaming SHA-256 digest and byte size of the file at path.
func DigestFile(path string) (digest string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()
	return DigestReader(f)
}
