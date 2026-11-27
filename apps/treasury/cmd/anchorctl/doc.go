// Command anchorctl bootstraps the Hanzo L1 (chain 36963) treasury anchor: it
// provisions the KMS-held signer key, funds it, and deploys contracts/TreasuryAnchor.sol
// — the on-chain witness that apps/treasury/anchor_evm.go later writes ledger roots to.
//
// It is a one-shot operator tool, run IN-CLUSTER (the 36963 RPC — hanzod-rpc-internal
// in ns hanzo-mainnet — is not reachable from outside). The canonical invocation is the
// `bootstrap` subcommand, which is idempotent: re-running with the same SIGNER_KEY (or a
// key already in KMS) re-uses it, only funds when the balance is short, and only deploys
// when no contract is given.
//
// It shares anchor_evm.go's fee strategy exactly (dynamicFees below is a byte-for-byte
// copy): a DynamicFeeTx with a 1 gwei tip floor and 2x-base-fee headroom, which is how
// the coreth 25 gwei min-base-fee "gas quirk" on 36963 is handled — a legacy tx priced
// at base+1 strands the moment the base fee moves.
//
// Config (env):
//
//	RPC_URL             36963 JSON-RPC (http://hanzod-rpc-internal.hanzo-mainnet.svc.cluster.local:9630)
//	SIGNER_KEY          optional hex priv for the anchor signer; generated if empty
//	ANVIL_KEY           funder hex priv (a funded 36963 genesis account)
//	FUND_ETH            ether to top the signer up to (default 10)
//	ANCHOR_BYTECODE     0x… creation bytecode (or ANCHOR_BYTECODE_FILE path)
//	CONTRACT            skip deploy, just report/verify this address
//	KMS_ADDR IAM_ADDR KMS_ORG KMS_CLIENT_ID KMS_CLIENT_SECRET   KMS write creds
//	KMS_PATH KMS_KEY    where the signer key is stored (default treasury-anchor / TREASURY_ANCHOR_SIGNER_KEY)
//
// Secrets rule: the signer private key is written to KMS and printed NOWHERE — stdout
// carries only the address, the KMS ref, and tx hashes.
package main
