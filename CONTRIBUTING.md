# Contributing

Thanks for taking a look. Selatpay is a single-author project at the moment, so this guide is short. The conventions here are non-negotiable for merged work; treat them as part of the code review checklist.

## Local setup

```bash
cp .env.example .env
make up                  # postgres, redis, jaeger, solana-test-validator, mock-bank
make build
make test                # short unit tests
make integration         # full integration tests against compose services
make lint                # golangci-lint v2
```

`make help` lists every target.

## Commit conventions

Commits follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <subject>

<body>
```

Required:

- A `type` from the standard set: `feat`, `fix`, `refactor`, `test`, `docs`, `build`, `ci`, `chore`.
- A `scope` in parentheses where it adds clarity (`feat(saga): ...`).
- A subject in imperative mood, lowercased, no trailing period.
- A body explaining the why, wrapped at ~72 columns. Even one-line changes get a short body.

Type usage:

| Type | When |
| --- | --- |
| `feat` | New code-level functionality |
| `fix` | A bug fix |
| `refactor` | Code rearrangement that does not change behavior |
| `test` | Adding or restructuring tests, no production-code change |
| `docs` | README, ADR, OpenAPI, comments |
| `build` | Makefile, Dockerfiles, compose, scripts that build or run the project |
| `ci` | GitHub Actions, lint configuration |
| `chore` | True housekeeping (dep bumps, license, gitignore) |

Split work into focused commits. One commit per concept is the goal; one commit per phase is too coarse.

## Code style

- `gofmt -s` clean. Run `make fmt` before pushing.
- `golangci-lint run` clean. Run `make lint` before pushing.
- No new global state. Pass dependencies explicitly through constructors.
- No `panic` outside `main` and tests. Return errors.
- `slog` for logging. Structured fields, not `fmt.Sprintf` into the message.
- Comments explain why, not what. Skip them when the code is self-evident.
- Avoid em dashes, en dashes, arrows, and middle dots in prose.

## Tests

- Unit tests live next to the code they exercise (`foo.go`, `foo_test.go`). Build tag `integration` for integration tests so unit runs stay fast.
- Integration tests use real Postgres (`testcontainers-go`) and the local `solana-test-validator`. They are slower; run them before opening a PR but you do not need them in the inner loop.
- Test names describe the behavior being verified (`TestSagaRetriesTransientStepFailures`). Avoid test names that just repeat the function name.

## Migrations

- New schema goes through `internal/db/migrations/NNNN_short_name.sql`. Numbering is sequential.
- Both Up and Down sections are required. Down must actually undo Up so a downgrade does not leave artifacts.
- Run `make migrate` against your local stack to verify.
- Regenerate sqlc bindings (`make sqlc`) and check the generated code into the same commit as the migration.

## Adding an ADR

When you make a decision that is hard to reverse or non-obvious, write an ADR. New ADRs go in `docs/adr/NNNN-kebab-case-title.md` and follow the existing template (Status, Context, Decision, Consequences, Alternatives considered). Add a row to `docs/adr/README.md`.

ADRs are immutable once accepted. If a decision changes, write a new ADR that supersedes the old one and reference the old one in its body.

## Pull requests

- Branch naming: `<type>/<short-slug>` (`feat/payout-rate-limit`).
- A PR description states what changed and why, links any relevant ADR, and notes any operational impact (new env var, new migration, new dependency).
- CI must be green. No merging through red CI.
- Squash-merge or rebase-merge; do not merge with a merge commit.
