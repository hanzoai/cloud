package flags

import (
	"testing"
)

func TestRouter(t *testing.T) {
	router := Router{
		Models: []Model{
			{ID: "cheap-model", CostPer1k: 0.01, Quality: 0.7, Capability: ModelCapability{ZeroRetention: true}},
			{ID: "expensive-model", CostPer1k: 0.10, Quality: 0.9, Capability: ModelCapability{ZeroRetention: true}},
		},
	}

	id, err := router.Route(0.8, ModelCapability{ZeroRetention: true})
	if err != nil {
		t.Fatalf("Expected model, got error: %v", err)
	}

	if id != "expensive-model" {
		t.Errorf("Expected expensive-model, got %s", id)
	}
}
