# ADR-0006: Solana Pay reference key per intent, no HD-wallet sweeper

- Status: Accepted
- Date: 2026-04-22

## Context

In an EVM-style deposit flow, the canonical pattern is:

1. Derive a fresh deposit address (HD wallet) per intent.
2. Watch that address for an ERC-20 transfer.
3. Sweep the funds to a hot wallet so the platform's working balance is consolidated.

This works but adds three moving parts: a derivation key (and its custody), a sweeper that must not sweep before the deposit confirms, and a separate "deposit detected" vs "deposit available" state because the sweep can fail.

Solana Pay offers a different primitive: any transfer can include zero or more **reference accounts** as read-only accounts that do not affect the transfer's balance. The reference is a regular ed25519 public key that the recipient generates. The watcher can ask the RPC `getSignaturesForAddress(reference)` to find the transaction that paid that intent, no matter how many transfers landed on the recipient ATA in parallel.

## Decision

Per intent we generate a fresh ed25519 keypair (`internal/solanapay/reference.go`) and treat the public key as the binding key between the off-chain intent and the on-chain transfer.

- The Solana Pay URL (`solana:<recipient>?spl-token=<mint>&amount=<usdc>&reference=<pubkey>&label=...&message=...`) embeds the reference pubkey.
- The recipient is the merchant hot wallet's USDC associated token account (ATA), derived deterministically from the hot wallet pubkey and the USDC mint. Funds land directly in the working balance with no sweep step.
- The reference secret is encrypted at rest with a process-level encryption key (`SELATPAY_REFERENCE_ENC_KEY`) before being stored in `payment_intents.reference_secret_enc`. The secret is not strictly required (the watcher only needs the public key), but holding it lets us prove ownership in disputes without depending on RPC archival.
- The watcher resolves a deposit by polling `getSignaturesForAddress(reference)` (with `signatureSubscribe` as the fast path) and parsing the resulting transaction's `TransferChecked` instruction.

## Consequences

- No sweeper, no HD-wallet derivation key custody, no per-deposit gas top-up.
- Two deposits to the same intent (a payer who retries) collide on the same reference and are visible as separate signatures under that pubkey; we credit the first finalized one and surface the rest as overpayment for refund.
- The hot wallet must be funded with a small amount of SOL for transaction fees the merchant pays (e.g. for sweeping later from hot to treasury, if that ever becomes desirable). The deposit itself is paid by the payer, so no merchant top-up is required for receiving.
- The flow is idiomatic Solana Pay; any wallet that supports the spec works without integration.

## Alternatives considered

- **HD-wallet per-intent address (EVM idiom)**. Possible on Solana too via on-the-fly keypair generation, but loses the major Solana win (no sweep) and adds custody surface for no benefit.
- **Memo program for binding the intent id**. The memo is in the transaction but is not indexable the way a reference account is. `getSignaturesForAddress` does not work against memo content.
- **Single shared reference per merchant**. Loses the per-intent binding; collisions become routine. Rejected.
