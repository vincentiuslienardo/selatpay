package chain

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/gagliardetto/solana-go"
)

// splTransferCheckedDiscriminator is the SPL Token program instruction tag
// for TransferChecked. The layout on the wire is:
//
//	[0]   u8  = 12
//	[1:9] u64 LE amount
//	[9]   u8  decimals
//
// See spl-token-program/src/instruction.rs — TransferChecked is explicitly
// the instruction Solana Pay mandates when an spl-token parameter is
// present in the URL, which is Selatpay's only supported payment path.
const splTransferCheckedDiscriminator byte = 12

// SPLTransfer is the subset of a TransferChecked instruction the watcher
// needs to credit a deposit: who paid, to which ATA, of which mint, and
// how much. Decimals round-trips so a downstream sanity check can reject
// a mint whose on-chain decimals disagree with the configured mint.
type SPLTransfer struct {
	Source    solana.PublicKey
	Mint      solana.PublicKey
	Dest      solana.PublicKey
	Authority solana.PublicKey
	Amount    uint64
	Decimals  uint8
}

// ParseSPLTransferChecked finds the first TransferChecked instruction in tx
// and returns its decoded fields. Any other instruction (memo, compute
// budget, additional token transfers) is ignored — Solana Pay payments
// carry exactly one TransferChecked and the watcher credits against that.
//
// Errors distinguish "no SPL transfer at all" (ErrNoSPLTransfer, expected
// for unrelated txs the logs stream might surface) from malformed data
// (other errors, which indicate either a non-conforming wallet or a bug
// in our parser and are worth surfacing loudly).
func ParseSPLTransferChecked(tx *solana.Transaction) (*SPLTransfer, error) {
	if tx == nil {
		return nil, errors.New("chain: nil transaction")
	}
	for i, ix := range tx.Message.Instructions {
		progID, err := tx.Message.ResolveProgramIDIndex(ix.ProgramIDIndex)
		if err != nil {
			return nil, fmt.Errorf("chain: resolve program id on ix %d: %w", i, err)
		}
		if !progID.Equals(solana.TokenProgramID) {
			continue
		}
		data := []byte(ix.Data)
		if len(data) == 0 || data[0] != splTransferCheckedDiscriminator {
			continue
		}
		return decodeTransferChecked(tx, ix)
	}
	return nil, ErrNoSPLTransfer
}

// ErrNoSPLTransfer is returned when a transaction carries no SPL
// TransferChecked instruction. Callers treat this as a benign skip.
var ErrNoSPLTransfer = errors.New("chain: no SPL TransferChecked instruction")

func decodeTransferChecked(tx *solana.Transaction, ix solana.CompiledInstruction) (*SPLTransfer, error) {
	data := []byte(ix.Data)
	// Layout: 1-byte discriminator, 8-byte amount LE, 1-byte decimals.
	if len(data) < 10 {
		return nil, fmt.Errorf("chain: TransferChecked data too short (%d bytes)", len(data))
	}
	amount := binary.LittleEndian.Uint64(data[1:9])
	decimals := data[9]

	// TransferChecked takes four accounts in fixed order: source, mint,
	// dest, authority. Extra trailing accounts are Solana Pay references
	// the payer may have attached to the instruction; we ignore them in
	// this parser — the watcher matches references against the whole
	// message's account keys regardless of which instruction cites them.
	if len(ix.Accounts) < 4 {
		return nil, fmt.Errorf("chain: TransferChecked needs ≥4 accounts, got %d", len(ix.Accounts))
	}
	keys, err := tx.Message.AccountMetaList()
	if err != nil {
		return nil, fmt.Errorf("chain: resolve account metas: %w", err)
	}
	resolve := func(idx uint16) (solana.PublicKey, error) {
		if int(idx) >= len(keys) {
			return solana.PublicKey{}, fmt.Errorf("account index %d out of range (%d keys)", idx, len(keys))
		}
		return keys[idx].PublicKey, nil
	}
	src, err := resolve(ix.Accounts[0])
	if err != nil {
		return nil, err
	}
	mint, err := resolve(ix.Accounts[1])
	if err != nil {
		return nil, err
	}
	dst, err := resolve(ix.Accounts[2])
	if err != nil {
		return nil, err
	}
	auth, err := resolve(ix.Accounts[3])
	if err != nil {
		return nil, err
	}
	return &SPLTransfer{
		Source:    src,
		Mint:      mint,
		Dest:      dst,
		Authority: auth,
		Amount:    amount,
		Decimals:  decimals,
	}, nil
}

// ReferencesIn returns the subset of known references that appear in tx's
// account key list. Solana Pay attaches reference pubkeys as read-only
// account metas on the transfer instruction, but some wallets surface
// them as additional top-level keys via a memo or noop instruction — so
// the watcher looks at the whole message's account set rather than any
// single instruction's account list.
func ReferencesIn(tx *solana.Transaction, known map[solana.PublicKey]struct{}) []solana.PublicKey {
	if tx == nil || len(known) == 0 {
		return nil
	}
	metas, err := tx.Message.AccountMetaList()
	if err != nil {
		return nil
	}
	var hits []solana.PublicKey
	seen := make(map[solana.PublicKey]struct{}, len(known))
	for _, m := range metas {
		if _, ok := known[m.PublicKey]; !ok {
			continue
		}
		if _, dup := seen[m.PublicKey]; dup {
			continue
		}
		seen[m.PublicKey] = struct{}{}
		hits = append(hits, m.PublicKey)
	}
	return hits
}
