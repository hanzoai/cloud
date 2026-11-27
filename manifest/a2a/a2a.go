package a2a

const (
	// Path is the A2A server: POST /v1/agent/a2a, JSON-RPC 2.0, served by the
	// agents app because a task IS an agent run there — the same balance gate,
	// the same fee, the same run history.
	Path = "/v1/agent/a2a"

	// Card is where the agent card is published (A2A §8.2): readable before a
	// client holds a credential, on the API host and on every first-party site.
	Card = "/.well-known/agent-card.json"

	// Legacy is the card's address before A2A 0.3, still probed by scanners. It
	// answers the same card; it is an alias, never a second document.
	Legacy = "/.well-known/agent.json"

	// Protocol is the A2A version the server speaks, Major.Minor as the
	// A2A-Version header and AgentInterface.protocolVersion carry it (§3.6).
	Protocol = "1.0"

	// Binding is the one protocol binding the server offers (§9).
	Binding = "JSONRPC"

	// Agent is the agent the server answers as: the built-in default assistant,
	// the same one a chat bridge's @hanzo reaches.
	Agent = "hanzo"
)
