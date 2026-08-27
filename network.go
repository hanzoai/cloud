package cloud

import "github.com/hanzoai/cloud/internal/environ"

// WHICH WORLD THIS DEPLOYMENT IS — its chain and its money, stated once.
//
// The estate runs one deployment per network, each with its own chain node, its
// own identity issuer and its own books (hanzo-testnet, hanzo-devnet). What was
// missing is the deployment SAYING so, so every part of it could agree.
//
// The money plane already separates sandbox from live: a ledger address is
// (org, subject, currency, test) and the two are physically different files. But
// `test` arrives from the CALLER, which is right on mainnet — the org's posture
// decides — and wrong everywhere else. On a test network the caller could ask for
// live books and get them, so "no real cash here" was a thing nobody had asked
// for rather than a thing that could not happen.
//
// Network is the one declaration that closes it. Off mainnet, live money is
// UNREPRESENTABLE rather than defaulted off: the books are forced sandbox where
// the address is formed, and the posture switch that would flip an org back
// refuses. Nothing else in the deployment has to remember.
type Network string

const (
	// Mainnet is real money on the real chain. It is the zero value on purpose:
	// a deployment that declares nothing is production, so no existing
	// environment changes behaviour by omission.
	Mainnet Network = "mainnet"
	// Testnet is the public test network — its own chain, its own coin, its own
	// books, and no rail to a card.
	Testnet Network = "testnet"
	// Devnet is the development network, same terms as Testnet.
	Devnet Network = "devnet"
)

// NetworkEnv names the one variable that decides it.
//
// One underscore from CLOUD_NETWORK_ADDR, which is unrelated: the app once called
// `zt` is called `network` now, and the plugin resolver reserves CLOUD_<APP>_ADDR
// and CLOUD_<APP>_BIN for reaching it. Go matches an environment name exactly so
// nothing can read one for the other, but an operator scanning a single env block
// can, which is why the two are pinned apart in network_test.go.
const NetworkEnv = "CLOUD_NETWORK"

// NetworkOf answers which network this deployment is.
//
// An unset or unrecognised value is Mainnet, and that direction is deliberate:
// the failure a typo must not cause is a production deployment quietly deciding
// its money is fake. Getting it wrong the other way is loud — a test network
// that takes a real card is discovered by a customer.
func NetworkOf() Network {
	switch Network(environ.Or(NetworkEnv, string(Mainnet))) {
	case Testnet:
		return Testnet
	case Devnet:
		return Devnet
	default:
		return Mainnet
	}
}

// SandboxOnly reports whether live money exists in this deployment at all.
//
// It is the predicate the money plane reads. True means every ledger address is
// the sandbox one whatever the caller asked for, and the posture that would
// change that cannot be set.
func SandboxOnly() bool { return NetworkOf() != Mainnet }
