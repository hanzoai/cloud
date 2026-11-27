// Package a2a is the fleet's A2A (Agent2Agent) server as its addresses and the
// protocol facts it answers with, and nothing else.
//
// It is a LEAF for the reason manifest/mcp is: the server (apps/agents/a2a.go)
// and the card that describes it (openapi/agent.go) both read these values, and
// neither may import the other. One constant, two readers, so the card can never
// name an endpoint, a binding or a protocol version the server does not answer.
package a2a
