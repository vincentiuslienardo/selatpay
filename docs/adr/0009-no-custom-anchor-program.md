# ADR-0009: No custom Anchor program for v1

- Status: Accepted
- Date: 2026-04-22

## Context

Most "Solana payments product" portfolios reach for an Anchor program by reflex. There is a real question whether one is justified for a settlement product, or whether it adds custody surface and audit cost without paying for itself.

Things a custom on-chain program could do for us:

1. Enforce that a deposit is conditional on something (escrow until X, refund after Y).
2. Make the on-chain transfer atomic with a record-keeping CPI (cross-program invocation), so that a deposit cannot land without an on-chain receipt.
3. Mint a non-transferable receipt token to the payer (proof of payment).
4. Support partial refunds, scheduled payouts, or merchant-side conditional logic on chain.

Things that argue against:

1. A new program means a new audit, new on-chain risk surface, new upgrade authority that has to be custodied as carefully as the hot wallet keys.
2. Any logic on chain duplicates logic that already lives in our saga and ledger, which already enforce all the invariants we care about (idempotency, double-entry balance, exactly-once webhook).
3. Wallet UX. Solana Pay's `solana:` URL with a vanilla `TransferChecked` is supported by every major wallet out of the box. A custom program means custom transaction construction that wallets must be willing to sign, which fragments the UX.

## Decision

v1 ships with stock SPL Token plus Solana Pay, no custom Anchor program. The on-chain interaction is exactly one `TransferChecked` instruction with a reference account attached for binding to the off-chain intent.

We would consider an Anchor program if and only if one of the following appears in a real product requirement:

- Conditional escrow with on-chain refund timing (e.g. invoice expires after T hours and funds return to the payer without merchant action). Today this is operator-enforced via the saga; an on-chain version is a real product feature, not a flex.
- A receipt token (proof-of-payment NFT) that downstream protocols compose with. Niche but plausible.
- Native on-chain support for the merchant's chart-of-accounts (e.g. fees split on chain). Extremely rare in practice; usually better off-chain.

## Consequences

- No on-chain audit cost. No upgrade-authority custody concern. No new attack surface beyond the SPL Token and System programs that already exist.
- The platform is portable across chains that support a USDC equivalent and a payment-binding primitive. Re-implementing on Base or Solana mainnet beta is a config change, not a program port.
- We forgo the differentiation of "we have a custom program". This is explicitly fine; the differentiation is the ledger and saga discipline, not the contract surface.

## Alternatives considered

- **Ship a custom Anchor program as a portfolio flex**. Worth the time only if it teaches something the rest of the build does not. We chose to spend that engineering budget on the ledger, saga, outbox, and reconciliation instead, which are higher-signal for a payments-platform engineering role.
