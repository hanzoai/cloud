package bitcoin

import (
	"encoding/hex"
)

// A Destination is the boiled-back simplistic form of a Bitcoin Output,
// denoting only the basics - where you want the money to end up, and how much
// of it you want there.
type Destination struct {
	Value   int64
	Address string
}

// An Origin is the boiled-back simpistic form of a Bitcoin Input, denoting
// only the basics - where you want the money to come from.
type Origin struct {
	TxId        string
	OutputIndex int
}

// An OriginWithAmount is the boiled-back simpistic form of a Bitcoin Input, denoting
// where you want the money to come from and the amount.
type OriginWithAmount struct {
	Origin
	Amount int64
}

// An Input is a slightly more complete form of a Bitcoin Input, denoting
// everything the base algorithms need to do their job.
type Input struct {
	TxId        string
	OutputIndex int
	ScriptSig   []byte // This is not to be confused with the 'Script' of the Output. This is the key to the Script's lock.
}

// An Output is a slightly more complete from of a Bitcoin Output, denoting
// everything the base algorithms need to do their job.
type Output struct {
	Value  int64
	Script []byte
}

type Sender struct {
	PrivateKey string
	PublicKey  string
	Address    string
}

type GetRawTransactionResponseResult struct {
	Txid      string
	Hash      string
	Version   int
	Size      int
	Vsize     int
	Locktime  int
	Vin       []GetRawTransactionResponseResultInput
	Vout      []GetRawTransactionResponseResultOutput
	Hex       string
	Blockhash string
	Time      int64
	Blocktime int64
}

type GetRawTransactionResponseResultInput struct {
	Txid        string
	Vout        int
	Scriptsig   GetRawTransactionResponseResultInputSig
	Txinwitness []string
	Sequence    int64
}

type GetRawTransactionResponseResultInputSig struct {
	Asm string
	Hex string
}

type GetRawTransactionResponseResultOutput struct {
	N            int
	Value        float64
	Scriptpubkey GetRawTransactionResponseResultOutputScriptPubKey
}

type GetRawTransactionResponseResultOutputScriptPubKey struct {
	Asm        string
	Hex        string
	ReqSigs    int
	ScriptType string `json:"type"`
	Addresses  []string
}

type GetRawTransactionResponseError struct {
	Code    int
	Message string
}

func InputToOrigin(in Input) Origin {
	return Origin{
		TxId:        in.TxId,
		OutputIndex: in.OutputIndex,
	}
}

func OriginToInput(o Origin) Input {
	blank, _ := hex.DecodeString("")
	return Input{
		TxId:        o.TxId,
		OutputIndex: o.OutputIndex,
		ScriptSig:   blank,
	}
}

func DestinationToOutput(d Destination) Output {
	pubKey, err := CreateScriptPubKey(d.Address)

	if err != nil {
		panic(err)
	}

	return Output{
		Value:  d.Value,
		Script: pubKey,
	}
}

// There's no Output to Destination because half the point of using the
// pay-to-pubkey-hash scripts the way we do is that you can't tell what the
// public key is from looking at it.
