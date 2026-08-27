# Workflows

Modelled on [FIWARE/VCVerifier](https://github.com/FIWARE/VCVerifier/tree/main/.github/workflows):
the individual checks are **reusable** workflows (`on: workflow_call`) that two entry points compose,
so a check is defined once and runs identically on a PR and on `main`.

## Entry points

| Workflow | Trigger | Does |
|---|---|---|
| `pr.yml` | PR to `main` | license-headers, style-guide, build, tests, security-analysis |
| `main.yml` | push to `main` | the same checks, then `release.yml` |
| `check.yml` | PR labelled/synchronized | enforces exactly one semver label (`patch`/`minor`/`major`), comments if missing |
| `pre-release.yml` | PR to `main` (**same-repo branches only**) | publishes a `<next>-PRE-<pr>` image + pre-release |
| `stale-issues.yml` | daily cron | closes stale issues/PRs |

## Reusable checks

| Workflow | Does |
|---|---|
| `license-headers.yml` | `hack/license-header.sh check` - the Apache-2.0 header on every Go file |
| `style-guide.yml` | `golangci-lint` (pinned) using the repo's `.golangci.yml` - same config as `make lint` |
| `build.yml` | `go build` |
| `tests.yml` | `go test -race` + coverage summary/artifact (Coveralls upload is non-blocking) |
| `security-analysis.yml` | `govulncheck` + `gosec` (both **blocking**), SARIF uploaded to code scanning |
| `release.yml` | version from the merged PR's semver label → image (multi-arch, Trivy-scanned) + binaries + GitHub release |

## Third-party actions

Every `uses:` is pinned to a **commit SHA** with the tag in a trailing comment; a mutable tag on a
third-party action is a supply-chain hole, and `@latest` on one is worse. `.github/dependabot.yml`
keeps the pins current (it rewrites SHA and comment together).

The semver plumbing is plain shell rather than third-party actions, because the ones previously used
are archived:

| was | now |
|---|---|
| `zwaldowski/match-label-action` | `hack/semver-bump.sh` |
| `zwaldowski/semver-release-action` | `hack/next-version.sh` |
| `actions-ecosystem/action-get-merged-pull-request` | `gh api .../commits/$SHA/pulls` |
| `marvinpinto/action-automatic-releases@latest` | `gh release create` |

Both scripts run locally: `printf 'patch\n' | ./hack/semver-bump.sh` and
`./hack/next-version.sh patch`.

## Where the headers are enforced

`license-headers.yml` runs in **three** places, so an unheadered file cannot reach a published
artifact: on every PR (`pr.yml`), on every push to `main` (`main.yml`, where `release` also `needs`
it), and as the first job of `pre-release.yml`.

## Required repository configuration

* Secrets `QUAY_USERNAME` / `QUAY_PASSWORD` - image push. Without them `pr.yml` still passes;
  the release will fail at the login step. `pre-release.yml` is skipped entirely for fork PRs (they
  get neither the secrets nor `contents: write`), so an external contributor sees a green pipeline
  rather than a broken one they cannot fix.
* Labels `patch`, `minor`, `major` must exist.
* Code scanning must be enabled for the SARIF uploads (`security-events: write` is granted by the
  callers).
* Coveralls is optional - the upload is `continue-on-error`.
* `main.yml` must grant `contents: write` and `pull-requests: read`: a called workflow's token can
  never be wider than its caller's, so without them the release is skipped silently.
