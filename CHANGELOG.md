# Changelog

## [Unreleased]

### Added

### Changed

- Health endpoints (`GET /health`, `GET /readyz`): each entry in `checks`
  is now an object (`status`, `detail`, `latency_ms`) instead of a bare
  string, and every check reports `ok`, `degraded` or `unready` — an
  unanswered dependency (`unready`) is now distinguished from a reachable
  but unhealthy one (`degraded`), and each check runs under its own
  5-second timeout so one hung dependency can't starve the others. Clients
  that read `checks.<name>` as a string should read its `status` field.
  Spec version bumped to 0.2.0; shapes documented in `api/openapi.yaml`.

### Fixed

### Removed
