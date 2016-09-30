package validation

import (
	"bytes"
	"encoding/hex"
	"math"
	"strings"

	"chain/errors"
	"chain/math/checked"
	"chain/protocol/bc"
	"chain/protocol/state"
	"chain/protocol/vm"
	"chain/protocol/vmutil"
)

var (
	// ErrBadTx is returned for transactions failing validation
	ErrBadTx = errors.New("invalid transaction")

	// ErrFalseVMResult is one of the ways for a transaction to fail validation
	ErrFalseVMResult = errors.New("false VM result")
)

// ConfirmTx validates the given transaction against the given state tree
// before it's added to a block. If tx is invalid, it returns a non-nil
// error describing why.
//
// Tx should have already been validated (with `ValidateTx`) when the tx
// was added to the pool.
func ConfirmTx(snapshot *state.Snapshot, tx *bc.Tx, timestampMS uint64) error {
	if timestampMS < tx.MinTime {
		return errors.WithDetail(ErrBadTx, "block time is before transaction min time")
	}
	if tx.MaxTime > 0 && timestampMS > tx.MaxTime {
		return errors.WithDetail(ErrBadTx, "block time is after transaction max time")
	}

	for i, txin := range tx.Inputs {
		if ii, ok := txin.TypedInput.(*bc.IssuanceInput); ok {
			if txin.AssetVersion != 1 {
				continue
			}
			if len(ii.Nonce) == 0 {
				continue
			}
			if timestampMS < tx.MinTime || timestampMS > tx.MaxTime {
				return errors.WithDetail(ErrBadTx, "timestamp outside issuance input's time window")
			}
			// TODO(bobg): test that timestampMS + maxIssuanceWindow >= tx.MaxTimeMS
			// expire old items out of snapshot.Issuances
			for h, expireMS := range snapshot.Issuances {
				if timestampMS > expireMS {
					delete(snapshot.Issuances, h)
				}
			}
			iHash, err := tx.IssuanceHash(i)
			if err != nil {
				return err
			}
			if _, ok2 := snapshot.Issuances[iHash]; ok2 {
				return errors.WithDetail(ErrBadTx, "duplicate issuance transaction")
			}
			continue
		}

		// txin is a spend

		// Lookup the prevout in the blockchain state tree.
		k, val := state.OutputTreeItem(state.Prevout(txin))
		if !snapshot.Tree.Contains(k, val) {
			return errors.WithDetailf(ErrBadTx, "output %s for input %d is invalid", txin.Outpoint().String(), i)
		}
	}
	return nil
}

// ValidateTx checks whether tx passes context-free validation:
// - inputs and outputs balance
// - no duplicate input commitments
// - input scripts pass
//
// If tx is well formed and valid, it returns a nil error; otherwise, it
// returns an error describing why tx is invalid.
func ValidateTx(tx *bc.Tx) error {
	if len(tx.Inputs) == 0 {
		return errors.WithDetail(ErrBadTx, "inputs are missing")
	}

	if len(tx.Inputs) > math.MaxUint32 {
		return errors.WithDetail(ErrBadTx, "number of inputs overflows uint32")
	}

	// Are all inputs issuances, all with asset version 1, and all with empty nonces?
	allIssuancesWithEmptyNonces := true
	for _, txin := range tx.Inputs {
		if txin.AssetVersion != 1 {
			allIssuancesWithEmptyNonces = false
			break
		}
		ii, ok := txin.TypedInput.(*bc.IssuanceInput)
		if !ok {
			allIssuancesWithEmptyNonces = false
			break
		}
		if len(ii.Nonce) > 0 {
			allIssuancesWithEmptyNonces = false
			break
		}
	}
	if allIssuancesWithEmptyNonces {
		return errors.WithDetail(ErrBadTx, "all inputs are issuances with empty nonce fields")
	}

	// Check that the transaction maximum time is greater than or equal to the
	// minimum time, if it is greater than 0.
	if tx.MaxTime > 0 && tx.MaxTime < tx.MinTime {
		return errors.WithDetail(ErrBadTx, "positive maxtime must be >= mintime")
	}

	// Check that each input commitment appears only once. Also check that sums
	// of inputs and outputs balance, and check that both input and output sums
	// are less than 2^63 so that they don't overflow their int64 representation.
	parity := make(map[bc.AssetID]int64)

	for i, txin := range tx.Inputs {
		assetID := txin.AssetID()

		if txin.Amount() > math.MaxInt64 {
			return errors.WithDetail(ErrBadTx, "input value exceeds maximum value of int64")
		}

		sum, ok := checked.AddInt64(parity[assetID], int64(txin.Amount()))
		if !ok {
			return errors.WithDetailf(ErrBadTx, "adding input %d overflows the allowed asset amount", i)
		}
		parity[assetID] = sum

		if ii, ok := txin.TypedInput.(*bc.IssuanceInput); ok {
			if txin.AssetVersion != 1 {
				continue
			}
			if len(ii.Nonce) == 0 {
				continue
			}
			if tx.MinTime == 0 || tx.MaxTime == 0 {
				return errors.WithDetail(ErrBadTx, "issuance input with unbounded time window")
			}
		}

		for j := 0; j < i; j++ {
			other := tx.Inputs[j]
			if bytes.Equal(txin.InputCommitmentBytes(), other.InputCommitmentBytes()) {
				return errors.WithDetailf(ErrBadTx, "input %d is a duplicate of %d", j, i)
			}
		}
	}

	if len(tx.Outputs) > math.MaxInt32 {
		return errors.WithDetail(ErrBadTx, "number of outputs overflows int32")
	}

	// Check that every output has a valid value.
	for i, txout := range tx.Outputs {
		// Transactions cannot have zero-value outputs.
		// If all inputs have zero value, tx therefore must have no outputs.
		if txout.Amount == 0 {
			return errors.WithDetail(ErrBadTx, "output value must be greater than 0")
		}

		if txout.Amount > math.MaxInt64 {
			return errors.WithDetail(ErrBadTx, "output value exceeds maximum value of int64")
		}

		sum, ok := checked.SubInt64(parity[txout.AssetID], int64(txout.Amount))
		if !ok {
			return errors.WithDetailf(ErrBadTx, "adding output %d overflows the allowed asset amount", i)
		}
		parity[txout.AssetID] = sum
	}

	for asset, val := range parity {
		if val != 0 {
			return errors.WithDetailf(ErrBadTx, "amounts for asset %s are not balanced on inputs and outputs", asset)
		}
	}

	if len(tx.Inputs) > math.MaxInt32 {
		return errors.WithDetail(ErrBadTx, "number of inputs overflows int32")
	}

	for i := range tx.Inputs {
		ok, err := vm.VerifyTxInput(tx, i)
		if err == nil && !ok {
			err = ErrFalseVMResult
		}
		if err != nil {
			input := tx.Inputs[i]
			var program []byte
			if input.IsIssuance() {
				program = input.IssuanceProgram()
			} else {
				program = input.ControlProgram()
			}
			scriptStr, _ := vm.Disassemble(program)
			args := input.Arguments()
			hexArgs := make([]string, 0, len(args))
			for _, arg := range args {
				hexArgs = append(hexArgs, hex.EncodeToString(arg))
			}
			return errors.WithDetailf(ErrBadTx, "validation failed in script execution, input %d (program [%s] args [%s]): %s", i, scriptStr, strings.Join(hexArgs, " "), err)
		}
	}
	return nil
}

// ApplyTx updates the state tree with all the changes to the ledger.
func ApplyTx(snapshot *state.Snapshot, tx *bc.Tx) error {
	for i, in := range tx.Inputs {
		if ii, ok := in.TypedInput.(*bc.IssuanceInput); ok {
			if len(ii.Nonce) > 0 {
				iHash, err := tx.IssuanceHash(i)
				if err != nil {
					return err
				}
				snapshot.Issuances[iHash] = tx.MaxTime
			}
			continue
		}

		// Remove the consumed output from the state tree.
		prevoutKey, _ := state.OutputTreeItem(state.Prevout(in))
		err := snapshot.Tree.Delete(prevoutKey)
		if err != nil {
			return err
		}
	}

	for i, out := range tx.Outputs {
		if vmutil.IsUnspendable(out.ControlProgram) {
			continue
		}
		// Insert new outputs into the state tree.
		o := state.NewOutput(*out, bc.Outpoint{Hash: tx.Hash, Index: uint32(i)})
		err := snapshot.Tree.Insert(state.OutputTreeItem(o))
		if err != nil {
			return err
		}
	}
	return nil
}
