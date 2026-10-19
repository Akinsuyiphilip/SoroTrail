# Contract Spec Overrides

When `?decoded=true` is requested on `/events`, `/events/{id}`, `/events/{id}/transaction`, or the GraphQL `tokenEvents` query, SoroTrail's `spec.Enricher` maps raw positional topics (`topics[1..n]`) and `value` onto named fields using the contract's event specification.

Normally the enricher fetches that specification automatically from the chain by reading the contract instance's `wasm_hash`, fetching the Wasm bytecode via `getLedgerEntries`, and parsing its `contractspecv0` custom section. When that automatic path cannot produce a usable spec, an operator can upload a **contract spec override** via the HTTP API (`PUT /contracts/{id}/spec`).

- [When an override is needed](#when-an-override-is-needed)
- [Authentication](#authentication)
- [Endpoints](#endpoints)
- [Accepted JSON format](#accepted-json-format)
- [Validation rules and error messages](#validation-rules-and-error-messages)
- [Wrapper vs. bare request body](#wrapper-vs-bare-request-body)
- [Precedence and contract upgrades](#precedence-and-contract-upgrades)
- [Caching and propagation timing](#caching-and-propagation-timing)

---

## When an override is needed

Use a contract spec override when:

1. **No `contractspecv0` section in the Wasm binary**: custom or hand-rolled Soroban contracts, older contracts compiled without spec metadata, or stripped Wasm binaries do not embed `SC_SPEC_ENTRY_EVENT_V0` entries.
2. **Unreachable or archived Wasm**: the contract's code entry has expired or is unavailable on the configured RPC provider, so `Fetcher.FetchSpec` cannot retrieve the bytecode.
3. **Correcting or augmenting event field names**: a deployed contract emits events whose on-chain spec is missing or mis-typed, and you want `?decoded=true` to surface clean field names without redeploying the contract.

---

## Authentication

All three `/contracts/{id}/spec` routes (`PUT`, `GET`, `DELETE`) are gated by the operator `API_KEY` middleware (`apiKeyAuth`), identical to `/watched-contracts`:

- Supply the key in `Authorization: Bearer <API_KEY>` or `X-API-Key: <API_KEY>`.
- If `API_KEY` is not configured in the server environment (empty), every request to `/contracts/{id}/spec` fails closed with `503 Service Unavailable` (`{"error": "API_KEY not configured"}`).
- If the header is missing or does not match `API_KEY` (constant-time comparison), the server responds `401 Unauthorized` (`{"error": "invalid or missing API key"}`).

Every response from `/contracts/{id}/spec` carries `Cache-Control: no-store`.

---

## Endpoints

| Method | Path | Success Status | Purpose |
| --- | --- | --- | --- |
| `PUT` | `/contracts/{id}/spec` | `200 OK` | Validate and store (or replace) the JSON spec override for contract `{id}`. |
| `GET` | `/contracts/{id}/spec` | `200 OK` | Return the stored spec override for contract `{id}`, or `404 Not Found` (`{"error": "no spec override for contract \"C...\""}`) when none is set. |
| `DELETE` | `/contracts/{id}/spec` | `200 OK` | Remove the stored spec override for contract `{id}`. Idempotent: deleting an absent override still returns `200 OK` with `{"contract_id": "C...", "deleted": true}`. |

`{id}` must be a valid 56-character Stellar contract strkey (`C...`); otherwise the handler returns `400 Bad Request` (`invalid contract id "..." (want 56-char C... strkey)`).

### End-to-end example

Upload a spec override for a contract:

```sh
curl -s -X PUT http://localhost:8080/contracts/CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC/spec \
  -H "X-API-Key: $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "spec": {
      "events": [
        {
          "name": "transfer",
          "doc": "Token transfer event",
          "topic_specs": [
            {"name": "from", "type": "address"},
            {"name": "to", "type": "address"}
          ],
          "value_spec": {"name": "amount", "type": "i128"}
        },
        {
          "name": "mint",
          "topic_specs": [
            {"name": "to", "type": "address"}
          ],
          "value_spec": {"name": "amount", "type": "i128"}
        }
      ]
    }
  }'
```

Response (`200 OK`):

```json
{
  "contract_id": "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
  "spec": {
    "events": [
      {
        "name": "transfer",
        "doc": "Token transfer event",
        "topic_specs": [
          {"name": "from", "type": "address"},
          {"name": "to", "type": "address"}
        ],
        "value_spec": {"name": "amount", "type": "i128"}
      },
      {
        "name": "mint",
        "topic_specs": [
          {"name": "to", "type": "address"}
        ],
        "value_spec": {"name": "amount", "type": "i128"}
      }
    ]
  }
}
```

Inspect the stored override:

```sh
curl -s http://localhost:8080/contracts/CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC/spec \
  -H "X-API-Key: $API_KEY"
```

Query decoded events using the override:

```sh
curl -s 'http://localhost:8080/events?contract_id=CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC&decoded=true&limit=1'
```

Remove the override so the enricher falls back to the on-chain spec:

```sh
curl -s -X DELETE http://localhost:8080/contracts/CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC/spec \
  -H "X-API-Key: $API_KEY"
```

---

## Accepted JSON format

`spec.ParseOverrideSpec` (`internal/spec/override.go`) validates the inner `"spec"` object before anything is written to the database. The schema mirrors `spec.ContractSpec`:

```json
{
  "events": [
    {
      "name": "transfer",
      "doc": "Optional human-readable documentation",
      "topic_specs": [
        {"name": "from", "type": "address"},
        {"name": "to", "type": "address"}
      ],
      "value_spec": {"name": "amount", "type": "i128"}
    }
  ]
}
```

How the enricher maps an event against an `EventSpec`:

- **`topics[0]`** must be a symbol (for example `{"symbol": "transfer"}`) and is matched against `events[].name`.
- **`topics[1..n]`** are mapped positionally to `topic_specs[0..n-1]`: `topics[1]` maps to `topic_specs[0]`, `topics[2]` maps to `topic_specs[1]`, and so on.
- **`value`** (when non-null and `value_spec` is non-nil) is mapped to `value_spec.name`.
- All decoded scalar values are formatted as strings in `decoded_event.fields`.

---

## Validation rules and error messages

Invalid payloads are rejected synchronously with `400 Bad Request` before calling the store:

| Condition | HTTP Status | Error message |
| --- | --- | --- |
| Invalid contract ID in URL | `400` | `invalid contract id "..." (want 56-char C... strkey)` |
| Empty request body | `400` | `invalid JSON body: request body is empty` |
| Missing or empty `"spec"` field (`{}`) | `400` | `request body must contain a "spec" object` |
| `"spec"` is not a JSON object | `400` | `parsing override spec JSON: ...` |
| `"events"` key omitted or `null` (`{"spec": {}}`) | `400` | `override spec: "events" array is required` (an empty array `"events": []` is valid) |
| Event entry has empty or missing `"name"` | `400` | `override spec: event #<N>: "name" is required and must be a non-empty string` |
| Two events share the same `"name"` | `400` | `override spec: duplicate event name "<name>"` |
| A `topic_specs[i]` entry has empty `"name"` or `"type"` | `400` | `override spec: event "<name>": topic_specs[<i>]: "name" (or "type") is required and must be a non-empty string` |
| `value_spec` has empty `"name"` or `"type"` | `400` | `override spec: event "<name>": value_spec: "name" (or "type") is required and must be a non-empty string` |

---

## Wrapper vs. bare request body

Always send the **wrapped** form:

```json
{
  "spec": {
    "events": [...]
  }
}
```

> **Known bug with bare spec payloads ([#1129](https://github.com/sorotrail/SoroTrail/issues/1129)):**
> A comment in `handlePutContractSpecOverride` notes an intended fallback for a bare spec object (`{"events": [...]}` without the outer `"spec"` wrapper). However, the first `decodeJSONBody(r, &body)` call drains `r.Body` and enables `DisallowUnknownFields()`, which rejects `"events"` on the wrapper struct, and the fallback `decodeJSONBody(r, &bare)` then reads an already-exhausted `r.Body` and fails with `400 Bad Request` (`invalid JSON body: json: unknown field "events"`). Until #1129 is fixed, only the `{"spec": {...}}` wrapper form succeeds.

---

## Precedence and contract upgrades

In `Enricher.getSpec` (`internal/spec/enricher.go`), lookup order is:

1. **User-supplied spec override (`getOverride`)** — checked first on every lookup.
2. **Wasm-hash-keyed spec cache (`cache.GetForContract`)** — which periodically re-verifies the contract's on-chain `wasm_hash` (every `DefaultHashTTL` = 1 minute) and invalidates the cached on-chain spec when the contract upgrades.
3. **RPC spec fetcher (`fetcher.FetchSpec`)** — lazy fetch from Stellar RPC on cache miss.

Because step 1 runs before step 2 and is keyed solely by `contract_id` (`"override:" + contractID`), **an active override always takes precedence over the on-chain spec — even after the contract upgrades to a new `wasm_hash`**.

- **When this is desirable**: the upgraded contract still omits `contractspecv0` or keeps the same event ABI.
- **When this is a hazard**: the contract upgrade changed event topics or value types, or added `contractspecv0` support, while the stale manual override remains in place.
- **How to notice**: events whose new shape cannot be decoded against the old override return `"decoded": false` with a `"decode_error"` string on `/events?decoded=true`, and increment `decode.decode_failures` in `GET /stats`. After upgrading a contract's event ABI, either `PUT /contracts/{id}/spec` with the updated spec or `DELETE /contracts/{id}/spec` to return to on-chain spec fetching.

---

## Caching and propagation timing

When `getOverride` loads an override from the database on a cache miss, it stores the parsed `*ContractSpec` in the in-memory `spec.Cache` under the key `"override:<contract_id>"` via `cache.Put`.

Verify the current caching behaviour against the code (`internal/spec/enricher.go` and `internal/spec/cache.go`):

- **First upload (no prior override cached in memory)**: takes effect **immediately** on the next `?decoded=true` request, because `getOverride` misses in memory (`e.cache.Get("override:" + contractID)` returns `nil`) and reads the newly stored row from Postgres/SQLite.
- **Replacing or deleting an already-cached override on a single instance (known bug [#1142](https://github.com/sorotrail/SoroTrail/issues/1142))**: `PUT /contracts/{id}/spec` and `DELETE /contracts/{id}/spec` write to the store but do not currently evict `"override:" + contractID` from `spec.Cache`. Although a comment in `getOverride` states that in-memory overrides never expire, `e.cache.Get(key)` calls `Cache.get`, which enforces `DefaultSpecTTL` (`10m`). As a result, replacing or deleting an override that has already been served takes up to **`DefaultSpecTTL` (10 minutes)** (or a process restart) to take effect in the handling process until #1142 is resolved.
- **Multi-replica deployments (known issue [#1167](https://github.com/sorotrail/SoroTrail/issues/1167))**: `spec.Cache` is per-process in memory with no cross-replica invalidation channel yet. Even once local eviction is wired, other API replicas that have already cached `"override:<contract_id>"` will continue serving their in-memory entry until their local `DefaultSpecTTL` (10 minutes) expires.
- **ClickHouse backend caveat**: as documented in [ClickHouse backend support](clickhouse.md#contract-inventory-and-metadata), `GetContractSpecOverride`, `SetContractSpecOverride`, and `DeleteContractSpecOverride` are silent no-ops on ClickHouse (`GET` returns `404`, `PUT`/`DELETE` return `200` without persisting). Spec overrides require Postgres or SQLite.
