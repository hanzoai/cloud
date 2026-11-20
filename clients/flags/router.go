package flags

import (
	"errors"
	"sort"
)

type ModelCapability struct {
	ZeroRetention bool
	Thinking      bool
	ToolChoice    bool
}

type Model struct {
	ID         string
	CostPer1k  float64
	Quality    float64
	Capability ModelCapability
}

type Router struct {
	Models []Model
}

func (r *Router) Route(minQuality float64, cap ModelCapability) (string, error) {
	var candidates []Model

	for _, m := range r.Models {
		if m.Quality >= minQuality &&
			(!cap.ZeroRetention || m.Capability.ZeroRetention) &&
			(!cap.ToolChoice || m.Capability.ToolChoice) {
			candidates = append(candidates, m)
		}
	}

	if len(candidates) == 0 {
		return "", errors.New("no model meets quality and capability constraints")
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CostPer1k < candidates[j].CostPer1k
	})

	return candidates[0].ID, nil
}
