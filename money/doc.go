// Package money is exact money: a USD balance carried to 18 decimals, so no amount
// is ever rounded away.
//
// It is the ONE money value for the Hanzo cloud finance stack. The 18 decimals are
// EVM/ERC-20 precision, so an off-chain ledger amount and an on-chain uint256 credit
// balance are THE SAME INTEGER — no conversion or rounding at the boundary. Every
// per-token AI price is represented and billed EXACTLY; there is no cent-flooring and
// no fractional-cent skim, at any scale.
//
// The exact-number machinery (big.Int fixed-point, no float, no precision ceiling) is NOT
// reimplemented here — it lives ONCE in github.com/hanzoai/money + github.com/hanzoai/decimal,
// the shared money value for the whole stack. This package is the thin policy layer that
// pins the cloud's credit unit to 18-decimal USD and nothing else; it is the single place
// that decision lives.
//
// An Amount is IMMUTABLE — every operation returns a new value — and the zero value is a
// valid 0.
package money
