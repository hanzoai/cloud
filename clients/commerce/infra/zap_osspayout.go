// ZAP-native data path for the SBOM-driven OSS-developer payout system.
//
// The SBOM emitted on every deploy is inter-service data (arcd build runner ->
// commerce). Per the unified-binary contract, NEW inter-subsystem data paths
// are ZAP-native (not tRPC/gRPC). This file adds the OpSBOMIngest opcode and a
// registration hook so the arcd runner — or any in-cluster service — can push
// an image's SBOM over the same ZAP node that already serves vector ops, using
// the identical [status(1)][reserved(7)][JSON payload] framing.
//
// Separation of concerns: the ZAPNode owns transport + framing only. The actual
// SBOM persistence lives in the sbomrecord model (one Ingest path shared with
// the HTTP handler); the handler here is a thin adapter that decodes the ZAP
// payload and calls an injected store function. The node is NOT coupled to the
// datastore — commerce.go wires the store closure at startup.
package infra

import (
	"context"
	"encoding/json"

	"github.com/luxfi/zap"
)

// OpSBOMIngest is the ZAP opcode for SBOM ingestion. It sits in a distinct
// range from the vector ops (0x10–0x12) so the two concerns never collide.
const OpSBOMIngest uint16 = 0x20

// SBOMIngestComponent mirrors the commerce SBOM ingest component shape. Kept
// here (not imported from api/billing) so infra stays free of HTTP-layer deps.
type SBOMIngestComponent struct {
	PURL      string `json:"purl"`
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
	Version   string `json:"version"`
	Scope     string `json:"scope"`
}

// SBOMIngestPayload is the JSON body carried in the ZAP OpSBOMIngest message.
type SBOMIngestPayload struct {
	ImageRef    string                `json:"imageRef"`
	ImageDigest string                `json:"imageDigest"`
	Service     string                `json:"service"`
	Format      string                `json:"format"`
	Tool        string                `json:"tool"`
	Components  []SBOMIngestComponent `json:"components"`
}

// SBOMStore is the injected persistence function the ZAP handler calls. It is
// implemented in commerce.go by adapting sbomrecord.Ingest against a
// system-namespaced datastore — the SAME path the HTTP handler uses, so there
// is exactly one way SBOMs are stored regardless of transport.
type SBOMStore func(ctx context.Context, p SBOMIngestPayload) error

// RegisterSBOMIngest wires the OpSBOMIngest handler onto the node. Call after
// NewZAPNode and before/after Start (handlers may be registered any time). When
// store is nil the opcode is not registered (feature off).
func (z *ZAPNode) RegisterSBOMIngest(store SBOMStore) {
	if z == nil || z.node == nil || store == nil {
		return
	}
	z.node.Handle(OpSBOMIngest, sbomHandlerFor(store))
}

// sbomHandlerFor builds the OpSBOMIngest handler closure for a store. Extracted
// from RegisterSBOMIngest so the decode/validate logic is unit-testable without
// a live node.
func sbomHandlerFor(store SBOMStore) func(context.Context, string, *zap.Message) (*zap.Message, error) {
	return func(ctx context.Context, _ string, msg *zap.Message) (*zap.Message, error) {
		payload := msg.Root().Bytes(zapFieldPayload)
		if payload == nil {
			return zapError("empty payload"), nil
		}
		var p SBOMIngestPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return zapError("bad request: " + err.Error()), nil
		}
		if p.ImageDigest == "" {
			return zapError("imageDigest is required"), nil
		}
		if err := store(ctx, p); err != nil {
			return zapError(err.Error()), nil
		}
		return zapOK(nil), nil
	}
}
