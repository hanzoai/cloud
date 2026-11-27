// Package company is incorporation end to end: pick a structure, add founders, pay,
// file, and e-sign.
//
// It mounts /v1/company — Hanzo Company: incorporation and fundraising, end to end,
// fundraising product. It runs ONE formation state machine per
// org: choose a structure (C-Corp / LLC / DAO-LLC) → add founders + KYC → pay the
// one-time $999 fee → generate formation documents → e-sign them → record the cap
// table's equity genesis on-chain → upgrade the org to a "company". An
// already-incorporated org SKIPS straight to the import path (corporate docs →
// dataroom, cap-table spreadsheet → captable) and lands at the same "company"
// terminal.
//
// This file is the machine: the domain model (Formation, Founder, Genesis) and the
// PURE transition logic. It performs no I/O — every guard is a total function of a
// *Formation, so the whole lifecycle (legal transitions, the payment gate, the skip
// path) is unit-testable without a store, a clock, or a network. company.go layers
// the per-org SQLite store, the provider clients (KYC, billing, esign, dataroom,
// captable, on-chain anchor, state filing), and the HTTP surface on top.
package company
