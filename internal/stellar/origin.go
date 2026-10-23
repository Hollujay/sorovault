package stellar

import (
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

var _ OriginFinder = (*RPCFetcher)(nil)

// maxOriginPages bounds how many pages of one ledger's transactions
// FindOrigin reads. A ledger holds at most a few hundred Soroban
// transactions, so the bound is never reached by an honest endpoint.
const maxOriginPages = 20

// FindOrigin determines who created a contract, and when, from the RPC's
// retained history.
//
// The contract's instance entry says which ledger last modified it. When the
// contract has not changed since it was created, that is the creation ledger,
// and the transaction that created the entry is found among that ledger's
// transactions by its metadata. A contract that has been modified since, or
// whose creation is older than the RPC retains, yields ErrOriginUnknown:
// a deployer found by guessing would be worse than none, since publisher
// verification trusts it.
func (f *RPCFetcher) FindOrigin(ctx context.Context, contractID string) (*Origin, error) {
	key, err := instanceLedgerKey(contractID)
	if err != nil {
		return nil, err
	}
	entry, err := f.singleEntry(ctx, key)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, notFoundError(contractID)
	}
	ledger := entry.LastModifiedLedger

	contractHash, err := contractIDBytes(contractID)
	if err != nil {
		return nil, err
	}

	cursor := ""
	for page := 0; page < maxOriginPages; page++ {
		req := protocol.GetTransactionsRequest{StartLedger: ledger}
		if cursor != "" {
			req = protocol.GetTransactionsRequest{}
		}
		req.Pagination = &protocol.LedgerPaginationOptions{Cursor: cursor, Limit: 200}

		resp, err := f.client.GetTransactions(ctx, req)
		if err != nil {
			if isOutOfRange(err) {
				return nil, fmt.Errorf("%w: ledger %d is outside the RPC's retention", ErrOriginUnknown, ledger)
			}
			return nil, fmt.Errorf("stellar: getTransactions: %w", err)
		}

		for _, tx := range resp.Transactions {
			if tx.Ledger > ledger {
				return nil, fmt.Errorf("%w: no creating transaction in ledger %d", ErrOriginUnknown, ledger)
			}
			if tx.Ledger != ledger || tx.Status != "SUCCESS" {
				continue
			}
			creator, ok, err := creatorIn(tx.TransactionDetails, contractHash)
			if err != nil {
				return nil, err
			}
			if ok {
				return &Origin{Creator: creator, Ledger: ledger}, nil
			}
		}

		if len(resp.Transactions) == 0 || resp.Cursor == "" || resp.Cursor == cursor {
			break
		}
		cursor = resp.Cursor
	}
	return nil, fmt.Errorf("%w: no creating transaction in ledger %d", ErrOriginUnknown, ledger)
}

// creatorIn reports whether the transaction created the contract's instance
// entry and, if so, which account is to be credited with creating it.
func creatorIn(tx protocol.TransactionDetails, contract xdr.ContractId) (creator string, created bool, err error) {
	if tx.ResultMetaXDR == "" || tx.EnvelopeXDR == "" {
		return "", false, nil
	}
	var meta xdr.TransactionMeta
	if err := unmarshalBase64(tx.ResultMetaXDR, &meta); err != nil {
		return "", false, fmt.Errorf("stellar: decoding transaction meta: %w", err)
	}

	opIndex := -1
	for i, changes := range operationChanges(meta) {
		for _, change := range changes {
			if change.Type == xdr.LedgerEntryChangeTypeLedgerEntryCreated &&
				change.Created != nil && isInstanceOf(change.Created.Data, contract) {
				opIndex = i
			}
		}
	}
	if opIndex < 0 {
		return "", false, nil
	}

	var env xdr.TransactionEnvelope
	if err := unmarshalBase64(tx.EnvelopeXDR, &env); err != nil {
		return "", false, fmt.Errorf("stellar: decoding transaction envelope: %w", err)
	}
	source := env.SourceAccount()
	if ops := env.Operations(); opIndex < len(ops) && ops[opIndex].SourceAccount != nil {
		source = *ops[opIndex].SourceAccount
	}
	id := source.ToAccountId()
	return id.Address(), true, nil
}

// operationChanges returns the ledger changes of each operation of a
// transaction, whichever meta version the network produced it in.
func operationChanges(meta xdr.TransactionMeta) []xdr.LedgerEntryChanges {
	var out []xdr.LedgerEntryChanges
	switch {
	case meta.V == 4 && meta.V4 != nil:
		for _, op := range meta.V4.Operations {
			out = append(out, op.Changes)
		}
	case meta.V == 3 && meta.V3 != nil:
		for _, op := range meta.V3.Operations {
			out = append(out, op.Changes)
		}
	case meta.V == 2 && meta.V2 != nil:
		for _, op := range meta.V2.Operations {
			out = append(out, op.Changes)
		}
	case meta.V == 1 && meta.V1 != nil:
		for _, op := range meta.V1.Operations {
			out = append(out, op.Changes)
		}
	default:
		if ops, ok := meta.GetOperations(); ok {
			for _, op := range ops {
				out = append(out, op.Changes)
			}
		}
	}
	return out
}

// isInstanceOf reports whether a ledger entry is the instance entry of the
// contract.
func isInstanceOf(data xdr.LedgerEntryData, contract xdr.ContractId) bool {
	cd := data.ContractData
	if cd == nil || cd.Key.Type != xdr.ScValTypeScvLedgerKeyContractInstance {
		return false
	}
	return cd.Contract.Type == xdr.ScAddressTypeScAddressTypeContract &&
		cd.Contract.ContractId != nil && *cd.Contract.ContractId == contract
}

func contractIDBytes(contractID string) (xdr.ContractId, error) {
	raw, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil || len(raw) != 32 {
		return xdr.ContractId{}, fmt.Errorf("stellar: %q is not a contract address", contractID)
	}
	return xdr.ContractId(*(*[32]byte)(raw)), nil
}

// isOutOfRange reports whether an RPC error says a requested ledger is
// outside the range the node retains.
func isOutOfRange(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled) &&
		strings.Contains(err.Error(), "ledger range")
}
