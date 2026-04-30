# ADR-0001: Solana as the settlement chain

- Status: Accepted
- Date: 2026-04-17

## Context

The corridor we are settling is Singapore (USDC source) to Indonesia (IDR destination). The chain choice is load-bearing: it dictates fees, finality, the on-chain UX surface (URI scheme, wallet support), and how payment monitoring is structured.

Three credible options:

1. Ethereum L1. Mature, deepest USDC liquidity, but per-transfer cost (USD 0.20 to several dollars on contention) and 12-second blocks plus reorg risk make it unworkable for a payments product targeting consumer-sized remittance.
2. An EVM L2 (Base, Arbitrum, Polygon). Fees acceptable, but bridge and finality semantics complicate the operational model, deposit flows still typically use HD-wallet derivation plus a sweeper to consolidate funds, and the EVM token monitoring story (logs, reorgs across L1 and L2) is heavier than necessary.
3. Solana. Sub-second blocks, around USD 0.0001 per transfer, single global state with no L2 bridge to operate, and a payment-specific URI standard (Solana Pay) that is supported natively by the major wallets (Phantom, Backpack, Solflare).

## Decision

We settle on Solana, USDC SPL on devnet for this build, with a clean path to mainnet via the same code (only the mint address and RPC endpoint change).

The deciding properties:

- Solana Pay defines a `solana:` URI scheme with a `reference` field that lets us bind a per-intent ed25519 public key to a transfer without modifying token semantics. The watcher resolves deposits with `getSignaturesForAddress(reference)`, which is dramatically simpler than EVM ERC-20 log scanning with HD-wallet sweep.
- Finalized commitment lands in around 13 seconds, fast enough for a "tap to pay, see it settled" merchant UX.
- SPL token transfers are a single instruction with deterministic sender, recipient, mint, amount, and decimals. We parse them with the canonical `solana-go` SDK and never need to chase ERC-20 transfer logs.
- Direct deposit to the merchant hot wallet's associated token account means no sweep step, no gas top-up, and no per-deposit derivation key to track. ADR-0006 covers the reference-key flow that replaces HD-wallet derivation.

## Consequences

- All settlement code is Solana-specific (`internal/chain`, `internal/solanapay`). Multi-chain support would require an abstraction layer that we have explicitly deferred.
- Monitoring depends on a healthy RPC and websocket provider. We use a poller fallback (`getSignaturesForAddress`) so that a flaky `signatureSubscribe` does not block settlement.
- The Indonesian payout rail is fiat (mocked here) and remains chain-agnostic: the saga sees a credited intent and posts a payout, regardless of which chain delivered the USDC.

## Alternatives considered

- USDT-SPL instead of USDC. Same rails, different mint. Deferred to a multi-asset extension.
- Tron (where USDT settlement is common in SEA OTC). Lower fees than EVM L1 but worse tooling, weaker compliance posture, and no equivalent of Solana Pay. Rejected.
- A custom Anchor program. ADR-0009 covers why this is unnecessary for v1.
