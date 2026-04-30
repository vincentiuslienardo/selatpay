# ADR-0007: KMS-shaped signer interface, local ed25519 in MVP

- Status: Accepted
- Date: 2026-04-22

## Context

In production, hot-wallet keys for a payments platform must live in a Hardware Security Module or a Multi-Party Computation custody service (Fireblocks, Turnkey, AWS KMS with ed25519, GCP Cloud KMS, etc.). The keys must never be loadable into the application process, and signing must happen behind an audited, rate-limited, policy-enforced boundary. None of that is on the table for an MVP whose goal is to show the engineering approach.

The risk is shipping an MVP where the signing call site is so coupled to "I have a private key in memory" that swapping in real custody later is a rewrite.

## Decision

Define a `wallet.Signer` interface (`internal/wallet/signer.go`) that exposes only what callers need:

```go
type Signer interface {
    PublicKey() solana.PublicKey
    Sign(ctx context.Context, msg []byte) (solana.Signature, error)
}
```

Two implementations:

- `SignerLocal` (`signer_local.go`), backed by an in-memory `solana.PrivateKey` loaded from `SELATPAY_HOT_WALLET_SECRET_BASE58`. Used in dev and tests.
- `SignerKMSStub` (`signer_kms_stub.go`), an explicit stub that returns `ErrNotImplemented` and documents the production drop-in. The presence of this file is the proof point: the call sites are already abstracted, and a real KMS adapter is a single file, not a refactor.

Every code path that signs (the payout flow, future Solana transaction signing for refunds or sweeps) takes the interface, never the concrete type, never the raw private key.

## Consequences

- The MVP runs without a KMS dependency.
- Production swap-in is one new file plus a config knob to pick the implementation.
- No code path holds the private key bytes outside `SignerLocal`'s constructor; the rest of the system sees only `PublicKey()` and `Sign()`.
- Failures from `Sign` (KMS unreachable, policy denial) are first-class errors that propagate up. We do not retry blindly; the caller decides whether the operation is safe to re-attempt.

## Alternatives considered

- **Use a real cloud KMS in the MVP**. Turns the build into a cloud-services demo, leaves a permanent infrastructure cost, and slows local development. Rejected.
- **Skip the interface and ship the in-memory signer directly**. Saves a file. Costs a rewrite later when the platform moves to MPC. Rejected on principle.
- **Use a third-party "hot wallet as a service" (Fireblocks, Turnkey) directly**. Reasonable in production. Not a v1 dependency.
