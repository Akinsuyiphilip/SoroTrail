# Changelog

## [Unreleased]

### Added

- The Helm chart tests announce when they skip because `helm` is not on PATH,
  instead of leaving a green run looking like the chart was verified, and
  `REQUIRE_HELM_CHART_TESTS=1` turns that skip into a failure so a job that
  installs Helm can assert the tests really ran.

### Changed

### Fixed

### Removed
