# Changelog

## [Unreleased]

### Added

### Changed

- CI caches Go modules and build output with an explicit key that includes the Go version, `go.sum` and the build-tag flavour, so the `-tags=integration` job no longer shares a single cache slot with the untagged jobs.
- The integration-tagged suite now runs against a matrix of Postgres 15 and 16 rather than 16 alone, and the aggregate `integration` check still reports the name branch tooling expects.
- Migration version-number collisions are gated by their own database-free `migrations` job and `make migrations-check`, ahead of the suites one collision would otherwise break. [CONTRIBUTING.md](CONTRIBUTING.md) documents how to pick the next version number.

### Fixed

- The in-process event broker in `internal/api` honours every filter field the REST query path does: a subscription with `topic0`–`topic3`, `topic_contains`, `has_value`, `tx_hash`, `network`, `contract_ids`, `contract_id_prefix`, `in_successful_call`, `tx_index`, `op_index`, `from_time` or `to_time` no longer receives events the equivalent `GET /events` query excludes. The shared table of filter cases that proves it is run against both halves, one of them against a real Postgres. The live `/events/ws` broadcaster in `internal/broadcast` keeps its own, narrower matcher and is unchanged — see [docs/subscriptions.md](docs/subscriptions.md).

### Removed
