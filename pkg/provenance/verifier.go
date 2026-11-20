package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/sigstore/sigstore/pkg/signature/payload"
)

type Verifier struct {
	PubKey signature.Verifier
}

func NewVerifier(pubKey signature.Verifier) *Verifier {
	return &Verifier{PubKey: pubKey}
}

// Verify checks the signature and ensures the artifact digest matches the signed provenance
func (v *Verifier) Verify(ctx context.Context, artifactData []byte, provenanceJSON []byte, signature []byte) error {
	// 1. Verify Signature
	err := v.PubKey.VerifySignature(signature, provenanceJSON)
	if err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	// 2. Parse Provenance
	var p payload.SimpleContainerImage
	if err := json.Unmarshal(provenanceJSON, &p); err != nil {
		return fmt.Errorf("failed to parse provenance: %w", err)
	}

	// 3. Verify Digest Match
	actualDigest := "sha256:" + GenerateDigest(artifactData)
	if p.Critical.Image.DockerManifestDigest != actualDigest {
		return errors.New("artifact digest mismatch: tampering detected")
	}

	return nil
}
