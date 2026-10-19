# Changelog

## [Unreleased]

### Added

- [docs/errors.md](docs/errors.md) — the API's error catalogue: every failure condition the server can produce, the status it answers with, the exact message a client sees, and the operations it can surface on. It is generated from `internal/api/error_catalogue_test.go`, which cross-checks it against the handlers' source and the shipped OpenAPI spec in both directions, so an undocumented new error — or a stale entry for one the server can no longer produce — fails the test. The spec gained the statuses it was missing along the way: 501 on the API-key and contract endpoints, 503 on the contract spec-override endpoints, 403 on the subscription create/update endpoints, and 426 for a refused websocket handshake.

### Changed

- CI caches Go modules and build output with an explicit key that includes the Go version, `go.sum` and the build-tag flavour, so the `-tags=integration` job no longer shares a single cache slot with the untagged jobs.
- The integration-tagged suite now runs against a matrix of Postgres 15 and 16 rather than 16 alone, and the aggregate `integration` check still reports the name branch tooling expects.
- Migration version-number collisions are gated by their own database-free `migrations` job and `make migrations-check`, ahead of the suites one collision would otherwise break. [CONTRIBUTING.md](CONTRIBUTING.md) documents how to pick the next version number.
- Dependency CVEs are reported by a new `vulncheck` CI job, which runs `govulncheck ./...` on pull requests and on pushes to `main` and fails when a known vulnerability is reachable from the module's own code. The module's minimum Go moves to 1.26.9, the newest 1.26 patch, because govulncheck reports standard-library advisories against the exact patch the `go` directive names and 1.26.0 carried 36 reachable ones.

### Fixed

- The in-process event broker in `internal/api` honours every filter field the REST query path does: a subscription with `topic0`–`topic3`, `topic_contains`, `has_value`, `tx_hash`, `network`, `contract_ids`, `contract_id_prefix`, `in_successful_call`, `tx_index`, `op_index`, `from_time` or `to_time` no longer receives events the equivalent `GET /events` query excludes. The shared table of filter cases that proves it is run against both halves, one of them against a real Postgres. The live `/events/ws` broadcaster in `internal/broadcast` keeps its own, narrower matcher and is unchanged — see [docs/subscriptions.md](docs/subscriptions.md).

### Removed
