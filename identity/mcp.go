package identity

import (
	"encoding/json"
	"net/http"
)

// MCPDiscovery implements the Model Context Protocol discovery for agents
type MCPDiscovery struct {
	Agent *Agent
}

type MCPResponse struct {
	JSONRPC string `json:"jsonrpc"`
	Result  struct {
		Capabilities []Capability `json:"capabilities"`
		Endpoints    []string     `json:"endpoints"`
	} `json:"result"`
}

func (h *MCPDiscovery) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	res := MCPResponse{JSONRPC: "2.0"}
	res.Result.Capabilities = h.Agent.Capabilities
	res.Result.Endpoints = []string{r.Host + "/v1/agent/rpc"}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}
