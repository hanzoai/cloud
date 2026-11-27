package marketplace

// capability.go — a Kai capability (kind capability) shared or sold across orgs:
// trained over Kai's shared latent representation, verified against the lesson
// gate (repairs, damage, OOD preservation and latency), and either open-sourced
// ($0, downloadable .tar weights) or monetized per fire over x402.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/client"
	trainpeer "github.com/hanzoai/cloud/client/train"
	"github.com/hanzoai/cloud/money"
	"github.com/zap-proto/zip"
)

// Licenses are the open-source and commercial licenses a capability listing may
// declare. A free capability defaults to Apache-2.0; a priced one is commercial.
var Licenses = []string{"Apache-2.0", "MIT", "BSD-3-Clause", "CC-BY-4.0", "commercial"}

// CapabilitySpec is the Kai capability metadata behind a listing of kind
// capability: its artifact hashes, the shared Kai representation it binds to,
// the route it answers, its verified evaluation metrics, and its license.
type CapabilitySpec struct {
	// SHA256 is the capability .tar artifact's SHA-256.
	SHA256 string `json:"sha256"`
	// BaseSHA256 is the Kai base weights' SHA-256 it was taught over.
	BaseSHA256 string `json:"baseSha256"`
	// Representation is the shared Kai latent representation hash (weights,
	// tokenizer, layout and pooling) the capability binds to.
	Representation string `json:"representation"`
	// Form is calibration, head, pair or full.
	Form string `json:"form"`
	// Rank is the correction's place on the form ladder (1..4).
	Rank int `json:"rank"`
	// Bytes is the artifact's size in bytes.
	Bytes int64 `json:"bytes"`
	// QuestionKey is the question key the capability routes.
	QuestionKey string `json:"questionKey"`
	// QuestionType is choice, score or noul.
	QuestionType string `json:"questionType"`
	// Labels are the question's option labels in order.
	Labels []string `json:"labels"`
	// Repairs is the count of held-out mistakes repaired during evaluation.
	Repairs int `json:"repairs"`
	// Damage is the count of held-out regressions on preserve rows.
	Damage int `json:"damage"`
	// FireRate is the fraction of routed decisions the capability modifies.
	FireRate float64 `json:"fireRate"`
	// Specificity is the fraction of preserve rows left untouched.
	Specificity float64 `json:"specificity"`
	// OODMoved is the number of out-of-distribution questions moved (0 required).
	OODMoved int `json:"oodMoved"`
	// LatencyUs is the measured CPU overhead in microseconds per decision.
	LatencyUs float64 `json:"latencyUs"`
	// License is Apache-2.0, MIT, BSD-3-Clause, CC-BY-4.0, or commercial.
	License string `json:"license"`
}

// capabilityIn carries optional publisher preferences when listing or updating a
// Kai capability.
type capabilityIn struct {
	// License names the open-source license ("Apache-2.0", "MIT", "BSD-3-Clause",
	// "CC-BY-4.0") for a free capability, or "commercial" for a priced one.
	License string `json:"license"`
}

// ShopCapabilityWeights is the downloadable .tar artifact (base64) and shared
// representation metadata of an open-source Kai capability listing.
type ShopCapabilityWeights struct {
	// Name is the capability's slug.
	Name string `json:"name"`
	// Version is the capability version number.
	Version int `json:"version"`
	// SHA256 is the capability .tar artifact's SHA-256.
	SHA256 string `json:"sha256"`
	// BaseSHA256 is the Kai base weights' SHA-256 it was taught over.
	BaseSHA256 string `json:"base_sha256"`
	// Representation is the shared Kai latent representation hash.
	Representation string `json:"representation"`
	// Form is calibration, head, pair or full.
	Form string `json:"form"`
	// Bytes is the artifact's size in bytes.
	Bytes int64 `json:"bytes"`
	// Tar is the base64-encoded .tar archive of the capability's weights.
	Tar string `json:"tar"`
}

func resolveLicense(price money.Amount, in *capabilityIn, current string) (string, error) {
	if price.Sign() > 0 {
		if in != nil && strings.TrimSpace(in.License) != "" && strings.TrimSpace(in.License) != "commercial" {
			return "", zip.ErrBadRequest("a priced capability listing uses license commercial; set price to 0 for an open-source license")
		}
		return "commercial", nil
	}
	lic := current
	if in != nil && strings.TrimSpace(in.License) != "" {
		lic = strings.TrimSpace(in.License)
	}
	if lic == "" || lic == "commercial" {
		lic = "Apache-2.0"
	}
	if !slices.Contains(Licenses, lic) {
		return "", zip.ErrBadRequest("license is one of " + strings.Join(Licenses, ", "))
	}
	return lic, nil
}

func capabilityFromOwned(o *client.CapabilityOwned, price money.Amount, in *capabilityIn) (*CapabilitySpec, error) {
	lic, err := resolveLicense(price, in, "")
	if err != nil {
		return nil, err
	}
	labels := o.Labels
	if labels == nil {
		labels = []string{}
	}
	return &CapabilitySpec{
		SHA256:         o.SHA256,
		BaseSHA256:     o.BaseSHA256,
		Representation: o.Representation,
		Form:           o.Form,
		Rank:           o.Rank,
		Bytes:          o.Bytes,
		QuestionKey:    o.QuestionKey,
		QuestionType:   o.QuestionType,
		Labels:         labels,
		Repairs:        o.Repairs,
		Damage:         o.Damage,
		FireRate:       o.FireRate,
		Specificity:    o.Specificity,
		OODMoved:       o.OODMoved,
		LatencyUs:      o.LatencyUs,
		License:        lic,
	}, nil
}

func (c *CapabilitySpec) column() string {
	if c == nil {
		return ""
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return string(raw)
}

func parseCapabilityColumn(raw string) (*CapabilitySpec, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var c CapabilitySpec
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, err
	}
	if c.Labels == nil {
		c.Labels = []string{}
	}
	return &c, nil
}

// Downloads an open-source Kai capability listing's .tar weights (base64) and
// shared representation metadata. Only public capability listings with price 0
// allow weight downloads; monetized capability listings keep their weights
// sealed and answer 403. It needs no credential.
//
// Example: {"id": "lst_4f1c"}
func (o marketOps) shopWeights(ctx context.Context, in *shopRef) (*ShopCapabilityWeights, error) {
	l, found, err := o.s.State.store.Public(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "shop: %v", err)
	}
	if !found || l.Kind != KindCapability {
		return nil, zip.ErrNotFound("capability listing not found")
	}
	if l.Price.Sign() > 0 {
		return nil, zip.ErrForbidden("monetized capability weights are sealed; install the capability to run it over x402")
	}
	out, err := trainpeer.TrainCapabilityWeights(ctx, &client.CapabilityWeightsIn{
		Ref:  l.Ref,
		Name: l.Tool,
	})
	if err != nil {
		return nil, peerErr("train", err)
	}
	return &ShopCapabilityWeights{
		Name:           out.Name,
		Version:        out.Version,
		SHA256:         out.SHA256,
		BaseSHA256:     out.BaseSHA256,
		Representation: out.Representation,
		Form:           out.Form,
		Bytes:          out.Bytes,
		Tar:            out.Tar,
	}, nil
}
