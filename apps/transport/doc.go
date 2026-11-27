// Package transport is one inbox for the chat apps you connect — Discord, Slack,
// Teams, Telegram.
//
// The /v1/transport routes carry a portable chat envelope, per-org access policy
// (pairing / allowlist / open), a durable inbox, and outbound send across every
// connected transport. Identity and token custody stay in apps/integrations:
// inbound events arrive on the plane (client.TransportIngest) and replies leave
// through that package's senders, so the dependency points one way —
// transport → integrations, never back.
package transport
