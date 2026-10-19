-- Aligns the SQLite series with the network-scoped tables the Go code
-- already reads and writes (issue #537). The SQLite 0001 schema predates
-- 0021_network_column: audit_state is an id = 1 singleton while the
-- Postgres backend keys it by network, audit_findings has no network
-- column even though RecordAuditFinding and ListOpenFindingsByRange are
-- network-aware, and watched_contracts is keyed by contract_id alone.
--
-- SQLite cannot ALTER the primary key of an existing table, so each
-- segment rebuilds its table under the new shape and copies rows over.

CREATE TABLE audit_state_network (
    network                 TEXT PRIMARY KEY,
    verified_through_ledger INTEGER NOT NULL DEFAULT 0,
    updated_at              TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
INSERT INTO audit_state_network (network, verified_through_ledger, updated_at)
    SELECT 'default', verified_through_ledger, updated_at FROM audit_state;
DROP TABLE audit_state;
ALTER TABLE audit_state_network RENAME TO audit_state;

ALTER TABLE audit_findings ADD COLUMN network TEXT NOT NULL DEFAULT 'default';
CREATE INDEX IF NOT EXISTS idx_audit_findings_network
    ON audit_findings (network);

CREATE TABLE watched_contracts_network (
    network     TEXT NOT NULL DEFAULT 'default',
    contract_id TEXT NOT NULL,
    added_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (network, contract_id)
);
INSERT INTO watched_contracts_network (network, contract_id, added_at)
    SELECT 'default', contract_id, added_at FROM watched_contracts;
DROP TABLE watched_contracts;
ALTER TABLE watched_contracts_network RENAME TO watched_contracts;
