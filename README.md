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
  "defaultScheme": "identifier",
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
    }
  ]
}
```

### Matchers

| type | data | how it finds the owner | selector emitted |
|---|---|---|---|
| `json` | structured JSON | `owner` = RFC6901 pointer within each item; `items`+`itemsIsArray` iterate a collection (multi-subject); `resource` fixed or `resourcePointer` | `json-pointer` |
| `path` | anything (incl. opaque files) | regexp `pattern` with named groups `(?P<owner>…)` and optional `(?P<resource>…)`; needs no body | `whole` |
| `static` | any | fixed `owner`/`resource` (tests, always-gated routes) | `whole` |

`dataResource` values MUST use the same vocabulary the privacy notice / consent
are expressed in (`Consent.data[].resource`) — that shared taxonomy is the real
integration contract.

## Run

```sh
make build && CONFIG_PATH=config/example.json ./owner-resolver   # :8080
make test                                                        # unit tests
make docker-build                                                # quay.io/wi_stefan/consent-owner-resolver:0.0.1
```

Env: `CONFIG_PATH`, `LISTEN_ADDR` (default `:8080`), `MAX_BODY_BYTES` (default 5 MiB).

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
