# Changelog

Release notes are **generated from the merged PRs** by `gh release create
--generate-notes`, so the authoritative, per-version list lives on the
[releases page](https://github.com/wistefan/consent-owner-resolver/releases).

This file records the things a generated list cannot: changes that alter the
integration contract with the [consent-plugin](https://github.com/wistefan/consent-plugin)
or the config file format, and the reasoning behind them. Add an entry under
"Unreleased" in the same PR that makes such a change.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
— see [CONTRIBUTING.md](CONTRIBUTING.md) for which label to apply.

## Unreleased

Nothing released yet; this is the pre-`0.1.0` state of the initial
implementation.

### Response contract

- `claims` is always a list, never `null` — the Lua plugin's `cjson` decodes
  `null` to a value that cannot be iterated.
- A payload the resolver cannot decode answers `400`; an owner it cannot
  determine answers `422`. The plugin keys its fail policy off the split.

### Configuration

- `contractService.resourceCacheTtlMs` (default `30000`, negative disables)
  caches a contract's catalog data resources across requests.
- A `static` rule requires a non-empty `owner`, and a `contract` rule requires
  `consentRequired: true`. Both combinations were previously accepted and
  silently produced an ungated answer.
