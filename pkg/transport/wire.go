package transport

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"ledger-sats-stack/pkg/types"
	"ledger-sats-stack/pkg/utils"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/rpcclient"
	log "github.com/sirupsen/logrus"
)

// XRPC stands for eXtended RPC. It extends the btcd RPC client.
//
// TODO: Use a separate namespace for the Client, in order to separate
//       the btcd layer from this project.
//
// Not an endorsement for XRP Classic (XRP) / Ripple (XRP).
type XRPC struct {
	*rpcclient.Client
	Chain    string
	Pruned   bool
	TxIndex  bool
	Currency string // Based on Chain value, for interoperability with libcore
}

func (x XRPC) getBlockByHash(hash *chainhash.Hash) (*BlockContainer, error) {
	rawBlock, err := x.GetBlockVerbose(hash)
	if err != nil {
		return nil, err
	}

	block := new(BlockContainer)
	block.init(rawBlock)
	return block, nil
}

func (x XRPC) getBlockHashByReference(blockRef string) (*chainhash.Hash, error) {
	switch {
	case blockRef == "current":
		return x.GetBestBlockHash()

	case strings.HasPrefix(blockRef, "0x"), len(blockRef) == 64:
		// 256-bit hex string with or without 0x prefix
		return utils.ParseChainHash(blockRef)
	default:
		{
			// Either an int64 block height, or garbage input
			blockHeight, err := strconv.ParseInt(blockRef, 10, 64)

			switch err {
			case nil:
				return x.GetBlockHash(blockHeight)

			default:
				return nil, fmt.Errorf("Invalid block '%s'", blockRef)
			}
		}

	}
}

func (x XRPC) buildUTXOs(vin []btcjson.Vin) (types.UTXOs, error) {
	utxos := make(types.UTXOs)
	utxoResults := make(map[types.OutputIdentifier]*btcjson.TxRawResult)

	for _, inputRaw := range vin {
		if inputRaw.IsCoinBase() {
			continue
		}

		utxo, err := x.getTransactionByHash(inputRaw.Txid)
		if err != nil {
			log.WithFields(log.Fields{
				"error": err,
				"hash":  inputRaw.Txid,
				"vout":  inputRaw.Vout,
			}).Warn("Encountered non-wallet Vout")
			continue
		}

		utxoResults[types.OutputIdentifier{Hash: inputRaw.Txid, Index: inputRaw.Vout}] = utxo
	}

	for utxoID, utxoResult := range utxoResults {
		utxo, err := parseUTXO(utxoResult, utxoID.Index)
		if err != nil {
			return nil, err
		}

		utxos[utxoID] = *utxo
	}

	return utxos, nil
}

func parseUTXO(tx *btcjson.TxRawResult, outputIndex uint32) (*types.UTXOData, error) {
	utxoRaw := tx.Vout[outputIndex]

	switch addresses := utxoRaw.ScriptPubKey.Addresses; len(addresses) {
	case 0:
		// TODO: Document when this happens
		return &types.UTXOData{
			Value:   utils.ParseSatoshi(utxoRaw.Value), // !FIXME: Can panic
			Address: "",                                // Will be omitted by the JSON serializer
		}, nil
	case 1:
		return &types.UTXOData{
			Value:   utils.ParseSatoshi(utxoRaw.Value),
			Address: addresses[0], // ?XXX: Investigate why we do this
		}, nil
	default:
		value := utils.ParseSatoshi(utxoRaw.Value) // !FIXME: Can panic
		log.WithFields(log.Fields{
			"addresses":   addresses,
			"value":       value,
			"outputIndex": outputIndex,
		}).Warn("Multisig transaction detected.")

		return &types.UTXOData{
			Value:   value,
			Address: addresses[0],
		}, nil
	}
}

// getTransactionByHash gets the transaction with the given hash.
// Supports transaction hashes with or without 0x prefix.
func (x XRPC) getTransactionByHash(txHash string) (*btcjson.TxRawResult, error) {
	chainHash, err := utils.ParseChainHash(txHash)
	if err != nil {
		return nil, err
	}

	switch x.TxIndex {
	case true:
		txRaw, err := x.GetRawTransactionVerbose(chainHash)
		if err != nil {
			return nil, err
		}

		return txRaw, nil
	default:
		tx, err := x.Client.GetTransactionWatchOnly(chainHash, true)
		if err != nil {
			return nil, err
		}

		serializedTx, err := hex.DecodeString(tx.Hex)
		if err != nil {
			return nil, err
		}

		txRaw, err := x.Client.DecodeRawTransaction(serializedTx)
		if err != nil {
			return nil, err
		}

		// The decoded transaction hex doesn't contain confirmation number and
		// block height/hash; it must be fetched from the GetTransactionResult
		// instance.
		txRaw.Confirmations = uint64(tx.Confirmations)
		txRaw.BlockHash = tx.BlockHash
		txRaw.Time = tx.Time
		txRaw.Blocktime = tx.BlockTime

		return txRaw, nil
	}
}
