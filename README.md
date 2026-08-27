# consent-owner-resolver

An **OwnerResolver** service for the [consent-plugin](https://github.com/wistefan/consent-plugin).

The consent-plugin must decide, for data flowing through it, whether that data
needs a consent check and **whose** data it is — **independently of the
requestor**. It delegates exactly that to this service, so the plugin stays
generic and the (provider-/dataset-specific) ownership logic lives here.

The resolver answers two questions about a payload:

- **(a)** does this data require a consent check? → `consentRequired`
- **(b)** who is the data owner (subject)? → `claims[].ownerId`

The decision unit is **(owner × dataResource)**: a single owner may have
consented to some data objects but not others, so every claim carries both an
owner and the resource it concerns. The request carries **no caller identity** —
that independence is structural.

The resolver is **data-format agnostic**: it handles structured JSON (owner read
from a field), and opaque payloads such as a single file (owner derived from the
request path, with the whole payload as one claim — the body need not even be
sent).

## HTTP API

### `POST /resolve`

Request — the data and its provenance, never the requestor:

```jsonc
{
  "resource": {
    "service":     "mp-data-service",     // logical dataset id (set on the plugin route)
    "method":      "GET",
    "path":        "/ngsi-ld/v1/entities/urn:ngsi-ld:PersonalProfile:alice",
    "contentType": "application/ld+json"
  },
  "parties": {                            // optional; identifies the CONTRACT, never the owner
    "consumer": "did:web:fancy-marketplace.biz",       // the requesting participant
    "provider": "https://…/participants/urn:…:prov"    // optional override of the configured provider SD
  },
  "body": {                               // optional
    "encoding": "json",                   // json | base64 | none
    "content":  { /* payload, per encoding */ }
  }
}
```

- `encoding: "none"` (or no `body`) → the resolver decides from `resource` alone
  (e.g. large or opaque files identified by their path).
- `encoding: "base64"` → `content` is a JSON string of base64 bytes; decoded to
  JSON when it happens to be JSON, otherwise treated as opaque.
- `parties` exists **only** so the governing contract can be identified (a
  contract is provider↔consumer by definition). It is **never** used to
  determine the owner — that always comes from the data. Only the `contract`
  matcher reads it, and for that matcher `parties.consumer` is **required**: a
  request without it fails `422`.

Response:

```jsonc
{
  "consentRequired": true,
  "scheme": "identifier",                 // how to interpret ownerId: identifier | email | did
  "claims": [
    {
      "selector":     { "type": "json-pointer", "value": "/0" },  // or { "type": "whole" }
      "ownerId":      "alice-42",
      "participant":  "https://…/provider-sd",                    // optional lookup scope
      "dataResource": "urn:dsc:resource:personal-profile"
    }
  ]
}
```

- `dataResource` is optional. In **v1** it is the **id of the requested entity**
  (set `resourcePointer: "/id"`, or a `(?P<resource>…)` path group), so consent is
  checked per entity. Omit it entirely for owner-level consent.
- `selector.type`: `whole` (opaque / single object — not redactable) or
  `json-pointer` (an RFC6901 pointer; enables v2 field-level filtering).
- On success → `200`. On a payload the resolver cannot decode → `400`. When
  consent may be required but the owner cannot be determined → `422` (the plugin
  then applies its fail policy — deny by default). A resolver response never
  silently means "no consent needed".

### `GET /health` → `200 {"status":"ok"}`

## Configuration

JSON, mounted at `CONFIG_PATH` (default `/etc/owner-resolver/config.json`).
Rules are evaluated top-down; the first whose `match` matches wins.

```jsonc
{
  "defaultConsentRequired": false,   // returned when no rule matches (set true to fail closed)
  "defaultScheme": "identifier",     // identifier | email | did
  "contractService": {               // required only when a `contract` matcher is used
    "url":                     "http://consent-facade:8080",
    "providerSelfDescription": "https://…/participants/urn:ngsi-ld:organization:prov",
    "timeoutMs":               3000,   // per facade call (default 3000)
    "resourceCacheTtlMs":      30000   // catalog cache (default 30000; negative disables)
  },
  "rules": [
    {
      "name": "ngsi-personal-profiles",
      "match": { "service": "mp-data-service", "pathPattern": "" },  // both optional
      "consentRequired": true,
      "matcher": { "type": "json", "items": "", "itemsIsArray": false,
                   "owner": "/dataOwner", "resource": "urn:dsc:resource:personal-profile" }
    },
    {
      "name": "opaque-files",
      "match": { "service": "file-service" },
      "consentRequired": true,
      "matcher": { "type": "path", "pattern": "^/files/(?P<owner>[^/]+)/(?P<resource>.+)$" }
    },
    {
      "name": "ngsi-from-contract",
      "match": { "service": "mp-data-service" },
      "consentRequired": true,
      "matcher": { "type": "contract", "items": "", "itemsIsArray": false,
                   "owner": "/dataOwner/value", "uriPointer": "/id" }
    }
  ]
}
```

### `contractService`

Points at a **consent-facade** (a provider-local instance) and is required as
soon as any rule uses the `contract` matcher; `Parse` fails without `url`.

| field | meaning |
|---|---|
| `url` | facade base url, e.g. `http://consent-facade:8080`. **Required.** |
| `providerSelfDescription` | this provider's participant SD url — one side of every contract lookup. May be omitted and supplied per request as `parties.provider`; with neither, a `contract` rule fails `422`. |
| `timeoutMs` | bounds each facade call. Default `3000`. |
| `resourceCacheTtlMs` | how long a contract's catalog data resources are reused across requests. Default `30000`; negative disables the cache. Contracts themselves are never cached — they carry the signature state. |

### Matchers

| type | data | how it finds the owner | selector emitted |
|---|---|---|---|
| `json` | structured JSON | `owner` = RFC6901 pointer within each item; `items`+`itemsIsArray` iterate a collection (multi-subject); `resource` fixed or `resourcePointer` | `json-pointer` |
| `path` | anything (incl. opaque files) | regexp `pattern` with named groups `(?P<owner>…)` and optional `(?P<resource>…)`; needs no body | `whole` |
| `static` | any | fixed `owner`/`resource` (tests, always-gated routes) | `whole` |
| `contract` | structured JSON | `owner` = RFC6901 pointer within each item (as `json`); the `dataResource` comes from the **governing contract** instead of config — see below | `json-pointer` |

#### The `contract` matcher

The owner still comes from the data; what the contract adds is **which data
resource** the claim is about, in the catalog's own vocabulary.

1. `parties.consumer` (required) plus the provider SD identify the **signed**
   contracts at the facade — only `status: "signed"` counts.
2. The requested object's URI (read from `uriPointer` within each item) is
   matched against each contract policy's ODRL `assetTarget`. A policy that
   **prohibits** that URI never governs it, even if it also permits it.
3. The governing contract's service offering is dereferenced, and its first
   catalog resource with `containsPII: true` becomes the claim's
   `dataResource`. A contract that declares no PII resource yields an
   owner-level claim (no `dataResource`) rather than silently allowing.

| option | meaning |
|---|---|
| `owner` | RFC6901 pointer to the owner within each item. **Required.** |
| `uriPointer` | RFC6901 pointer to the requested object's URI within each item. Default `/id` (NGSI-LD and most JSON-LD payloads). |
| `items` | RFC6901 pointer to the collection/item root; `""` = the whole body. |
| `itemsIsArray` | `true` → each element of `items` yields its own claim. |
| `participant` | optional lookup scope, copied onto every claim. |

`consentRequired` is still the rule's own flag: `containsPII` selects *which*
resource the claim names, it does not decide *whether* consent is checked.

Every failure here is fail-closed: no consumer, no provider, no signed
contract, no contract targeting the object, or no owner in the data all produce
`422`.

`dataResource` values MUST use the same vocabulary the privacy notice / consent
are expressed in (`Consent.data[].resource`) — that shared taxonomy is the real
integration contract.

## Run

```sh
make build && CONFIG_PATH=config/example.json ./owner-resolver   # :8080
make test                                                        # unit tests
make lint                                                        # golangci-lint, .golangci.yml
make security                                                    # govulncheck + gosec (blocking in CI)
make docker-build                                                # quay.io/wi_stefan/consent-owner-resolver:0.0.1
```

Requires Go 1.26.7+ (the version `go.mod` pins). The runtime image is
`gcr.io/distroless/static-debian12:nonroot` — no shell, no package manager, and
nothing to patch beyond the binary itself.

Env: `CONFIG_PATH`, `LISTEN_ADDR` (default `:8080`), `MAX_BODY_BYTES` (default 5 MiB),
`AUTH_TOKEN` (see below).

## Deployment: this service is cluster-internal

**`/resolve` must not be reachable from outside the cluster.** It answers *who
owns this data*, so an open port is an owner-identifier oracle: a caller can
probe which services and paths are gated, harvest owner ids (a `path`-matcher
route returns `ownerId` from the path with no body at all), and enumerate which
contracts exist between arbitrary provider/consumer pairs by varying `parties`.

Restrict it to the plugin with a NetworkPolicy:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: owner-resolver-plugin-only
spec:
  podSelector:
    matchLabels: { app: owner-resolver }
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchLabels: { app: apisix }   # the consent-plugin's pod
      ports:
        - protocol: TCP
          port: 8080
```

Where the plugin and the resolver do not share a trust boundary, set
`AUTH_TOKEN` on the resolver and have the plugin send
`Authorization: Bearer <token>`; requests without it get `401`. `GET /health`
stays unauthenticated so liveness probes keep working. mTLS between the two (a
service mesh, or a sidecar) is the stronger option where one is available.

## Where it fits

```
consumer ─▶ APISIX ─▶ upstream ─▶ [consent-plugin] ──POST /resolve──▶ [owner-resolver]
                                        │  ◀── consentRequired, claims[] (owner × resource) ──┘
                                        └── per (owner × dataResource): consent check ─▶ consent-manager
```

This repo is only the resolver. The plugin-side integration (calling `/resolve`,
then checking consent per resolved owner) and the consent-manager changes are
tracked separately.

## Copyright headers

Every Go source file carries the Apache-2.0 copyright header. The canonical text lives in
[`hack/license-header.txt`](hack/license-header.txt) - edit it there and nowhere else.

```shell
make license-check   # verify (what CI runs)
make license-fix     # add the header to files that lack it
```

CI enforces this on every pull request and on every push to `main`
(`.github/workflows/license-headers.yml`), so a new file without the header fails the build. The
check covers `*.go` only: the header is a `/* */` block, which is not valid comment syntax in the
Dockerfile or the Makefile.
