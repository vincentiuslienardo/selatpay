# ADR-0008: Travel Rule and sanctions: production design, MVP stubs

- Status: Accepted
- Date: 2026-04-22

## Context

Cross-border stablecoin settlement at any meaningful volume is a regulated activity. Two compliance surfaces dominate:

1. **Travel Rule** (FATF Recommendation 16). For transfers above a threshold (USD or EUR 1000 in most jurisdictions; lower in some, e.g. Indonesia and Singapore), the originator and beneficiary VASPs must exchange identifying information about the parties before or with the value transfer. In Asia this is implemented through TRP, Sumsub, Notabene, or Shyft; the standards are TRP (Travel Rule Protocol) and IVMS101.
2. **Sanctions and watchlist screening**. Both fiat and on-chain. Fiat screening uses standard providers (ComplyAdvantage, Refinitiv). On-chain screening uses TRM, Chainalysis, or Elliptic against OFAC SDN, EU and UN lists, and protocol-level red flags (mixers, sanctioned addresses).

An MVP cannot integrate any of this responsibly without a real legal entity, a real compliance officer, and a real list-update SLA. Pretending to is worse than not doing it at all.

## Decision

Compliance is treated as an interface boundary in v1, with the production design written down here so that the integration is a feature flag rather than a redesign.

In code:

- A `compliance.Screener` interface (planned at `internal/compliance/screener.go` for v2; the seam is established at the saga step level today) that accepts a payload describing the parties, the amount, and the on-chain addresses.
- The saga's `screen_parties` step is currently a no-op that always returns `pass`. In production it would call the screener interface and gate progression on the result.
- A `compliance.TravelRulePeer` interface that a production deployment satisfies with a TRP or Notabene client. The IVMS101 payload is constructed at intent creation from merchant KYC and payer KYC fields that v1 collects as opaque pass-through fields.

In production:

- All payouts gate on screening result. A `pass` advances the saga; a `manual_review` pauses the saga (the `saga_runs` row stays in a holdable state with no `next_run_at`); a `block` short-circuits to `failed` and triggers a refund flow.
- A separate `compliance.officer` console (out of scope for v1) consumes manual-review queues. Reviewer decisions write back through the same interface so that the saga can resume.
- Address screening is run both at intent creation (originator address from the wallet that will fund the intent, if known) and at deposit detection (beneficiary side and the actual sending address from the on-chain transaction).
- All screening results are stored alongside the intent for audit. Screening responses are immutable; a re-screen on the same payload is a new row, not an update.

The Indonesian regulator (BI and Bappebti) requirements are stricter for IDR off-ramps than for crypto-to-crypto. The mock IDR bank rail used in v1 stands in for a real Indonesian payment service provider that would carry its own compliance obligations on the fiat side.

## Consequences

- v1 has no real compliance enforcement and is therefore not safe to operate against real customer money. The README and demo materials are explicit about this.
- The shape of the saga, the data model, and the interface boundaries are compatible with a production compliance layer with no architectural changes. Adding compliance is a code change inside the screening step, not a redesign.
- Manual-review semantics are the trickiest part: the saga must be pausable from outside, which is why the lease and `next_run_at` fields exist on `saga_runs`. The same machinery that handles "retry after backoff" handles "wait until human says go".

## Alternatives considered

- **Implement a real screener against open lists in v1**. Possible (OFAC SDN is publicly downloadable, OFAC consolidated list is too) but creates the false impression that v1 is compliance-ready. Worse than an honest stub.
- **Skip the seam entirely**. Forces a rewrite when compliance lands. The marginal cost of the seam today is one interface and a no-op saga step, which is trivial.
