package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// SLSA v1.2 Predicate structure
// Reference: https://slsa.dev/spec/v1.2/provenance
type SLSAPredicate struct {
	Builder struct {
		ID string `json:"id"`
	} `json:"builder"`
	BuildType string `json:"buildType"`
	Invocation struct {
		ConfigSource struct {
			URI    string            `json:"uri"`
			Digest map[string]string `json:"digest"`
		} `json:"configSource"`
		Parameters map[string]interface{} `json:"parameters"`
	} `json:"invocation"`
	Metadata struct {
		BuildStartedOn  time.Time `json:"buildStartedOn"`
		BuildFinishedOn time.Time `json:"buildFinishedOn"`
		Completeness    struct {
			Parameters  bool `json:"parameters"`
			Environment bool `json:"environment"`
			Materials   bool `json:"materials"`
		} `json:"completeness"`
		Reproducible bool `json:"reproducible"`
	} `json:"metadata"`
	Materials []Material `json:"materials"`
}

type Material struct {
	URI    string            `json:"uri"`
	Digest map[string]string `json:"digest"`
}

// GenerateDigest computes the SHA256 hex string for a byte slice
func GenerateDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
