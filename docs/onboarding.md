# Contributor Onboarding Guide

Welcome to SoroTrail! This guide walks you through everything you need to make
your first contribution — from cloning the repo to getting a PR merged.

## Quick Start (TL;DR)

```sh
# 1. Clone and enter
git clone https://github.com/sorotrail/SoroTrail.git
cd SoroTrail

# 2. Build (writes bin/sorotrail)
make build

# 3. Run with zero deps (SQLite)
DATABASE_URL=sqlite:./sorotrail.db make run

# 4. Verify
curl -s http://localhost:8080/health
curl -s http://localhost:8080/stats

# 5. Run tests
make test          # unit + race detector
make lint          # golangci-lint
```

---

## 1. Prerequisites

| Tool | Minimum Version | Purpose |
|------|----------------|---------|
| Go | 1.21+ (1.25+ recommended) | Building, testing. The `go` directive in `go.mod` pins the toolchain; `go build` auto-downloads it. |
| Docker | any recent | Local Postgres via `docker compose`, integration test containers via testcontainers-go. |
| golangci-lint | latest | `make lint`. CI runs the pinned version from `.golangci-lint-version`. Install from [golangci-lint.run](https://golangci-lint.run/). |
| golang-migrate | latest | Optional: `make migrate-up`/`migrate-down`. Migrations run automatically on startup. |
| promtool | latest | `make check-monitoring` (validates Prometheus rules). |
| python3 | 3.8+ | `make check-monitoring` (validates Grafana dashboard JSON). |

---

## 2. Environment Setup (Step by Step)

### 2.1 Clone the Repository

```sh
git clone https://github.com/sorotrail/SoroTrail.git
cd SoroTrail
```

### 2.2 Verify Go Toolchain

```sh
go version
# Should show go1.25.x or later (matches go.mod toolchain)
```

### 2.3 Start a Database

**Option A: SQLite (zero deps, fastest)**
```sh
# No extra step needed — just set DATABASE_URL when running
```

**Option B: Postgres via Docker Compose (recommended for development)**
```sh
docker compose up -d postgres
# Wait for healthcheck to pass (check with `docker ps`)
```

**Option C: Full stack (Postgres + indexer + Jaeger)**
```sh
docker compose --profile dev up --build
```

### 2.4 Configure Environment

```sh
cp .env.example .env
# Edit .env — minimum required: DATABASE_URL
# For SQLite: DATABASE_URL=sqlite:./sorotrail.db
# For Postgres: DATABASE_URL=postgres://sorotrail:sorotrail@localhost:5432/sorotrail?sslmode=disable
```

### 2.5 Build and Run

```sh
make build        # compiles to bin/sorotrail
make run          # runs with env from .env (or DATABASE_URL=sqlite:./sorotrail.db make run)
```

### 2.6 Verify It Works

```sh
curl -s http://localhost:8080/health | jq .
curl -s http://localhost:8080/stats | jq .
```

You should see JSON responses with `status: "ok"` and event counts.

---

## 3. Test Tiers Explained

SoroTrail has three test tiers. Know which one to run and when.

### 3.1 Unit Tests (`make test` / `make test-fast`)

```sh
make test          # go test -race ./...  (default CI gate)
make test-fast     # go test ./...       (no race detector, faster)
```

- **Runs**: All packages *without* the `integration` build tag.
- **Requires**: Nothing external. Database-backed tests skip themselves when `TEST_DATABASE_URL` is unset.
- **Time**: ~10-15 seconds.
- **When to run**: Every time before you push. This is the primary CI gate.

### 3.2 Integration Tests (`make test-integration`)

```sh
make test-integration
# Runs: go test -tags=integration -p 1 ./... -count=1
```

- **Runs**: Tests tagged with `//go:build integration` (migrations, event upserts, ingestion cursor, API filters).
- **Requires**: 
  - Docker running (spins up ephemeral Postgres 16 via testcontainers-go), OR
  - `TEST_DATABASE_URL` set to a real Postgres instance.
- **Time**: ~30-60 seconds.
- **When to run**: Before opening a PR that touches schema, ingestion, or API filters.
- **Important**: Uses `-p 1` (serial execution) because tests share and truncate tables.

### 3.3 Full Suite Against Live Postgres (`make test-db`)

```sh
export TEST_DATABASE_URL="postgres://user:pass@host:5432/db?sslmode=disable"
make test-db
```

- **Runs**: Entire test suite against your provided Postgres.
- **Requires**: Live Postgres at `TEST_DATABASE_URL`. **Never point at a database with data you care about** — tests truncate tables.
- **When to run**: When you need to verify behavior against a specific Postgres version or debug a flaky integration test.

### 3.4 Fuzz Tests

```sh
go test ./internal/decode -run '^$' -fuzz FuzzDecodeScVal -fuzztime 30s
go test ./internal/decode -run '^$' -fuzz FuzzDecodeTopicArray -fuzztime 30s
```

- **When to run**: After changes to `internal/decode/`. CI runs fuzz targets on PRs with a 30s budget.

### 3.5 Benchmarks

```sh
make bench-ci
```

- **Runs**: Short benchmark smoke test (compile + run).
- **When to run**: After performance-sensitive changes.

---

## 4. Picking an Issue and Getting Assigned

### 4.1 Find an Issue

- **Good first issues**: Filter by label `good first issue` on GitHub Issues.
- **Bugs**: Label `bug` — usually have a clear reproduction and fix scope.
- **Features**: Label `enhancement` — larger scope, may need design discussion first.
- **Docs**: Label `documentation` — updates to `docs/`, `README.md`, or code comments.

### 4.2 Get Assigned

1. **Comment on the issue**: "I'd like to work on this — assigning to me?"
2. **Wait for maintainer acknowledgment** — they'll assign you or ask clarifying questions.
3. **Don't start coding until assigned** — prevents duplicate work.

### 4.3 Create a Branch

```sh
git checkout -b <short-descriptive-name>
# Examples:
# fix-clickhouse-deadletter-stubs
# test-telemetry-sanitization
# chore-untrack-root-artifacts
```

---

## 5. Development Workflow

### 5.1 Make Your Changes

- Follow the existing code style (run `make lint` to check).
- Write tests for new behavior — table-driven with `t.Run`, using `testify`.
- Update documentation if you change user-facing behavior.

### 5.2 Run the Local CI Gate

```sh
make ci
```

This reproduces the **exact CI pipeline** locally:

1. `make build-all` — `go build ./...`
2. `make vet` — `go vet ./...`
3. `make vet-integration` — `go vet -tags=integration ./...`
4. `make spec-check` — OpenAPI spec drift check
5. `make migrations-check` — migration numbering validation
6. `make test-ci` — `go test -p 1 ./... -count=1 -race -timeout=120s`
7. `make test-integration` — integration suite (skips if no Docker/Postgres)
8. `make bench-ci` — benchmark smoke run
9. `make lint` — `golangci-lint run` (guarded against toolchain drift)

**Stop at the first failure**. Fix it, then re-run `make ci`.

### 5.3 Individual Checks (if `make ci` is slow)

```sh
make build-all      # go build ./...
make vet            # go vet ./...
make vet-integration # go vet -tags=integration ./...
make test           # unit + race
make test-integration # integration (needs Docker)
make lint           # golangci-lint
make spec-check     # OpenAPI drift
make migrations-check # migration numbering
```

---

## 6. Code Conventions

### 6.1 Go Style

- **Standard library first** — avoid external deps when stdlib works.
- **Interfaces over concretions** — the four seams (`rpc.Client`, `decode.Decoder`, `store.Store`, `spec.Enricher`) are interfaces; depend on those.
- **Small functions, focused packages** — when in doubt, match surrounding code.
- **Errors**: Wrap with `fmt.Errorf("context: %w", err)`; use `errors.Is/As` for checks.
- **Logging**: `log/slog` with structured keys (`FieldContractID`, `FieldLedger`, etc. from `internal/telemetry`). Pass loggers explicitly — no globals.

### 6.2 Testing

- **Table-driven tests** with `t.Run` for each case.
- **Use testify** (`assert`, `require`) — already in `go.mod`.
- **Mock via interfaces** — see `internal/ingester/mocks_test.go` for patterns.
- **Integration tests** carry `//go:build integration` and use `internal/testdb`.
- **Fuzz targets** in `internal/decode/` — add regressions to `testdata/fuzz/`.

### 6.3 Migrations

- **Never edit an applied migration** — add a new numbered pair instead.
- **Schema changes** = new migration in `internal/store/migrations/` (Postgres) and `internal/store/migrations/sqlite/` (SQLite).
- **Update the migration test** — `TestMigrations_ApplyFromEmptyLand` column list must match the final schema.
- **Run `make migrations-check`** after any migration change.

### 6.4 API Changes

- **Source of truth**: `api/openapi.yaml` — edit this, never the generated files.
- **Regenerate**: `make spec` (updates `internal/api/openapi.json`), `make client` (updates `pkg/client/client.gen.go`).
- **Drift tests**: `pkg/docs.TestSpecCopiesAreIdentical` and `pkg/client.TestGeneratedClientIsUpToDate` fail the build if you skip regeneration.

---

## 7. Review Conventions

### 7.1 PR Requirements

- `make ci` passes (green checks on GitHub Actions).
- `Closes #<issue>` in PR description.
- Summary of what changed and how you verified it.
- For behavioral changes: update `CHANGELOG.md` under `[Unreleased]`.

### 7.2 Comment Style

- **Explain *why*, not *what*** — the code shows what; comments explain rationale.
- **Full sentences** with proper punctuation.
- **No redundant comments** — `i++ // increment i` is noise.
- **Seam comments** — interfaces have `// InterfaceName is...` explaining the contract.

### 7.3 Common Review Feedback

| Feedback | What it means | Fix |
|----------|---------------|-----|
| "This leaks the concrete type" | You imported a concrete implementation where an interface was expected | Depend on the interface (`store.Store`, not `*Postgres`) |
| "Add a test for the error path" | Happy path covered, but failure case isn't | Add a table case that forces the error |
| "Run `make spec`" | You edited `api/openapi.yaml` but didn't regenerate | Run `make spec` (and `make client` if operations/schemas changed) |
| "Migration test needs update" | Schema changed but `TestMigrations_ApplyFromEmptyLand` wasn't updated | Add the new columns to the expected column list in that test |
| "Comment explains what, not why" | Comment restates the code | Rewrite to explain the design decision or constraint |

---

## 8. Common First-PR Mistakes (and the CI Failure Each Produces)

| Mistake | CI Job That Fails | Error Message |
|---------|-------------------|---------------|
| Forgot `make spec` after editing `api/openapi.yaml` | `spec-check` | `pkg/docs.TestSpecCopiesAreIdentical` diff shows YAML/JSON mismatch |
| Forgot `make client` after adding/removing an endpoint | `test-ci` | `pkg/client.TestGeneratedClientIsUpToDate` fails |
| Edited an applied migration file | `migrations-check` | `TestMigrate_VersionNumbersAreUnique` or `TestMigrate_FilesAreWellFormed` collision |
| Added a `toolchain` line to `go.mod` unintentionally | `lint` | `golangci-lint` version mismatch (Go version too new for pinned linter) |
| Wrote a test without `-p 1` for integration tests | `test-integration` | Deadlock or "database is locked" / truncation races |
| Pointed `TEST_DATABASE_URL` at a real database | `test-integration` | Data loss (tests truncate tables) — **never do this** |
| Added a new Store method without implementing it in ClickHouse | `test-ci` | `TestClickHouse_Unsupported_Methods` fails (missing method) |
| Used `log.Printf` instead of `slog` | `lint` | `govet` or `golangci-lint` warns about non-structured logging |
| Forgot to wrap error with `%w` | `lint` | `errorlint` suggests wrapping |
| Test depends on test order / global state | `test-ci` (flaky) | Intermittent failures on CI reruns |

---

## 9. Verified Commands Reference

Every command below has been verified to work in a clean clone.

### Build & Run

```sh
make build                    # → bin/sorotrail
DATABASE_URL=sqlite:./db make run
docker compose --profile dev up --build
```

### Testing

```sh
make test                     # unit + race
make test-fast                # unit, no race
make test-integration         # integration (needs Docker or TEST_DATABASE_URL)
export TEST_DATABASE_URL=...; make test-db  # full suite against live PG
make cover                    # coverage summary
make cover-html               # HTML report in browser
make bench-ci                 # benchmark smoke
go test ./internal/decode -run '^$' -fuzz FuzzDecodeScVal -fuzztime 30s
```

### Lint & Quality

```sh
make lint                     # golangci-lint
make lint-fix                 # golangci-lint --fix
make vet                      # go vet ./...
make vet-integration          # go vet -tags=integration ./...
```

### API Spec

```sh
make spec                     # regenerate internal/api/openapi.json
make client                   # regenerate pkg/client/client.gen.go
make spec-check               # diff check (runs in CI)
```

### Migrations

```sh
make migrate-up               # apply all (needs migrate CLI)
make migrate-down             # rollback one (needs migrate CLI)
make migrations-check         # validate numbering (no DB needed)
sorotrail migrate-status      # show pending (needs DB)
```

### Monitoring

```sh
make check-monitoring         # promtool + python validation
```

### Full CI Gate

```sh
make ci                       # runs everything in order, stops on first failure
```

---

## 10. Getting Help

- **Stuck on setup?** Check `docs/troubleshooting.md` and `docs/local-setup.md#troubleshooting`.
- **Don't understand the architecture?** Read `docs/architecture-overview.md` (10 min) then `docs/architecture.md` (deep dive).
- **Need clarification on an issue?** Comment on the GitHub issue — maintainers respond within a business day.
- **Code review taking time?** Ping the reviewer with a specific question; "ready for review" comments without questions get lower priority.

---

## 11. Your First PR Checklist

- [ ] Issue assigned to you (commented and got maintainer acknowledgment)
- [ ] Branch created from `main` with descriptive name
- [ ] Changes made with tests
- [ ] `make ci` passes locally
- [ ] `CHANGELOG.md` updated (if user-visible change)
- [ ] `api/openapi.yaml` regenerated with `make spec` (if API changed)
- [ ] `pkg/client` regenerated with `make client` (if API changed)
- [ ] Migration test updated (if schema changed)
- [ ] PR description includes `Closes #<issue>` and summary
- [ ] PR is up to date with `main` (rebased if needed)

---

## Appendix: Project Structure Cheat Sheet

```
cmd/sorotrail/          Main entry point, wiring, shutdown
internal/config/        Env parsing + validation
internal/rpc/           Stellar RPC JSON-RPC client (interface: rpc.Client)
internal/decode/        ScVal → JSON (interface: decode.Decoder)
internal/store/         Postgres persistence (interface: store.Store)
internal/ingester/      Polling loop, pagination, backoff
internal/replay/        Re-decode stored raw XDR (sorotrail replay)
internal/api/           chi HTTP handlers (read-only by default)
internal/spec/          Contract-spec enrichment (interface: spec.Enricher)
internal/testdb/        //go:build integration helper
internal/telemetry/     Logging, tracing, metrics
internal/webhook/       Subscription delivery
internal/audit/         Background data integrity auditor
internal/pruner/        Retention/age-based deletion
internal/backfill/      Horizon-based historical backfill
internal/meta/          Contract metadata enrichment
api/openapi.yaml        OpenAPI 3.1 source of truth
deploy/                 Grafana dashboard, Prometheus rules, Helm chart
docs/                   All documentation (this file lives here)
```