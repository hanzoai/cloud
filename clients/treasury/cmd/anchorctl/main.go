// Command anchorctl bootstraps the Hanzo L1 (chain 36963) treasury anchor: it
// provisions the KMS-held signer key, funds it, and deploys contracts/TreasuryAnchor.sol
// — the on-chain witness that clients/treasury/anchor_evm.go later writes ledger roots to.
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

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	ethereum "github.com/luxfi/geth"
	"github.com/luxfi/crypto"
	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/ethclient"
)

func main() {
	ctx := context.Background()
	var err error
	switch cmd() {
	case "bootstrap":
		err = bootstrap(ctx)
	case "anchor":
		err = anchorRoot(ctx)
	default:
		fmt.Fprintln(os.Stderr, "usage: anchorctl {bootstrap|anchor}   (config via env; see package doc)")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "anchorctl: %v\n", err)
		os.Exit(1)
	}
}

func cmd() string {
	if len(os.Args) < 2 {
		return ""
	}
	return os.Args[1]
}

// anchorRoot sends TreasuryAnchor.anchor(bytes32) from the signer (the contract
// owner) with ANCHOR_ROOT, then reads count()+latestRoot() back to confirm the
// commit. This is the exact on-chain path clients/treasury/anchor_evm.go uses when
// TREASURY_ANCHOR_CONTRACT is set (same selector, same DynamicFeeTx fee strategy).
func anchorRoot(ctx context.Context) error {
	rpc := env("RPC_URL", "")
	sk := strings.TrimSpace(env("SIGNER_KEY", ""))
	contractHex := strings.TrimSpace(env("CONTRACT", ""))
	rootHex := strings.TrimSpace(env("ANCHOR_ROOT", ""))
	if rpc == "" || sk == "" || contractHex == "" || rootHex == "" {
		return fmt.Errorf("anchor requires RPC_URL, SIGNER_KEY, CONTRACT, ANCHOR_ROOT")
	}
	rootBytes, err := hex.DecodeString(trim0x(rootHex))
	if err != nil || len(rootBytes) != 32 {
		return fmt.Errorf("ANCHOR_ROOT must be 32-byte hex, got %q", rootHex)
	}
	priv, err := crypto.HexToECDSA(trim0x(sk))
	if err != nil {
		return fmt.Errorf("parse SIGNER_KEY: %w", err)
	}
	signer := common.Address(crypto.PubkeyToAddress(priv.PublicKey))
	contract := common.HexToAddress(contractHex)

	cl, err := ethclient.DialContext(ctx, rpc)
	if err != nil {
		return fmt.Errorf("dial %s: %w", rpc, err)
	}
	defer cl.Close()
	chainID, err := cl.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("chainID: %w", err)
	}
	// anchor(bytes32) selector — keccak256("anchor(bytes32)")[:4].
	sel := crypto.Keccak256([]byte("anchor(bytes32)"))[:4]
	data := append(append([]byte{}, sel...), rootBytes...)
	txh, rcpt, err := sendTx(ctx, cl, chainID, priv, &contract, big.NewInt(0), 200_000, data)
	if err != nil {
		return fmt.Errorf("anchor tx: %w", err)
	}
	if rcpt == nil {
		return fmt.Errorf("anchor tx %s: no receipt within timeout", txh.Hex())
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("anchor tx %s reverted (onlyOwner? signer %s)", txh.Hex(), signer.Hex())
	}
	// Read back count() (0x06661abd) and latestRoot() (0xd7b0fef1) to confirm.
	countRaw, _ := cl.CallContract(ctx, ethereum.CallMsg{To: &contract, Data: crypto.Keccak256([]byte("count()"))[:4]}, nil)
	latestRaw, _ := cl.CallContract(ctx, ethereum.CallMsg{To: &contract, Data: crypto.Keccak256([]byte("latestRoot()"))[:4]}, nil)
	out := map[string]any{
		"chainId":      chainID.String(),
		"contract":     contract.Hex(),
		"signer":       signer.Hex(),
		"anchoredRoot": "0x" + hex.EncodeToString(rootBytes),
		"txHash":       txh.Hex(),
		"block":        rcpt.BlockNumber.Uint64(),
		"txType":       rcpt.Type,
		"count":        new(big.Int).SetBytes(countRaw).String(),
		"latestRoot":   "0x" + hex.EncodeToString(latestRaw),
		"rootMatches":  len(latestRaw) == 32 && string(latestRaw) == string(rootBytes),
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

type result struct {
	ChainID       string `json:"chainId"`
	SignerAddr    string `json:"signerAddr"`
	SignerKMSRef  string `json:"signerKmsRef,omitempty"`
	KMSStored     bool   `json:"kmsStored"`
	FundTx        string `json:"fundTx,omitempty"`
	SignerBalWei  string `json:"signerBalanceWei"`
	ContractAddr  string `json:"contractAddr,omitempty"`
	DeployTx      string `json:"deployTx,omitempty"`
	DeployBlock   uint64 `json:"deployBlock,omitempty"`
	OwnerVerified bool   `json:"ownerVerified"`
}

func bootstrap(ctx context.Context) error {
	rpc := env("RPC_URL", "")
	if rpc == "" {
		return fmt.Errorf("RPC_URL required")
	}
	cl, err := ethclient.DialContext(ctx, rpc)
	if err != nil {
		return fmt.Errorf("dial %s: %w", rpc, err)
	}
	defer cl.Close()
	chainID, err := cl.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("chainID: %w", err)
	}
	res := result{ChainID: chainID.String()}

	// 1) Signer key: reuse SIGNER_KEY or generate a fresh one.
	var priv *ecdsa.PrivateKey
	generated := false
	if sk := strings.TrimSpace(env("SIGNER_KEY", "")); sk != "" {
		if priv, err = crypto.HexToECDSA(trim0x(sk)); err != nil {
			return fmt.Errorf("parse SIGNER_KEY: %w", err)
		}
	} else {
		if priv, err = crypto.GenerateKey(); err != nil {
			return fmt.Errorf("generate signer key: %w", err)
		}
		generated = true
	}
	signer := common.Address(crypto.PubkeyToAddress(priv.PublicKey))
	res.SignerAddr = signer.Hex()

	// 2) Store the private key in KMS (never printed, never on disk).
	kmsPath := env("KMS_PATH", "treasury-anchor")
	kmsKey := env("KMS_KEY", "TREASURY_ANCHOR_SIGNER_KEY")
	res.SignerKMSRef = fmt.Sprintf("%s/%s/%s", env("KMS_ORG", "hanzo"), kmsPath, kmsKey)
	if env("KMS_CLIENT_ID", "") != "" {
		privHex := hex.EncodeToString(crypto.FromECDSA(priv))
		if err := kmsPut(ctx, kmsPath, kmsKey, privHex); err != nil {
			return fmt.Errorf("kms put %s: %w", res.SignerKMSRef, err)
		}
		res.KMSStored = true
	} else if generated {
		return fmt.Errorf("generated a new signer but KMS_CLIENT_ID unset — refusing to lose the key")
	}

	// 3) Fund the signer from a funded genesis account (only if short).
	fundEth := env("FUND_ETH", "10")
	want := new(big.Int).Mul(etherToWei(fundEth), big.NewInt(1))
	bal, err := cl.BalanceAt(ctx, signer, nil)
	if err != nil {
		return fmt.Errorf("balance: %w", err)
	}
	if bal.Cmp(want) < 0 {
		anvil := strings.TrimSpace(env("ANVIL_KEY", ""))
		if anvil == "" {
			return fmt.Errorf("signer underfunded (%s wei) and ANVIL_KEY unset — fund %s with >= %s wei", bal, signer.Hex(), want)
		}
		fpriv, err := crypto.HexToECDSA(trim0x(anvil))
		if err != nil {
			return fmt.Errorf("parse ANVIL_KEY: %w", err)
		}
		topUp := new(big.Int).Sub(want, bal)
		txh, _, err := sendTx(ctx, cl, chainID, fpriv, &signer, topUp, 21000, nil)
		if err != nil {
			return fmt.Errorf("fund tx: %w", err)
		}
		res.FundTx = txh.Hex()
		if bal, err = cl.BalanceAt(ctx, signer, nil); err != nil {
			return fmt.Errorf("balance after fund: %w", err)
		}
	}
	res.SignerBalWei = bal.String()

	// 4) Deploy TreasuryAnchor (constructor sets owner = signer) unless one was given.
	contract := strings.TrimSpace(env("CONTRACT", ""))
	if contract == "" {
		code, err := loadBytecode()
		if err != nil {
			return err
		}
		gas, err := cl.EstimateGas(ctx, ethereum.CallMsg{From: signer, To: nil, Data: code})
		if err != nil {
			gas = 1_000_000 // generous fallback; ~916-byte contract deploys well under this
		} else {
			gas = gas + gas/5 // +20% headroom
		}
		txh, rcpt, err := sendTx(ctx, cl, chainID, priv, nil, big.NewInt(0), gas, code)
		if err != nil {
			return fmt.Errorf("deploy tx: %w", err)
		}
		res.DeployTx = txh.Hex()
		if rcpt == nil {
			return fmt.Errorf("deploy tx %s: no receipt within timeout", txh.Hex())
		}
		if rcpt.Status != types.ReceiptStatusSuccessful {
			return fmt.Errorf("deploy tx %s reverted (status 0)", txh.Hex())
		}
		res.ContractAddr = rcpt.ContractAddress.Hex()
		res.DeployBlock = rcpt.BlockNumber.Uint64()
		contract = rcpt.ContractAddress.Hex()
	} else {
		res.ContractAddr = contract
	}

	// 5) Verify owner() == signer (proves the contract is anchor-ready for this signer).
	owner, err := callOwner(ctx, cl, common.HexToAddress(contract))
	if err != nil {
		return fmt.Errorf("verify owner(): %w", err)
	}
	res.OwnerVerified = owner == signer

	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	if !res.OwnerVerified {
		return fmt.Errorf("owner mismatch: contract owner %s != signer %s", owner.Hex(), signer.Hex())
	}
	return nil
}

// sendTx builds, signs and sends a DynamicFeeTx, then waits (bounded) for its receipt.
// to == nil is contract creation.
func sendTx(ctx context.Context, cl *ethclient.Client, chainID *big.Int, priv *ecdsa.PrivateKey, to *common.Address, value *big.Int, gas uint64, data []byte) (common.Hash, *types.Receipt, error) {
	from := common.Address(crypto.PubkeyToAddress(priv.PublicKey))
	nonce, err := cl.PendingNonceAt(ctx, from)
	if err != nil {
		return common.Hash{}, nil, fmt.Errorf("nonce: %w", err)
	}
	tipCap, feeCap, err := dynamicFees(ctx, cl)
	if err != nil {
		return common.Hash{}, nil, err
	}
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		To:        to,
		Value:     value,
		Gas:       gas,
		GasTipCap: tipCap,
		GasFeeCap: feeCap,
		Data:      data,
	})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), priv)
	if err != nil {
		return common.Hash{}, nil, fmt.Errorf("sign: %w", err)
	}
	if err := cl.SendTransaction(ctx, signed); err != nil {
		return signed.Hash(), nil, fmt.Errorf("send: %w", err)
	}
	for i := 0; i < 45; i++ {
		if r, rerr := cl.TransactionReceipt(ctx, signed.Hash()); rerr == nil && r != nil {
			return signed.Hash(), r, nil
		}
		select {
		case <-ctx.Done():
			return signed.Hash(), nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return signed.Hash(), nil, nil
}

// dynamicFees — identical to clients/treasury/anchor_evm.go's dynamicFees. See its doc.
func dynamicFees(ctx context.Context, cl *ethclient.Client) (tipCap, feeCap *big.Int, err error) {
	head, err := cl.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("latest header (base fee): %w", err)
	}
	if head.BaseFee == nil || head.BaseFee.Sign() == 0 {
		gp, gerr := cl.SuggestGasPrice(ctx)
		if gerr != nil {
			return nil, nil, fmt.Errorf("gas price: %w", gerr)
		}
		return gp, gp, nil
	}
	tipCap, err = cl.SuggestGasTipCap(ctx)
	if err != nil || tipCap == nil {
		tipCap = big.NewInt(0)
	}
	if minTip := big.NewInt(1_000_000_000); tipCap.Cmp(minTip) < 0 {
		tipCap = minTip
	}
	feeCap = new(big.Int).Add(new(big.Int).Mul(head.BaseFee, big.NewInt(2)), tipCap)
	return tipCap, feeCap, nil
}

// callOwner reads TreasuryAnchor.owner() (selector 0x8da5cb5b).
func callOwner(ctx context.Context, cl *ethclient.Client, contract common.Address) (common.Address, error) {
	sel := crypto.Keccak256([]byte("owner()"))[:4]
	out, err := cl.CallContract(ctx, ethereum.CallMsg{To: &contract, Data: sel}, nil)
	if err != nil {
		return common.Address{}, err
	}
	if len(out) < 32 {
		return common.Address{}, fmt.Errorf("owner() returned %d bytes", len(out))
	}
	return common.BytesToAddress(out[12:32]), nil
}

// kmsPut stores value at org/path/name over the KMS HTTP API (token via IAM
// client_credentials). Mirrors kms/sdk/go/kmsclient Put — no SDK dep pulled in.
func kmsPut(ctx context.Context, path, name, value string) error {
	kms := strings.TrimRight(env("KMS_ADDR", "http://kms.hanzo.svc"), "/")
	iam := strings.TrimRight(env("IAM_ADDR", "http://iam.hanzo.svc"), "/")
	org := env("KMS_ORG", "hanzo")

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {env("KMS_CLIENT_ID", "")},
		"client_secret": {env("KMS_CLIENT_SECRET", "")},
	}
	tReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, iam+"/v1/iam/oauth/token", strings.NewReader(form.Encode()))
	tReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tResp, err := http.DefaultClient.Do(tReq)
	if err != nil {
		return fmt.Errorf("iam token: %w", err)
	}
	defer tResp.Body.Close()
	tBody, _ := io.ReadAll(tResp.Body)
	if tResp.StatusCode != http.StatusOK {
		return fmt.Errorf("iam token status %d: %s", tResp.StatusCode, tBody)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(tBody, &tok); err != nil || tok.AccessToken == "" {
		return fmt.Errorf("iam token decode: %v", err)
	}
	// env=prod: the KMSSecret operator reads storage keyed by {path}/{env}/{name}
	// (kms/secrets layout). Omitting env lands the key at "default", where the
	// operator's prod-scoped sync can't see it.
	payload, _ := json.Marshal(map[string]string{"path": path, "name": name, "env": env("KMS_ENV", "prod"), "value": value})
	pReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, kms+"/v1/kms/orgs/"+url.PathEscape(org)+"/secrets", bytes.NewReader(payload))
	pReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	pReq.Header.Set("Content-Type", "application/json")
	pResp, err := http.DefaultClient.Do(pReq)
	if err != nil {
		return fmt.Errorf("kms put: %w", err)
	}
	defer pResp.Body.Close()
	if pResp.StatusCode != http.StatusCreated && pResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(pResp.Body)
		return fmt.Errorf("kms put status %d: %s", pResp.StatusCode, b)
	}
	return nil
}

func loadBytecode() ([]byte, error) {
	hexStr := strings.TrimSpace(env("ANCHOR_BYTECODE", ""))
	if hexStr == "" {
		if f := strings.TrimSpace(env("ANCHOR_BYTECODE_FILE", "")); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, fmt.Errorf("read ANCHOR_BYTECODE_FILE: %w", err)
			}
			hexStr = strings.TrimSpace(string(b))
		}
	}
	if hexStr == "" {
		return nil, fmt.Errorf("ANCHOR_BYTECODE or ANCHOR_BYTECODE_FILE required for deploy")
	}
	code, err := hex.DecodeString(trim0x(hexStr))
	if err != nil {
		return nil, fmt.Errorf("decode bytecode: %w", err)
	}
	return code, nil
}

func etherToWei(eth string) *big.Int {
	f, _, err := big.ParseFloat(strings.TrimSpace(eth), 10, 256, big.ToNearestEven)
	if err != nil {
		f = big.NewFloat(10)
	}
	wei, _ := new(big.Float).Mul(f, big.NewFloat(1e18)).Int(nil)
	return wei
}

func trim0x(s string) string {
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return s[2:]
	}
	return s
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
