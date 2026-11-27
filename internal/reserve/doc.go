// Package reserve holds money a call has committed but not yet spent, so a
// prepaid gate weighs a balance against the deployment's in-flight holds, not its
// settled ledger alone. A hold lapses if its debit never arrives.
package reserve
