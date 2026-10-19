# Documentation

This directory holds the project documentation for operators and contributors.
Use this index to find the relevant page by topic before drilling into the
full details.

## Runbooks

- [replay.md](replay.md) — explains how to re-run the decoder over stored raw
  XDR and refresh decoded event data after a decoder change.

## Reference

- No standalone reference pages are currently present in this directory.

## Design

- No design documents are currently present in this directory.
# SoroTrail Documentation

Welcome to the SoroTrail documentation index. This directory contains detailed
technical guides, architectural specifications, runbooks, and operational manuals
for operators and developers.

## Architecture and Core Concepts

- [System Architecture](architecture.md): Complete internal system architecture,
  component boundaries, data flow from Stellar RPC to storage, request lifecycles,
  and sequence diagrams.
- [Architecture Overview](architecture-overview.md): High-level architectural summary
  and system topology.
- [Multi-tenancy](multi-tenancy.md): Multi-tenant isolation model, tenant boundaries,
  contract grants, quotas, and accounting.

## Real-Time and Authentication APIs

- [Live Subscriptions](subscriptions.md): Real-time event streaming over WebSocket,
  pub-sub broker architecture, at-most-once delivery semantics, backpressure handling,
  and the cursor catch-up pattern.
- [API Keys and Scopes](api-keys.md): Key generation, storage hashing, startup
  bootstrap behavior, CLI management, scope enforcement, rotation procedures,
  and the PostgreSQL requirement.

## Ingestion, Backfill, and Replay

- [Backfill Guide](backfill.md): Procedures for historical ledger backfills and
  navigating Stellar RPC retention limits.
- [Replay Guide](replay.md): Re-running decoders against stored raw XDR payloads
  when contract ABIs change or parser bugs are fixed.
- [Local Setup](local-setup.md): Setting up local development dependencies,
  PostgreSQL, and Stellar RPC testnet environments.

## Operations and Runbooks

- [Operational Runbook](runbook.md): Procedures for health probes, scaling, dead-letter
  queue maintenance, and backups.
- [Incident Triage](triage.md): Immediate step-by-step triage guide for production
  outages and lag alerts.
- [Troubleshooting](troubleshooting.md): Diagnosis and remedies for RPC errors,
  database lock contention, decode failures, and startup issues.
- [Logging](logging.md): Structured logging conventions and tracing configuration.
- [Performance](performance.md): Latency profiles, indexing strategies, and
  throughput tuning.
- [Database Indexes](indexes.md): Explanations of composite indexes and query plans.
- [Event Archival](archival.md): Data retention, partition pruning, and historical archiving.
- [Export Sink](sink.md): Bulk event export configurations and external sinks.
