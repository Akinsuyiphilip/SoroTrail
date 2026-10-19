# store

## Purpose
The `store` package provides persistence layers and database abstractions for storing indexed ledgers, transactions, events, and contract states, acting as the primary persistence engine for SoroTrail.

## Entry Points & Key Abstractions
- **`Store`**: Core persistence interface defining methods for writing and querying historical chain data.
- **`PostgresStore`**: Production PostgreSQL implementation supporting transactions, connection pooling, and optimized indexing for event streams.
- **`SQLite`** and **`ClickHouse`**: alternative backends selected via `DATABASE_URL`.

## Non-Obvious Decisions & Invariants
- **Idempotency**: All write operations are designed to be fully idempotent, safely handling duplicate ingestion of ledgers or blocks during recovery or replay scenarios.
- **Cross-Links**: Refer to the architecture document for details on schema partitioning and migration strategies.
- **Honest gaps**: a backend that does not implement an operation returns an error wrapping `ErrUnsupported` instead of a zero value. A silent empty result reads as success, which is how an unimplemented feature becomes a wrong 200; `ErrUnsupported` makes the gap loud and mappable.

## Backend capability matrix
The shared conformance suite in `conformance_test.go` runs the same assertions against every registered backend. A backend is registered in `conformanceBackends`; server-backed backends are skipped cleanly when their URL environment variable is unset.

| Capability | Postgres | SQLite | ClickHouse |
| --- | --- | --- | --- |
| Events, queries, pagination | yes | yes | implemented, but declared unsupported in conformance |
| Ingestion/audit state, watched contracts | yes | yes | yes |
| Per-contract cursors | yes | yes | yes |
| Contract inventory (`ListContracts`, summary, type counts) | yes | yes | summary and type counts yes; `ListContracts` is a silent empty page |
| Contract metadata (enrichment) | yes | `ErrUnsupported` | silent empty results |
| API keys | yes | `ErrUnsupported` | plain errors (not `ErrUnsupported`) |
| Server required for tests | `TEST_DATABASE_URL` | no | `TEST_CLICKHOUSE_URL` |

SQLite is the single-node backend, but its read surface is no longer a
second-class one: contract inventory, summaries and the per-type event
breakdown are implemented against the same aggregate shape as Postgres.
Contract metadata enrichment remains Postgres-only and refuses explicitly.

ClickHouse implements the event read path, ingestion/audit state, watched
contracts, per-contract cursors, subscriptions, the dead-letter queue and the
contract summary/type-count reads, but its conformance registration above
still declares the behavioural suite unsupported wholesale: the suite
truncates between tests on shared backends, which this backend cannot do over
its HTTP interface yet, and topic-filtered event queries return
`ErrUnsupported` (the schema stores `topics` as an opaque JSON string, so
jsonb containment cannot be expressed). Those tests are therefore skipped
until a live `TEST_CLICKHOUSE_URL` server can be used to verify and narrow the
declaration.

The full ClickHouse support matrix — including the methods that silently
return zero values rather than refusing — lives in
[docs/clickhouse.md](../../docs/clickhouse.md).
