package solanapay

import (
	"strings"
	"testing"

	"github.com/gagliardetto/solana-go"
)

func TestBuildParseURL_Roundtrip(t *testing.T) {
	recipient := solana.MustPublicKeyFromBase58("DjuMPGThkGdyk2vDvDDYjTUZynnBq9rZjYJBdoWcE7PG")
	mint := solana.MustPublicKeyFromBase58("4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU")
	ref := solana.MustPublicKeyFromBase58("CS8j7vMi6jfoM1C3ghhz4C2R5tXRbJzZzngzkdtxCn3w")

	in := URLParams{
		Recipient:  recipient,
		SPLToken:   &mint,
		Amount:     "1.5",
		References: []solana.PublicKey{ref},
		Label:      "Selatpay",
		Message:    "Order INV-4242",
		Memo:       "inv-4242",
	}
	s, err := BuildURL(in)
	if err != nil {
		t.Fatalf("BuildURL: %v", err)
	}
	if !strings.HasPrefix(s, "solana:"+recipient.String()+"?") {
		t.Fatalf("URL prefix drift: %s", s)
	}
	out, err := ParseURL(s)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if out.Recipient != recipient {
		t.Fatalf("recipient mismatch: %s", out.Recipient)
	}
	if out.SPLToken == nil || *out.SPLToken != mint {
		t.Fatalf("spl-token mismatch: %+v", out.SPLToken)
	}
	if out.Amount != "1.5" {
		t.Fatalf("amount mismatch: %q", out.Amount)
	}
	if len(out.References) != 1 || out.References[0] != ref {
		t.Fatalf("reference mismatch: %+v", out.References)
	}
	if out.Label != "Selatpay" || out.Message != "Order INV-4242" || out.Memo != "inv-4242" {
		t.Fatalf("text fields mismatch: %+v", out)
	}
}

func TestBuildURL_MinimalOnlyRecipient(t *testing.T) {
	r := solana.MustPublicKeyFromBase58("DjuMPGThkGdyk2vDvDDYjTUZynnBq9rZjYJBdoWcE7PG")
	s, err := BuildURL(URLParams{Recipient: r})
	if err != nil {
		t.Fatalf("BuildURL: %v", err)
	}
	want := "solana:" + r.String()
	if s != want {
		t.Fatalf("minimal URL drift: got %q want %q", s, want)
	}
}

func TestBuildURL_MultipleReferences(t *testing.T) {
	r := solana.MustPublicKeyFromBase58("DjuMPGThkGdyk2vDvDDYjTUZynnBq9rZjYJBdoWcE7PG")
	r1 := solana.MustPublicKeyFromBase58("CS8j7vMi6jfoM1C3ghhz4C2R5tXRbJzZzngzkdtxCn3w")
	r2 := solana.MustPublicKeyFromBase58("4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU")
	s, err := BuildURL(URLParams{Recipient: r, References: []solana.PublicKey{r1, r2}})
	if err != nil {
		t.Fatalf("BuildURL: %v", err)
	}
	if strings.Count(s, "reference=") != 2 {
		t.Fatalf("expected two reference params: %s", s)
	}
	out, err := ParseURL(s)
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	if len(out.References) != 2 || out.References[0] != r1 || out.References[1] != r2 {
		t.Fatalf("reference roundtrip drift: %+v", out.References)
	}
}

func TestBuildURL_RejectsZeroRecipient(t *testing.T) {
	if _, err := BuildURL(URLParams{}); err == nil {
		t.Fatal("expected error for zero recipient")
	}
}

func TestBuildURL_RejectsZeroSPLToken(t *testing.T) {
	r := solana.MustPublicKeyFromBase58("DjuMPGThkGdyk2vDvDDYjTUZynnBq9rZjYJBdoWcE7PG")
	var zero solana.PublicKey
	if _, err := BuildURL(URLParams{Recipient: r, SPLToken: &zero}); err == nil {
		t.Fatal("expected error for zero spl-token")
	}
}

func TestParseURL_RejectsNonSolanaScheme(t *testing.T) {
	if _, err := ParseURL("https://example.com/pay"); err == nil {
		t.Fatal("expected error for non-solana scheme")
	}
}

func TestParseURL_RejectsMissingRecipient(t *testing.T) {
	if _, err := ParseURL("solana:?amount=1"); err == nil {
		t.Fatal("expected error for missing recipient")
	}
}

func TestParseURL_RejectsMalformedRecipient(t *testing.T) {
	if _, err := ParseURL("solana:not-base58?amount=1"); err == nil {
		t.Fatal("expected error for malformed recipient")
	}
}

func TestParseURL_RejectsEmpty(t *testing.T) {
	if _, err := ParseURL(""); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		raw      uint64
		decimals uint8
		want     string
	}{
		{0, 6, "0"},
		{1, 6, "0.000001"},
		{1_000_000, 6, "1"},
		{1_500_000, 6, "1.5"},
		{1_234_567, 6, "1.234567"},
		{12_345, 6, "0.012345"},
		{1_000_000_000, 9, "1"},
		{42, 0, "42"},
	}
	for _, tc := range cases {
		got := FormatAmount(tc.raw, tc.decimals)
		if got != tc.want {
			t.Errorf("FormatAmount(%d,%d)=%q want %q", tc.raw, tc.decimals, got, tc.want)
		}
	}
}

func TestParseAmount(t *testing.T) {
	cases := []struct {
		s        string
		decimals uint8
		want     uint64
	}{
		{"0", 6, 0},
		{"1", 6, 1_000_000},
		{"1.5", 6, 1_500_000},
		{"0.000001", 6, 1},
		{"1.234567", 6, 1_234_567},
		{"42", 0, 42},
	}
	for _, tc := range cases {
		got, err := ParseAmount(tc.s, tc.decimals)
		if err != nil {
			t.Errorf("ParseAmount(%q,%d): %v", tc.s, tc.decimals, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseAmount(%q,%d)=%d want %d", tc.s, tc.decimals, got, tc.want)
		}
	}
}

func TestParseAmount_RejectsInvalid(t *testing.T) {
	cases := []string{
		"",
		"-1",
		"1.2345678", // 7 frac digits > 6 decimals
		"1e5",       // scientific notation
		"abc",
		"1.a",
	}
	for _, s := range cases {
		if _, err := ParseAmount(s, 6); err == nil {
			t.Errorf("ParseAmount(%q) should have errored", s)
		}
	}
}

func TestFormatParseAmount_Roundtrip(t *testing.T) {
	raws := []uint64{0, 1, 42, 500_000, 1_000_000, 1_234_567, 999_999_999, 1_000_000_000_000}
	for _, raw := range raws {
		s := FormatAmount(raw, 6)
		got, err := ParseAmount(s, 6)
		if err != nil {
			t.Errorf("roundtrip(%d): parse %q: %v", raw, s, err)
			continue
		}
		if got != raw {
			t.Errorf("roundtrip(%d): got %d via %q", raw, got, s)
		}
	}
}
