// Command keygen reads or creates a Solana CLI compatible keypair
// JSON file (a JSON array of 64 byte values: secret seed concatenated
// with public key) and prints the pubkey plus the base58 secret as
// JSON. The base58 secret is what selatpayd's
// SELATPAY_HOT_WALLET_SECRET_BASE58 env var expects.
//
// Usage:
//
//	go run ./scripts/keygen --out scripts/keys/hot-wallet.json
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/gagliardetto/solana-go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "keygen: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		path = flag.String("out", "", "Path to read or create the keypair JSON")
	)
	flag.Parse()
	if *path == "" {
		return errors.New("--out is required")
	}

	wallet, err := loadOrCreate(*path)
	if err != nil {
		return err
	}

	out := struct {
		Pubkey       string `json:"pubkey"`
		SecretBase58 string `json:"secret_base58"`
		File         string `json:"file"`
	}{
		Pubkey:       wallet.PublicKey().String(),
		SecretBase58: wallet.PrivateKey.String(),
		File:         *path,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func loadOrCreate(path string) (solana.Wallet, error) {
	bs, err := os.ReadFile(path)
	if err == nil {
		var raw []byte
		if err := json.Unmarshal(bs, &raw); err != nil {
			return solana.Wallet{}, fmt.Errorf("unmarshal %s: %w", path, err)
		}
		return solana.Wallet{PrivateKey: solana.PrivateKey(raw)}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return solana.Wallet{}, err
	}

	w := solana.NewWallet()
	out, err := json.Marshal([]byte(w.PrivateKey))
	if err != nil {
		return solana.Wallet{}, err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return solana.Wallet{}, err
	}
	return *w, nil
}
