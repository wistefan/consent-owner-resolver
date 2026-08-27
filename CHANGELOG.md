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

### API contract

- `api/openapi.json` is the machine-readable contract for `/resolve`, `/health`
  and `/metrics`. `internal/api/openapi_test.go` drives the real handler and
  fails on any response property the spec does not document, any required
  property missing, any enum value outside the documented set, and any status
  code the spec does not list.

### Response contract

- `claims` is always a list, never `null` — the Lua plugin's `cjson` decodes
  `null` to a value that cannot be iterated.
- A payload the resolver cannot decode answers `400`; an owner it cannot
  determine answers `422`. The plugin keys its fail policy off the split.
- Unknown fields in a `/resolve` request are ignored rather than rejected, so
  the plugin can add one without waiting for a resolver deployment.
- Error bodies are generic (`{"error":"cannot resolve owner"}`); the detail
  names the deployment's pointer configuration and goes to the log instead.

### Facade wire contract

- Participant self-description URLs in `/verify/{provider}/{consumer}` path
  parameters are encoded with **unpadded base64url** (RFC 4648 §5), not the
  standard alphabet. The standard alphabet can emit `/` and `=`, which a proxy
  may percent-decode or normalize; base64url emits nothing that needs escaping.
  **The consent-facade must decode with the URL-safe alphabet.**
  `TestEncodeParticipant` pins the exact strings.

### Observability

- `GET /metrics` exposes request counts, a latency histogram and resolve
  failures by error class, in Prometheus text format. Stdlib only — no client
  library, no new dependency.
- `X-Request-Id` is accepted, minted when absent, echoed on every response and
  logged with each failure.

### Configuration

- `contractService.resourceCacheTtlMs` (default `30000`, negative disables)
  caches a contract's catalog data resources across requests.
- A `static` rule requires a non-empty `owner`, and a `contract` rule requires
  `consentRequired: true`. Both combinations were previously accepted and
  silently produced an ungated answer.
