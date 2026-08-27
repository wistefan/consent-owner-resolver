# Security Policy

## Reporting a vulnerability

**Do not open a public issue.** This service decides whether personal data is
gated behind a consent check, so a flaw in it is a data-protection problem
before it is a software problem.

Report privately through GitHub:
[**Report a vulnerability**](https://github.com/wistefan/consent-owner-resolver/security/advisories/new).
That opens a private advisory visible only to the maintainers.

Please include what you would want to receive: the version or commit, the
configuration that triggers it, and the smallest request that reproduces it. If
you are unsure whether something counts, report it — an over-reported
false positive costs one reply.

## What counts

The things worth reporting here, in rough order of severity:

- anything that makes `/resolve` answer `consentRequired: false`, or return no
  claims, for data that should have been gated — this fails **open**;
- anything that lets the **requestor** influence the resolved `ownerId`
  (ownership must come from the data alone);
- owner identifiers leaking somewhere they should not be — logs, error bodies,
  a response for a payload the caller did not supply;
- a contract in a non-signed state being honoured as governing;
- request handling that lets a caller exhaust the service or the consent-facade
  behind it.

## What is out of scope

- **`/resolve` being reachable without credentials.** It is designed to run
  cluster-internal, restricted to the consent-plugin; see "Deployment" in the
  [README](README.md). A deployment that exposes it publicly is a
  misconfiguration, not a vulnerability in this code.
- Findings from a scanner with no reachable call path — `govulncheck` and
  `gosec` block the build, so anything they would catch is already gated.

## Supported versions

Only the latest release. The service has no dependencies beyond the Go standard
library, so "patched" in practice means rebuilt on a current Go toolchain, which
CI enforces on every merge.
