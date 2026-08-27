# Contributing

Thanks for taking the time. This document is what `check.yml` links to when a PR
fails its label check, so the semver labels come first.

## Every PR needs exactly one semver label

`patch`, `minor` or `major`. The merge to `main` reads that label and releases
the corresponding version — no label means **no release**, more than one means
the check fails.

| label | use it when | 0.4.2 becomes |
|---|---|---|
| `patch` | bug fix, docs, CI, dependency bump — no behaviour change for callers | `0.4.3` |
| `minor` | new matcher, new config field, new response field — backwards compatible | `0.5.0` |
| `major` | anything a deployed plugin or an existing config file would break on | `1.0.0` |

"Backwards compatible" is judged against the two things this service is a
contract with: the **`/resolve` request/response shape** (consumed by the
separately-released [consent-plugin](https://github.com/wistefan/consent-plugin))
and the **config file format**. A change that makes an existing config fail to
load is `major`, however small the diff.

You can see what a label would release before pushing:

```sh
printf 'patch\n' | ./hack/semver-bump.sh   # validates the label
./hack/next-version.sh patch               # prints the version it would cut
```

## Before you open the PR

```sh
make license-check   # Apache-2.0 header on every Go file (CI blocks on this)
make lint            # golangci-lint, .golangci.yml
make test            # go test -race ./...
make security        # govulncheck + gosec — both block in CI
```

All four run in CI on every PR (`pr.yml`), so running them locally only saves a
round trip.

## Code rules

These are enforced, not aspirational:

- **Every exported type and method carries a doc comment.** `revive`'s
  `exported` and `package-comments` rules fail the build otherwise.
- **Every Go file carries the Apache-2.0 header.** The canonical text is
  [`hack/license-header.txt`](hack/license-header.txt) — edit it there and
  nowhere else; `make license-fix` applies it.
- **No magic constants.** Name them. `goconst` catches the repeated ones.
- **Prefer parameterized (table-driven) tests.** Most existing tests are; follow
  the pattern rather than copying a case.

## Two rules specific to this service

The resolver decides whether personal data is gated, which makes two habits
non-negotiable:

1. **Ownership comes from the data, never from the requestor.** `Parties` exists
   only to identify the governing contract. If a change lets a request field
   influence an `ownerId`, it is wrong regardless of how convenient it is.
2. **Fail closed.** When the owner cannot be determined, return an error — the
   plugin then denies. A response must never silently mean "no consent needed".
   New code paths that can return zero claims need an explicit reason, written
   down.

## Security issues

Do not open a public issue — see [SECURITY.md](SECURITY.md).
