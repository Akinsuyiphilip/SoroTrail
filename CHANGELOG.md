# Changelog

## [Unreleased]

### Added

- Every entry in the README's configuration table now carries a Reload column: `SIGHUP` for the two values the running indexer re-reads and applies live (`POLL_INTERVAL`, `LOG_LEVEL`), `restart` for the hundred values read exactly once at startup. `internal/config` pins the classification against the real reload path in `cmd/sorotrail` — the set `applyReload` applies must equal the reloadable set, and the set `warnIgnoredTopologyChanges` announces must equal the restart-required topology set — and against the table itself, so a value can no longer be added, reloaded or renamed without its restart semantics being stated.
- [docs/metrics.md](docs/metrics.md) — the metrics catalogue: every Prometheus metric SoroTrail exports, with its type, its labels and their closed value sets, the help text `/metrics` serves, and what the number means to an operator. It is generated from a table in `internal/metrics` that is checked against `metrics.go`'s declarations and against what the registries actually serve, so renaming, removing or adding a metric without documenting it fails the test; the same table pins the Prometheus naming rules and the no-ids-in-labels rule, and drives a cardinality test proving client-shaped values never reach a label.
- [docs/errors.md](docs/errors.md) — the API's error catalogue: every failure condition the server can produce, the status it answers with, the exact message a client sees, and the operations it can surface on. It is generated from `internal/api/error_catalogue_test.go`, which cross-checks it against the handlers' source and the shipped OpenAPI spec in both directions, so an undocumented new error — or a stale entry for one the server can no longer produce — fails the test. The spec gained the statuses it was missing along the way: 501 on the API-key and contract endpoints, 503 on the contract spec-override endpoints, 403 on the subscription create/update endpoints, and 426 for a refused websocket handshake.

### Changed

- CI caches Go modules and build output with an explicit key that includes the Go version, `go.sum` and the build-tag flavour, so the `-tags=integration` job no longer shares a single cache slot with the untagged jobs.
- The integration-tagged suite now runs against a matrix of Postgres 15 and 16 rather than 16 alone, and the aggregate `integration` check still reports the name branch tooling expects.
- Migration version-number collisions are gated by their own database-free `migrations` job and `make migrations-check`, ahead of the suites one collision would otherwise break. [CONTRIBUTING.md](CONTRIBUTING.md) documents how to pick the next version number.
- Dependency CVEs are reported by a new `vulncheck` CI job, which runs `govulncheck ./...` on pull requests and on pushes to `main` and fails when a known vulnerability is reachable from the module's own code. The module's minimum Go moves to 1.26.9, the newest 1.26 patch, because govulncheck reports standard-library advisories against the exact patch the `go` directive names and 1.26.0 carried 36 reachable ones.

### Fixed

- The in-process event broker in `internal/api` honours every filter field the REST query path does: a subscription with `topic0`–`topic3`, `topic_contains`, `has_value`, `tx_hash`, `network`, `contract_ids`, `contract_id_prefix`, `in_successful_call`, `tx_index`, `op_index`, `from_time` or `to_time` no longer receives events the equivalent `GET /events` query excludes. The shared table of filter cases that proves it is run against both halves, one of them against a real Postgres. The live `/events/ws` broadcaster in `internal/broadcast` keeps its own, narrower matcher and is unchanged — see [docs/subscriptions.md](docs/subscriptions.md).

### Removed
