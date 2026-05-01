// Command pay simulates a Solana Pay payer for the demo. It parses a
// solana: transfer-request URL emitted by POST /v1/payment_intents,
// builds a single SPL TransferChecked instruction with the reference
// account attached, and submits it to the configured RPC.
//
// The simulator does the smallest amount of bootstrapping that the
// happy-path demo needs: it auto-airdrops SOL to the payer when
// running against the local solana-test-validator, and idempotently
// creates the payer's source associated token account if it does not
// already exist. Mint creation and minting USDC to the payer are
// handled by scripts/demo.sh using the solana CLI, not this tool.
//
// Usage:
//
//	go run ./scripts/pay.go \
//	    --rpc http://localhost:8899 \
//	    --url 'solana:<recipient>?spl-token=<mint>&amount=<usdc>&reference=<ref>&label=...' \
//	    --payer scripts/keys/payer.json
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gagliardetto/solana-go"
	ata "github.com/gagliardetto/solana-go/programs/associated-token-account"
	"github.com/gagliardetto/solana-go/programs/token"
	"github.com/gagliardetto/solana-go/rpc"
	confirm "github.com/gagliardetto/solana-go/rpc/sendAndConfirmTransaction"
	"github.com/gagliardetto/solana-go/rpc/ws"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "pay: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		rpcURL     = flag.String("rpc", envOr("SELATPAY_SOLANA_RPC_URL", "http://localhost:8899"), "Solana JSON-RPC endpoint")
		wsURL      = flag.String("ws", envOr("SELATPAY_SOLANA_WS_URL", "ws://localhost:8900"), "Solana WS endpoint (for signature subscribe on confirm)")
		urlArg     = flag.String("url", "", "Solana Pay transfer-request URL (solana:...). Required.")
		payerPath  = flag.String("payer", "scripts/keys/payer.json", "Path to payer keypair JSON (solana CLI format). Generated if missing.")
		minSOL     = flag.Float64("min-sol", 0.5, "Auto-airdrop until payer holds at least this many SOL (only against local test-validator)")
		commitment = flag.String("commitment", "confirmed", "Commitment level to wait for (processed, confirmed, finalized)")
		dry        = flag.Bool("dry-run", false, "Build and print the transaction but do not submit")
	)
	flag.Parse()

	if *urlArg == "" {
		return errors.New("--url is required")
	}

	pay, err := parseSolanaPay(*urlArg)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}

	payer, err := loadOrCreatePayer(*payerPath)
	if err != nil {
		return fmt.Errorf("load payer: %w", err)
	}
	fmt.Printf("payer        %s\n", payer.PublicKey())
	fmt.Printf("recipient    %s\n", pay.recipient)
	fmt.Printf("mint         %s\n", pay.splMint)
	fmt.Printf("amount       %d (raw, decimals=%d)\n", pay.amountRaw, pay.decimals)
	fmt.Printf("references   %v\n", pay.references)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cli := rpc.New(*rpcURL)

	if isLocalValidator(*rpcURL) {
		if err := ensureMinSOL(ctx, cli, payer.PublicKey(), *minSOL); err != nil {
			return fmt.Errorf("airdrop: %w", err)
		}
	}

	srcATA, _, err := solana.FindAssociatedTokenAddress(payer.PublicKey(), pay.splMint)
	if err != nil {
		return fmt.Errorf("derive source ata: %w", err)
	}
	fmt.Printf("source ata   %s\n", srcATA)

	ixs := []solana.Instruction{
		// CreateIdempotent is a no-op if the payer's USDC ATA already exists.
		ata.NewCreateIdempotentInstructionBuilder().
			SetPayer(payer.PublicKey()).
			SetWallet(payer.PublicKey()).
			SetMint(pay.splMint).
			Build(),
	}

	transfer := token.NewTransferCheckedInstructionBuilder().
		SetAmount(pay.amountRaw).
		SetDecimals(pay.decimals).
		SetSourceAccount(srcATA).
		SetMintAccount(pay.splMint).
		SetDestinationAccount(pay.recipient).
		SetOwnerAccount(payer.PublicKey())

	// Solana Pay reference accounts are appended to the TransferChecked
	// instruction as readonly, non-signer accounts. The watcher resolves
	// the deposit by querying getSignaturesForAddress(reference).
	for _, ref := range pay.references {
		transfer.Accounts.Append(solana.Meta(ref))
	}
	ixs = append(ixs, transfer.Build())

	recent, err := cli.GetLatestBlockhash(ctx, rpc.CommitmentConfirmed)
	if err != nil {
		return fmt.Errorf("blockhash: %w", err)
	}

	tx, err := solana.NewTransaction(ixs, recent.Value.Blockhash, solana.TransactionPayer(payer.PublicKey()))
	if err != nil {
		return fmt.Errorf("build tx: %w", err)
	}

	if _, err := tx.Sign(func(key solana.PublicKey) *solana.PrivateKey {
		if key.Equals(payer.PublicKey()) {
			pk := payer.PrivateKey
			return &pk
		}
		return nil
	}); err != nil {
		return fmt.Errorf("sign tx: %w", err)
	}

	if *dry {
		fmt.Println("--- transaction (dry run, not submitted) ---")
		fmt.Println(tx.String())
		return nil
	}

	wsCli, err := ws.Connect(ctx, *wsURL)
	if err != nil {
		return fmt.Errorf("ws connect: %w", err)
	}
	defer wsCli.Close()

	commit := parseCommitment(*commitment)
	timeout := 60 * time.Second
	sig, err := confirm.SendAndConfirmTransactionWithOpts(ctx, cli, wsCli, tx, rpc.TransactionOpts{
		PreflightCommitment: commit,
	}, &timeout)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}

	fmt.Println()
	fmt.Printf("signature    %s\n", sig)
	fmt.Printf("explorer     %s\n", explorerURL(*rpcURL, sig))
	return nil
}

type solanaPayURL struct {
	recipient  solana.PublicKey
	splMint    solana.PublicKey
	amountRaw  uint64
	decimals   uint8
	references []solana.PublicKey
	label      string
	message    string
}

func parseSolanaPay(raw string) (solanaPayURL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return solanaPayURL{}, err
	}
	if u.Scheme != "solana" {
		return solanaPayURL{}, fmt.Errorf("expected scheme solana, got %q", u.Scheme)
	}
	rec := strings.TrimPrefix(u.Opaque, "//")
	if rec == "" {
		rec = u.Host
	}
	if rec == "" {
		rec = u.Path
	}
	recipient, err := solana.PublicKeyFromBase58(rec)
	if err != nil {
		return solanaPayURL{}, fmt.Errorf("recipient: %w", err)
	}

	q := u.Query()
	mintStr := q.Get("spl-token")
	if mintStr == "" {
		return solanaPayURL{}, errors.New("missing spl-token")
	}
	mint, err := solana.PublicKeyFromBase58(mintStr)
	if err != nil {
		return solanaPayURL{}, fmt.Errorf("spl-token: %w", err)
	}

	// Amount in Solana Pay is a decimal in human units (e.g. "1.50"
	// USDC). For USDC (decimals=6) we hard-code; in production the
	// decimals are read from the mint account on chain.
	amtStr := q.Get("amount")
	if amtStr == "" {
		return solanaPayURL{}, errors.New("missing amount")
	}
	amountRaw, err := decimalToRaw(amtStr, 6)
	if err != nil {
		return solanaPayURL{}, fmt.Errorf("amount: %w", err)
	}

	var refs []solana.PublicKey
	for _, r := range q["reference"] {
		ref, err := solana.PublicKeyFromBase58(r)
		if err != nil {
			return solanaPayURL{}, fmt.Errorf("reference: %w", err)
		}
		refs = append(refs, ref)
	}

	return solanaPayURL{
		recipient:  recipient,
		splMint:    mint,
		amountRaw:  amountRaw,
		decimals:   6,
		references: refs,
		label:      q.Get("label"),
		message:    q.Get("message"),
	}, nil
}

func decimalToRaw(s string, decimals uint8) (uint64, error) {
	rat, ok := new(big.Rat).SetString(s)
	if !ok {
		return 0, fmt.Errorf("invalid decimal %q", s)
	}
	mul := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	rat.Mul(rat, new(big.Rat).SetInt(mul))
	if !rat.IsInt() {
		return 0, fmt.Errorf("amount %s exceeds %d decimals of precision", s, decimals)
	}
	v := rat.Num()
	if v.Sign() < 0 {
		return 0, fmt.Errorf("amount must be non-negative")
	}
	if !v.IsUint64() {
		return 0, fmt.Errorf("amount overflows uint64")
	}
	return v.Uint64(), nil
}

func loadOrCreatePayer(path string) (solana.Wallet, error) {
	if path == "" {
		return solana.Wallet{}, errors.New("payer path empty")
	}
	bs, err := os.ReadFile(path)
	if err == nil {
		// solana CLI keypair format: a JSON array of 64 byte values.
		var raw []byte
		if err := json.Unmarshal(bs, &raw); err != nil {
			return solana.Wallet{}, fmt.Errorf("unmarshal keypair %s: %w", path, err)
		}
		pk := solana.PrivateKey(raw)
		return solana.Wallet{PrivateKey: pk}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return solana.Wallet{}, err
	}

	w := solana.NewWallet()
	out, err := json.Marshal([]byte(w.PrivateKey))
	if err != nil {
		return solana.Wallet{}, err
	}
	if err := os.MkdirAll(parentDir(path), 0o755); err != nil {
		return solana.Wallet{}, err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return solana.Wallet{}, err
	}
	fmt.Printf("generated payer keypair at %s\n", path)
	return *w, nil
}

func parentDir(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return "."
}

func ensureMinSOL(ctx context.Context, cli *rpc.Client, who solana.PublicKey, minSOL float64) error {
	bal, err := cli.GetBalance(ctx, who, rpc.CommitmentConfirmed)
	if err != nil {
		return err
	}
	want := uint64(minSOL * float64(solana.LAMPORTS_PER_SOL))
	if bal.Value >= want {
		return nil
	}
	missing := want - bal.Value
	fmt.Printf("airdrop      requesting %d lamports to %s\n", missing, who)
	sig, err := cli.RequestAirdrop(ctx, who, missing, rpc.CommitmentConfirmed)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st, err := cli.GetSignatureStatuses(ctx, true, sig)
		if err == nil && len(st.Value) == 1 && st.Value[0] != nil && st.Value[0].ConfirmationStatus == rpc.ConfirmationStatusConfirmed {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("airdrop %s did not confirm in time", sig)
}

func parseCommitment(s string) rpc.CommitmentType {
	switch strings.ToLower(s) {
	case "processed":
		return rpc.CommitmentProcessed
	case "finalized":
		return rpc.CommitmentFinalized
	default:
		return rpc.CommitmentConfirmed
	}
}

func isLocalValidator(rpcURL string) bool {
	return strings.Contains(rpcURL, "localhost") || strings.Contains(rpcURL, "127.0.0.1")
}

func explorerURL(rpcURL string, sig solana.Signature) string {
	switch {
	case strings.Contains(rpcURL, "localhost"), strings.Contains(rpcURL, "127.0.0.1"):
		return fmt.Sprintf("https://explorer.solana.com/tx/%s?cluster=custom&customUrl=%s", sig, rpcURL)
	case strings.Contains(rpcURL, "devnet"):
		return fmt.Sprintf("https://explorer.solana.com/tx/%s?cluster=devnet", sig)
	case strings.Contains(rpcURL, "testnet"):
		return fmt.Sprintf("https://explorer.solana.com/tx/%s?cluster=testnet", sig)
	default:
		return fmt.Sprintf("https://explorer.solana.com/tx/%s", sig)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
