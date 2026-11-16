package identity

import (
	"crypto/ecdsa"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Agent represents a portable identity compatible with ERC-8004
type Agent struct {
	Address      common.Address `json:"address"`
	Owner        common.Address `json:"owner"`
	Capabilities []Capability   `json:"capabilities"`
	MetadataURI  string         `json:"metadata_uri"` // IPFS/Arweave link to agent manifest
	CreatedAt    time.Time      `json:"created_at"`
}

// Capability defines what an agent can do (MCP/A2A compatible)
type Capability struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"` // A2A or MCP endpoint
	Protocol    string `json:"protocol"` // "mcp" or "a2a"
	Description string `json:"description"`
}

// SignedCredential represents a verifiable claim about an agent
type SignedCredential struct {
	Issuer    common.Address `json:"issuer"`
	Subject   common.Address `json:"subject"`
	Claim     string         `json:"claim"`
	Signature []byte         `json:"signature"`
}

func NewAgent(ownerPrivKey *ecdsa.PrivateKey, metadataURI string) (*Agent, error) {
	agentPrivKey, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}
	
	agentAddr := crypto.PubkeyToAddress(agentPrivKey.PublicKey)
	ownerAddr := crypto.PubkeyToAddress(ownerPrivKey.PublicKey)

	return &Agent{
		Address:     agentAddr,
		Owner:       ownerAddr,
		MetadataURI: metadataURI,
		CreatedAt:   time.Now(),
	}, nil
}
