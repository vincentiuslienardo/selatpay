package recon

import (
	"context"
	"errors"
	"testing"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

type stubFetcher struct {
	amount string
	err    error
}

func (s *stubFetcher) GetTokenAccountBalance(ctx context.Context, account solana.PublicKey, commitment rpc.CommitmentType) (*rpc.GetTokenAccountBalanceResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &rpc.GetTokenAccountBalanceResult{
		Value: &rpc.UiTokenAmount{Amount: s.amount},
	}, nil
}

func TestNewWalker_RejectsZeroPubkeys(t *testing.T) {
	stub := &stubFetcher{amount: "0"}
	if _, err := NewWalker(nil, stub, solana.NewWallet().PublicKey(), solana.NewWallet().PublicKey(), nil); err == nil {
		t.Error("expected pool error")
	}
}

func TestWalker_FetchOnChainParsesBaseTen(t *testing.T) {
	stub := &stubFetcher{amount: "1234567"}
	w := &Walker{rpc: stub, hotWalletATA: solana.NewWallet().PublicKey(), commitment: rpc.CommitmentFinalized}
	got, err := w.fetchOnChainBalance(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got != 1234567 {
		t.Errorf("got %d want 1234567", got)
	}
}

func TestWalker_FetchOnChainPropagatesRPCError(t *testing.T) {
	stub := &stubFetcher{err: errors.New("boom")}
	w := &Walker{rpc: stub, hotWalletATA: solana.NewWallet().PublicKey(), commitment: rpc.CommitmentFinalized}
	if _, err := w.fetchOnChainBalance(context.Background()); err == nil {
		t.Error("expected error")
	}
}
