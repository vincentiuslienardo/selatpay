package chain

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/gagliardetto/solana-go"
)

// buildTransferCheckedTx constructs a minimal Transaction carrying a single
// TransferChecked instruction. The wire format mirrors what a real wallet
// produces: signers up front, then writable non-signers, then readonly
// non-signers (program, mint, references) — same ordering invariant the
// Solana runtime enforces.
func buildTransferCheckedTx(t *testing.T, amount uint64, decimals uint8, references ...solana.PublicKey) (
	*solana.Transaction,
	solana.PublicKey, // source
	solana.PublicKey, // mint
	solana.PublicKey, // dest
	solana.PublicKey, // authority
) {
	t.Helper()
	authority := solana.NewWallet().PublicKey()
	source := solana.NewWallet().PublicKey()
	dest := solana.NewWallet().PublicKey()
	mint := solana.NewWallet().PublicKey()

	// AccountKeys layout (writability is derived from header offsets):
	//   [0] authority   — signer, writable
	//   [1] source      — non-signer, writable
	//   [2] dest        — non-signer, writable
	//   [3] mint        — non-signer, readonly
	//   [4] program id  — non-signer, readonly
	//   [5...] refs     — non-signer, readonly
	keys := solana.PublicKeySlice{authority, source, dest, mint, solana.TokenProgramID}
	keys = append(keys, references...)

	data := make([]byte, 10)
	data[0] = splTransferCheckedDiscriminator
	binary.LittleEndian.PutUint64(data[1:9], amount)
	data[9] = decimals

	ix := solana.CompiledInstruction{
		ProgramIDIndex: 4,
		Accounts:       []uint16{1, 3, 2, 0}, // source, mint, dest, authority
		Data:           data,
	}
	// Count readonly non-signer accounts (indexes 3 onward): mint + program + refs.
	readonlyUnsigned := uint8(2 + len(references))

	tx := &solana.Transaction{
		Message: solana.Message{
			Header: solana.MessageHeader{
				NumRequiredSignatures:       1,
				NumReadonlySignedAccounts:   0,
				NumReadonlyUnsignedAccounts: readonlyUnsigned,
			},
			AccountKeys:  keys,
			Instructions: []solana.CompiledInstruction{ix},
		},
	}
	return tx, source, mint, dest, authority
}

func TestParseSPLTransferChecked_Roundtrip(t *testing.T) {
	const amount uint64 = 1_234_567_890
	const decimals uint8 = 6
	ref := solana.NewWallet().PublicKey()
	tx, src, mint, dest, auth := buildTransferCheckedTx(t, amount, decimals, ref)

	got, err := ParseSPLTransferChecked(tx)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Amount != amount {
		t.Errorf("amount: got %d want %d", got.Amount, amount)
	}
	if got.Decimals != decimals {
		t.Errorf("decimals: got %d want %d", got.Decimals, decimals)
	}
	if !got.Source.Equals(src) {
		t.Errorf("source: got %s want %s", got.Source, src)
	}
	if !got.Mint.Equals(mint) {
		t.Errorf("mint: got %s want %s", got.Mint, mint)
	}
	if !got.Dest.Equals(dest) {
		t.Errorf("dest: got %s want %s", got.Dest, dest)
	}
	if !got.Authority.Equals(auth) {
		t.Errorf("authority: got %s want %s", got.Authority, auth)
	}
}

func TestParseSPLTransferChecked_IgnoresNonTokenInstructions(t *testing.T) {
	const amount uint64 = 42
	tx, _, _, _, _ := buildTransferCheckedTx(t, amount, 6)

	// Prepend a memo instruction — watcher must skip it and find the
	// TransferChecked that follows.
	memoKeyIdx := uint16(len(tx.Message.AccountKeys))
	tx.Message.AccountKeys = append(tx.Message.AccountKeys, solana.MemoProgramID)
	tx.Message.Header.NumReadonlyUnsignedAccounts++
	memo := solana.CompiledInstruction{
		ProgramIDIndex: memoKeyIdx,
		Accounts:       nil,
		Data:           []byte("Order #4242"),
	}
	tx.Message.Instructions = append([]solana.CompiledInstruction{memo}, tx.Message.Instructions...)

	got, err := ParseSPLTransferChecked(tx)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Amount != amount {
		t.Errorf("amount: got %d want %d", got.Amount, amount)
	}
}

func TestParseSPLTransferChecked_NoTransferPresent(t *testing.T) {
	authority := solana.NewWallet().PublicKey()
	tx := &solana.Transaction{
		Message: solana.Message{
			Header: solana.MessageHeader{
				NumRequiredSignatures:       1,
				NumReadonlyUnsignedAccounts: 1,
			},
			AccountKeys: solana.PublicKeySlice{authority, solana.MemoProgramID},
			Instructions: []solana.CompiledInstruction{
				{ProgramIDIndex: 1, Data: []byte("hi")},
			},
		},
	}
	_, err := ParseSPLTransferChecked(tx)
	if !errors.Is(err, ErrNoSPLTransfer) {
		t.Fatalf("err: got %v want ErrNoSPLTransfer", err)
	}
}

func TestParseSPLTransferChecked_TruncatedData(t *testing.T) {
	tx, _, _, _, _ := buildTransferCheckedTx(t, 100, 6)
	// Corrupt the data to 4 bytes — shorter than the 10-byte layout.
	tx.Message.Instructions[0].Data = []byte{splTransferCheckedDiscriminator, 0x01, 0x02, 0x03}
	_, err := ParseSPLTransferChecked(tx)
	if err == nil || errors.Is(err, ErrNoSPLTransfer) {
		t.Fatalf("expected truncated-data error, got %v", err)
	}
}

func TestParseSPLTransferChecked_NilTx(t *testing.T) {
	_, err := ParseSPLTransferChecked(nil)
	if err == nil {
		t.Fatal("expected error on nil tx")
	}
}

func TestReferencesIn_MatchesAccountKey(t *testing.T) {
	refA := solana.NewWallet().PublicKey()
	refB := solana.NewWallet().PublicKey()
	refUnused := solana.NewWallet().PublicKey()

	tx, _, _, _, _ := buildTransferCheckedTx(t, 100, 6, refA, refB)

	known := map[solana.PublicKey]struct{}{
		refA:      {},
		refB:      {},
		refUnused: {},
	}
	hits := ReferencesIn(tx, known)
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d: %v", len(hits), hits)
	}
	// Order reflects AccountKeys iteration, which is deterministic.
	seen := map[solana.PublicKey]bool{}
	for _, h := range hits {
		seen[h] = true
	}
	if !seen[refA] || !seen[refB] {
		t.Fatalf("missing expected hit: %v", hits)
	}
	if seen[refUnused] {
		t.Fatalf("unused ref should not be reported")
	}
}

func TestReferencesIn_EmptyKnown(t *testing.T) {
	tx, _, _, _, _ := buildTransferCheckedTx(t, 100, 6, solana.NewWallet().PublicKey())
	if got := ReferencesIn(tx, nil); got != nil {
		t.Fatalf("expected nil for empty known, got %v", got)
	}
	if got := ReferencesIn(tx, map[solana.PublicKey]struct{}{}); got != nil {
		t.Fatalf("expected nil for empty known map, got %v", got)
	}
}
