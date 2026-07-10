package bitcoin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	mathRand "math/rand"
	"strconv"
	"time"

	"golang.org/x/crypto/ripemd160"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	luxcrypto "github.com/luxfi/crypto"

	//"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/base58"
	ecdsa2 "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains/blocktransaction"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

// The steps notated in the variable names here relate to the steps outlined in
// https://en.bitcoin.it/wiki/Technical_background_of_version_1_Bitcoin_addresses
var SatoshiPerByte = int64(200)

// Errors
var WeRequireAdditionalFunds = errors.New("Wallet address contains insufficient funds")

func PubKeyToAddress(pubKey string, testNet bool) (string, []byte, error) {
	ripe := ripemd160.New()
	step2decode, err := hex.DecodeString(pubKey)
	if err != nil {
		return "", nil, err
	}
	step2 := sha256.Sum256(step2decode)

	log.Debug("PubKeyToAddress: public key: %v", pubKey)
	log.Debug("PubKeyToAddress: public key hex decode: %v", step2decode)
	log.Debug("PubKeyToAddress: Step 2 hex: %v", hex.EncodeToString(step2[:]))
	if len(step2) != 32 {
		return "", nil, fmt.Errorf("PubKeyToAddress: Step 2: Invalid length. %v", len(step2))
	}

	log.Debug("PubKeyToAddress: Step 2: %v", step2)
	ripe.Write(step2[:])
	step3 := ripe.Sum(nil)

	log.Debug("PubKeyToAddress: Step 3 hex: %v", hex.EncodeToString(step3))
	if len(step3) != 20 {
		return "", nil, fmt.Errorf("PubKeyToAddress: Step 3: Invalid length. %v", len(step3))
	}

	prefix := []byte{byte(0)}
	if testNet {
		log.Debug("PubKeyToAddress: Appending Testnet prefix.")
		prefix, _ = hex.DecodeString("6F")
	}
	step4 := append(prefix, step3...)

	log.Debug("PubKeyToAddress: Step 4 hex: %v", hex.EncodeToString(step4))
	if len(step4) != 21 {
		return "", nil, fmt.Errorf("PubKeyToAddress: Step 4: Invalid length. %v", len(step4))
	}

	step5 := sha256.Sum256(step4)

	log.Debug("PubKeyToAddress: Step 5 hex: %v", hex.EncodeToString(step5[:]))
	if len(step5) != 32 {
		return "", nil, fmt.Errorf("PubKeyToAddress: Step 5: Invalid length. %v", len(step5))
	}

	step6 := sha256.Sum256(step5[:])

	log.Debug("PubKeyToAddress: Step 6 hex: %v", hex.EncodeToString(step6[:]))
	if len(step6) != 32 {
		return "", nil, fmt.Errorf("PubKeyToAddress: Step 6: Invalid length. %v", len(step6))
	}
	step7 := step6[0:4]

	log.Debug("PubKeyToAddress: Step 7 hex: %v", hex.EncodeToString(step7[:]))
	if len(step7) != 4 {
		return "", nil, fmt.Errorf("PubKeyToAddress: Step 7: Invalid length. %v", len(step7))
	}
	step8 := append(step4, step7...)

	log.Debug("PubKeyToAddress: Step 8 hex: %v", hex.EncodeToString(step8[:]))
	log.Debug("PubKeyToAddress: Step 8 Base58 encode: %v", base58.Encode(step8))
	if len(step8) != 25 {
		return "", nil, fmt.Errorf("PubKeyToAddress: Step 8: Invalid length. %v", len(step8))
	}

	return base58.Encode(step8), step8, nil
}

func GenerateKeyPair() (string, string, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return "", "", err
	}
	privBytes := priv.Serialize()
	pubBytes := priv.PubKey().SerializeCompressed()

	// If you want to use the uncompressed format and drop the first byte (0x04)
	// pubBytes = priv.PubKey().SerializeUncompressed()[1:]

	return hex.EncodeToString(privBytes), hex.EncodeToString(pubBytes), nil

	// Remove the extra pubkey byte before serializing hex (drop the first 0x04)
	//return hex.EncodeToString(luxcrypto.FromECDSA(priv)), hex.EncodeToString(luxcrypto.FromECDSAPub(&priv.PublicKey)), nil
}

func GetRawTransactionSignature(rawTransaction []byte, pk string) ([]byte, error) {
	//Here we start the process of signing the raw transaction.

	log.Debug("GetRawTransactionSignature: Private key prior to decode: %v", pk)
	pkBytes, err := hex.DecodeString(pk)
	if err != nil {
		log.Error("GetRawTransactionSignature: Could not hex decode '%s': %v", pk, err)
		return nil, err
	}

	privateKey, err := luxcrypto.ToECDSA(pkBytes)
	if err != nil {
		log.Error("GetRawTransactionSignature: Could not crypto decode '%s': %v", pkBytes, err)
		return nil, err
	}

	log.Debug("GetRawTransactionSignature: Private key decoding successful.")
	publicKey := privateKey.PublicKey
	log.Debug("GetRawTransactionSignature: Public key derived: %v", publicKey)
	publicKeyBytes := luxcrypto.FromECDSAPub(&publicKey)
	log.Debug("GetRawTransactionSignature: Public key bytes: %s", publicKeyBytes)

	//Hash the raw transaction twice before the signing
	shaHash := sha256.New()
	shaHash.Write(rawTransaction)
	var hash []byte = shaHash.Sum(nil)

	shaHash2 := sha256.New()
	shaHash2.Write(hash)
	rawTransactionHashed := shaHash2.Sum(nil)

	// rawTransactionHashedReversed := make([]byte, len(rawTransactionHashed))
	// for i := 0; i < len(rawTransactionHashed); i++ {
	// 	rawTransactionHashedReversed[i] = rawTransactionHashed[len(rawTransactionHashed)-i-1]
	// }

	//Sign the raw transaction
	privateKeyBytes := privateKey.D.Bytes()
	privateKeySecp256k1 := secp256k1.PrivKeyFromBytes(privateKeyBytes)

	sig := ecdsa.Sign(privateKeySecp256k1, rawTransactionHashed)
	if err != nil {
		log.Error("GetRawTransactionSignature: Failed to sign transaction: %v", err)
		return nil, err
	}

	signedTransaction := sig.Serialize()

	parsedSig, err := ecdsa2.ParseDERSignature(signedTransaction)
	if err != nil {
		log.Error("GetRawTransactionSignature: Failed to parse signed transaction: %v", err)
		return nil, err
	}
	log.Debug("GetRawTransactionSignature: Parsed Signature: %v", parsedSig)

	// verified := sig.Verify(signedTransaction, (*btcec.PublicKey)(&publicKey))
	// if !verified {
	// 	log.Fatal("GetRawTransactionSignature: Failed to verify signed transaction.")
	// }

	hashCodeType, err := hex.DecodeString("01")
	if err != nil {
		log.Fatal(err)
	}

	//+1 for hashCodeType
	signedTransactionLength := byte(len(signedTransaction) + 1)

	pubKeyLength := byte(len(publicKeyBytes))

	log.Debug("# Writing ScriptSig")
	var buffer bytes.Buffer
	log.Debug("# %v", signedTransactionLength)
	buffer.WriteByte(signedTransactionLength)
	log.Debug("# %v", signedTransaction)
	buffer.Write(signedTransaction)
	log.Debug("# %v", hashCodeType[0])
	buffer.WriteByte(hashCodeType[0])
	log.Debug("# %v", pubKeyLength)
	buffer.WriteByte(pubKeyLength)
	log.Debug("# %v", publicKeyBytes)
	buffer.Write(publicKeyBytes)

	scriptSig := buffer.Bytes()

	//Return the final transaction signature
	return scriptSig, nil
}
func CreateScriptPubKey(publicKeyBase58 string) ([]byte, error) {
	address, err := btcutil.DecodeAddress(publicKeyBase58, &chaincfg.MainNetParams)
	if err != nil {
		log.Error("DecodeAddress '%s' Error: %v", publicKeyBase58, err)
		return nil, err
	}

	// Create a public key script that pays to the address.
	script, err := txscript.PayToAddrScript(address)
	if err != nil {
		log.Error("PayToAddrScript '%s' Error: %v", address, err)
		return nil, err
	}
	log.Debug("CreateScriptPubKey: Script Hex: %x\n", script)
	return script, nil
}

func generateNonce() [32]byte {
	var bytes [32]byte
	for i := 0; i < 32; i++ {
		//This is not "cryptographically random"
		bytes[i] = byte(randInt(0, math.MaxUint8))
	}
	return bytes
}

func randInt(min int, max int) uint8 {
	mathRand.Seed(time.Now().UTC().UnixNano())
	return uint8(min + mathRand.Intn(max-min))
}

/* NOTE: This function presumes you're doing a pay to public key hash
* transaction and using a single script to authenticate the entire thing. More
* complex stuff will come later. */
func CreateRawTransaction(inputs []Input, outputs []Output) ([]byte, error) {
	//Create the raw transaction.

	//Version field
	version, err := hex.DecodeString("01000000")
	if err != nil {
		log.Fatal(err)
	}

	in := ""
	if len(inputs) < 15 {
		in = "0" + fmt.Sprintf("%x", len(inputs))
	} else {
		in = fmt.Sprintf("%x", len(inputs))
	}
	inCount, err := hex.DecodeString(in)
	if err != nil {
		log.Error("CreateRawTransaction: String representation of length: %v", strconv.Itoa(len(inputs)))
		log.Error("CreateRawTransaction: Could not decode hash %s, %v", in, err)
		return nil, err
	}

	//Input transaction hash

	inputTransactionLittleEndian := make([][]byte, len(inputs))
	outputIndeces := make([][]byte, len(inputs))
	for index, input := range inputs {
		inputBytes, err := hex.DecodeString(input.TxId)
		if err != nil {
			log.Error(err)
			return nil, err
		}

		//Convert input transaction hash to little-endian form
		inputBytesReversed := make([]byte, len(inputBytes))
		for i := 0; i < len(inputBytes); i++ {
			inputBytesReversed[i] = inputBytes[len(inputBytes)-i-1]
		}
		inputTransactionLittleEndian[index] = inputBytesReversed

		outputIndexBytes := make([]byte, 4)
		binary.LittleEndian.PutUint32(outputIndexBytes, uint32(input.OutputIndex))
		outputIndeces[index] = outputIndexBytes
	}

	//sequence_no. Normally 0xFFFFFFFF. Always in this case.
	sequence, err := hex.DecodeString("ffffffff")
	if err != nil {
		return nil, err
	}

	out := ""
	if len(outputs) < 15 {
		out = "0" + fmt.Sprintf("%x", len(outputs))
	} else {
		out = fmt.Sprintf("%x", len(outputs))
	}

	numOutputs, err := hex.DecodeString(out)
	if err != nil {
		return nil, err
	}
	//Satoshis to send.

	satoshisToOutputBytes := make([][]byte, len(outputs))
	scripts := make([][]byte, len(outputs))
	for index, output := range outputs {
		satoshiBytes := make([]byte, 8)
		binary.LittleEndian.PutUint64(satoshiBytes, uint64(output.Value))
		satoshisToOutputBytes[index] = satoshiBytes

		scriptPubKey := output.Script
		scripts[index] = scriptPubKey
	}

	//Lock time field
	lockTimeField, err := hex.DecodeString("00000000")
	if err != nil {
		log.Fatal(err)
	}

	log.Debug("# Writing Transaction")
	var buffer bytes.Buffer
	log.Debug("Version Number: # %v", version)
	buffer.Write(version)
	log.Debug("Number of inputs: # %v", inCount)
	buffer.Write(inCount)
	for index, bytes := range inputTransactionLittleEndian {
		log.Debug("bytes: # %v", bytes)
		buffer.Write(bytes)
		log.Debug("outputIndeces of index: # %v", outputIndeces[index])
		log.Debug("outputIndeces of index (hex): # %v", hex.EncodeToString(outputIndeces[index]))
		buffer.Write(outputIndeces[index])
		log.Debug("Script Sig Length: # %v", len(inputs[index].ScriptSig))
		buffer.WriteByte(byte(len(inputs[index].ScriptSig)))
		log.Debug("Script Sig:# %v", inputs[index].ScriptSig)
		buffer.Write(inputs[index].ScriptSig)
		log.Debug("Sequence Number: # %v", sequence)
		buffer.Write(sequence)
	}
	log.Debug("Number of outputs: # %v", numOutputs)
	buffer.Write(numOutputs)
	for index, script := range scripts {
		log.Debug("Satoshis for output: # %v", satoshisToOutputBytes[index])
		buffer.Write(satoshisToOutputBytes[index])
		log.Debug("Length of script: # %v", byte(len(script)))
		buffer.WriteByte(byte(len(script)))
		log.Debug("Output script: # %v", script)
		buffer.Write(script)
	}
	log.Debug("# %v", lockTimeField)
	buffer.Write(lockTimeField)

	return buffer.Bytes(), nil
}

func CreateTransaction(client BitcoinClient, origins []Origin, destinations []Destination, sender Sender, feePerByte int64) ([]byte, error) {
	if feePerByte == 0 {
		feePerByte = SatoshiPerByte
	}

	// There will be a need to keep track of change.
	totalChange := int64(0)

	// We need to get our Destinations changed to proper outputs.
	outputs := make([]Output, len(destinations))
	for index, destination := range destinations {
		outputs[index] = DestinationToOutput(destination)
	}

	// Then we need proper inputs. This process is a little more involved.

	// To get the appropriate Signature to unlock the Script of each
	// invoked transaction output, every OTHER input must have a blank Script.
	// To put it another way, to get the signature for Input 2 of 4, inputs 1,
	// 3, and 4 must have a blank Script, and Input 2 must have the Script of
	// the transaction Output it's hoping to redeem.

	// This is the final slice that we're going to eventually send into the
	// final transaction.
	inputs := make([]Input, len(origins))

	// And this is the temporary slice that we're going to be using to satisfy
	// the blanking requirements.
	buildableInputs := make([]Input, len(origins))
	for index, origin := range origins {
		buildableInputs[index] = OriginToInput(origin)
		inputs[index] = OriginToInput(origin)
	}

	for index, origin := range origins {
		trxFromNode, err := client.GetRawTransaction(origin.TxId)
		if err != nil {
			return nil, err
		}
		content := &GetRawTransactionResponseResult{}
		json.DecodeBytes(trxFromNode.Result, content)
		if origin.OutputIndex >= len(content.Vout) {
			return nil, fmt.Errorf("CreateTransaction: Wanted output index %v of input transaction %v - only %v outputs available", origin.OutputIndex, origin.TxId, len(content.Vout))
		}
		// Keep track of how much value we're playing with.
		totalChange += int64(content.Vout[origin.OutputIndex].Value * 100000000) // convert to Satoshi

		// Grab the Script of the Output we're hoing to redeem.
		script, _ := hex.DecodeString(content.Vout[origin.OutputIndex].Scriptpubkey.Hex)
		inputs[index].ScriptSig = script // Temporary holding.
	}

	// Subtract the amount we're giving out
	for _, output := range outputs {
		totalChange -= output.Value
	}

	approximateFee := int64(CalculateFee(len(inputs), len(outputs), feePerByte))
	// Check to see if it's worth taking change - algo here is "is there more
	// change than twice what it costs to add another output"
	if totalChange > (approximateFee + (2 * 34 * feePerByte)) {
		// If we're in here, it's worth taking change and we should add the
		// sender onto the outputs.
		approximateFee += int64(34 * feePerByte) // Update the fee to account for the extra length.
		totalChange -= approximateFee            // pull down the change to account for the fee.

		// Add the change to our outputs
		outScript, err := CreateScriptPubKey(sender.Address)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, Output{totalChange, outScript})

	}

	for index, input := range inputs {
		buildableInputs[index].ScriptSig = input.ScriptSig                    // Load in temporary script signature
		rawTransaction, err := CreateRawTransaction(buildableInputs, outputs) // Create initial raw transaction
		if err != nil {
			return nil, err
		}

		// Add the hash code required to compute the signature.
		log.Debug("CreateTransaction: initial raw transaction created: %v", hex.EncodeToString(rawTransaction))
		hashCodeType, err := hex.DecodeString("01000000")
		log.Debug("CreateTransaction: Hash code type created.")
		var rawTransactionBuffer bytes.Buffer
		rawTransactionBuffer.Write(rawTransaction)
		rawTransactionBuffer.Write(hashCodeType)
		rawTransactionWithHashCodeType := rawTransactionBuffer.Bytes()
		log.Debug("CreateTransaction: Raw transaction appended with hash code. %v", len(rawTransactionWithHashCodeType))

		// Compute the signature.
		finalSignature, err := GetRawTransactionSignature(rawTransactionWithHashCodeType, sender.PrivateKey)
		if err != nil {
			return nil, err
		}
		// Save the final signature to our input slice.
		inputs[index].ScriptSig = finalSignature
		log.Debug("CreateTransaction: Saved signature to input index %v: %v", index, finalSignature)

		// Blank out the script signature we just used so we can keep computing
		// the other final signatures.
		blankScript, _ := hex.DecodeString("")
		buildableInputs[index].ScriptSig = blankScript // This needs to get blanked out so the others can be computed correctly.
	}

	rawTrx, err := CreateRawTransaction(inputs, outputs)
	if err != nil {
		return nil, err
	}
	log.Info("CreateTransaction: Final trx: %v", hex.EncodeToString(rawTrx))
	return rawTrx, nil
}

func CalculateFee(inputs, outputs int, feePerByte int64) int64 {
	if feePerByte == 0 {
		feePerByte = SatoshiPerByte
	}
	// Now compute the probable fee and be pessimistic about the size of the
	// transaction
	// 180 is the length (in bytes) of each input.
	// 34 is the length (in bytes) of each output.
	// 10 is the standard length (in bytes) of basic stuff in the protocol.
	// The final +len(inputs) is padding. Certain inputs are 11, others are 9.
	// We're being pessimistic and adding always.
	approximateTransactionLength := (inputs * 180) + (outputs * 34) + 10 + inputs
	return int64(approximateTransactionLength) * feePerByte
}

func GetBitcoinTransactions(ctx context.Context, address string) ([]OriginWithAmount, error) {
	db := datastore.New(ctx)
	db.SetNamespace(blockchains.BlockchainNamespace)

	bts := make([]*blocktransaction.BlockTransaction, 0)

	if _, err := blocktransaction.Query(db).Filter("BitcoinTransactionType=", blockchains.BitcoinTransactionTypeVOut).Filter("BitcoinTransactionUsed=", false).Filter("Address=", address).GetAll(&bts); err != nil {
		return nil, err
	}

	oris := make([]OriginWithAmount, len(bts))

	for i, bt := range bts {
		oris[i] = OriginWithAmount{
			Origin: Origin{
				TxId:        bt.BitcoinTransactionTxId,
				OutputIndex: int(bt.BitcoinTransactionVOutIndex),
			},
			Amount: bt.BitcoinTransactionVOutValue,
		}
	}

	return oris, nil
}

func PruneOriginsWithAmount(oris []OriginWithAmount, amount int64) ([]OriginWithAmount, error) {
	// TODO sort in 1.8
	// sort.Slice(oris[:], func(i, j int) bool {
	// 	return oris[i].Amount < oris[j].Amount
	// })

	if IsTest {
		return oris, nil
	}

	origins := make([]OriginWithAmount, 0)

	var a int64 = 0

	for _, ori := range oris {
		if a < amount {
			a += int64(ori.Amount)
			origins = append(origins, ori)
		} else {
			break
		}
	}

	if a < amount {
		return origins, WeRequireAdditionalFunds
	}

	return origins, nil
}

func OriginsWithAmountToOrigins(oris []OriginWithAmount) []Origin {
	origins := make([]Origin, 0)

	for _, ori := range oris {
		origins = append(origins, ori.Origin)
	}

	return origins
}

/*func CreateTransactionBtcd(client BitcoinClient, inputs []Input, output []Output, sender Sender) {

	totalChange := int64(0)
	transaction := wire.NewMsgTx(1)
	for index, input := range inputs {
		trxFromNode, err := client.GetRawTransaction(input.TxId)
		if err != nil {
			return nil, err
		}
		content := &GetRawTransactionResponseResult{}
		json.DecodeBytes(trxFromNode.Result, content)
		if input.OutputIndex >= len(content.Vout) {
			return nil, fmt.Errorf("CreateTransaction: Wanted output index %v of input transaction %v - only %v outputs available", input.OutputIndex, input.TxId, len(content.Vout))
		}
		totalChange += int64(content.Vout[input.OutputIndex].Value * 100000000) // convert to Satoshi
		inputScript, err = content.Vout[input.OutputIndex].Scriptpubkey.Hex
		if err != nil {
			return nil, err
		}
		outpointHash, err := chainhash.NewHashFromStr(content.Hash)
		if err != nil {
			return nil, err
		}
		transaction.AddTxIn(
			wire.NewTxIn(
				wire.NewOutPoint(outpointHash, input.OutputIndex),
				inputScript,
				nil
			)
		)
		log.Debug("CreateTransaction: Created TxIn")
	}

	for _, output := range outputs {
		totalChange -= output.Value
		transaction.AddTxOut(
			wire.NewTxOut(
				output.Value,
				CreatePubScriptKey(output.Address))
		)
		log.Debug("CreateTransaction: Created TxOut")
	}

	approximateFee := int64(CalculateFee(len(inputs), len(outputs)))
	// Check to see if it's worth taking change - algo here is "is there more
	// change than twice what it costs to add another output"
	if totalChange > (approximateFee + (2 * 34 * int64(SatoshiPerByte))) {
		// If we're in here, it's worth taking change and we should add the
		// sender onto the outputs.
		approximateFee += int64(34 * SatoshiPerByte) // Update the fee to account for the extra length.
		totalChange -= approximateFee                // pull down the change to account for the fee.

		// Add the change to our outputs, asking our Bitcoin Client if we're in
		// test mode or not.
		if client.IsTest {
			transaction.AddTxOut(wire.newTxOut(totalChange, CreateScriptPubKey(sender.TestNetAddress))
		} else {
			transaction.AddTxOut(wire.newTxOut(totalChange, CreateScriptPubKey(sender.Address))
		}

	}
}*/
