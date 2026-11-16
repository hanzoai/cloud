package identity

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestAgentLifecycle(t *testing.T) {
	store := NewStore()

	// 1. Register Agent
	ownerKey, _ := crypto.GenerateKey()
	agent, err := NewAgent(ownerKey, "ipfs://QmAgentMetadata")
	if err != nil {
		t.Fatal(err)
	}
	agent.Capabilities = append(agent.Capabilities, Capability{
		Name: "web-search",
		Protocol: "mcp",
		Endpoint: "/mcp/search",
	})
	store.RegisterAgent(agent)
	t.Logf("Agent registered: %s", agent.Address.Hex())

	// 2. Delegate bounded task and spending limit
	delegation := &Delegation{
		ID:             "del_001",
		AgentAddress:   agent.Address,
		OwnerAddress:   agent.Owner,
		SpendingLimit:  big.NewInt(1000000), // 1M units
		SpentAmount:    big.NewInt(0),
		AllowedMethods: []string{"web-search"},
		ExpiresAt:      time.Now().Add(1 * time.Hour),
	}
	store.SaveDelegation(delegation)
	t.Log("Delegation created with limit 1,000,000")

	// 3. Complete paid work (Enforce permissions)
	taskCost := big.NewInt(50000)
	err = delegation.Enforce("web-search", taskCost)
	if err != nil {
		t.Errorf("Should have allowed task: %v", err)
	}
	t.Logf("Task completed. Remaining limit: %s", new(big.Int).Sub(delegation.SpendingLimit, delegation.SpentAmount))

	// 4. Receive independently verifiable feedback (Reputation)
	feedback := SignedCredential{
		Issuer:  agent.Owner,
		Subject: agent.Address,
		Claim:   "Task completed successfully with high accuracy",
	}
	// In a real scenario, we would sign this with ownerKey
	t.Logf("Feedback received for agent %s: %s", feedback.Subject.Hex(), feedback.Claim)

	// 5. Revoke a compromised delegation
	delegation.Revoked = true
	err = delegation.Enforce("web-search", big.NewInt(1))
	if err == nil || err != ErrRevoked {
		t.Errorf("Expected ErrRevoked, got %v", err)
	}
	t.Log("Delegation successfully revoked.")
}
