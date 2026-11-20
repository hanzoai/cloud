package provenance

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"

	"github.com/sigstore/cosign/v2/pkg/cosign"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/sigstore/sigstore/pkg/signature/payload"
)

type Signer struct {
	PrivKey signature.Signer
}

func NewSigner(privKey signature.Signer) *Signer {
	return &Signer{PrivKey: privKey}
}

// SignArtifact generates a SLSA attestation and signs it using Cosign logic
func (s *Signer) SignArtifact(ctx context.Context, artifactName string, artifactData []byte, agentID string, sourceCommit string) ([]byte, []byte, error) {
	digest := GenerateDigest(artifactData)

	predicate := SLSAPredicate{}
	predicate.Builder.ID = fmt.Sprintf("hanzo-cloud-agent-%s", agentID)
	predicate.BuildType = "https://hanzo.ai/slsa/agent-build/v1"
	predicate.Invocation.ConfigSource.URI = "github.com/hanzoai/forge"
	predicate.Invocation.ConfigSource.Digest = map[string]string{"sha1": sourceCommit}
	predicate.Metadata.BuildFinishedOn = time.Now()
	
	predicateBytes, err := json.Marshal(predicate)
	if err != nil {
		return nil, nil, err
	}

	// Create Cosign-compatible payload
	wrappedPayload := payload.SimpleContainerImage{
		Critical: payload.Critical{
			Identity: payload.Identity{
				DockerReference: artifactName,
			},
			Image: payload.Image{
				DockerManifestDigest: "sha256:" + digest,
			},
			Type: "Hanzo Agent Artifact Attestation",
		},
		Optional: map[string]interface{}{
			"slsa": predicate,
		},
	}

	payloadBytes, err := json.Marshal(wrappedPayload)
	if err != nil {
		return nil, nil, err
	}

	// Sign the payload
	sig, err := s.PrivKey.SignMessage(nil, payloadBytes)
	if err != nil {
		return nil, nil, err
	}

	return payloadBytes, sig, nil
}
