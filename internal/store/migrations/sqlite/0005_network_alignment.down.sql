-- Reverse of 0005: collapse back to the singleton audit_state row, drop
-- the findings network column, and re-key watched_contracts by
-- contract_id alone. Only 'default'-network rows survive — the down path
-- restores the pre-0005 shape, not the multi-network data.

-- The index must go before its column: SQLite keeps an index referencing
-- a dropped column and the next statement on the table errors with
-- "error in index ... after drop column".
DROP INDEX IF EXISTS idx_audit_findings_network;

CREATE TABLE audit_state_singleton (
    id                      INTEGER PRIMARY KEY CHECK (id = 1),
    verified_through_ledger INTEGER NOT NULL DEFAULT 0,
    updated_at              TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
INSERT INTO audit_state_singleton (id, verified_through_ledger, updated_at)
    SELECT 1, verified_through_ledger, updated_at FROM audit_state WHERE network = 'default';
DROP TABLE audit_state;
ALTER TABLE audit_state_singleton RENAME TO audit_state;

ALTER TABLE audit_findings DROP COLUMN network;

CREATE TABLE watched_contracts_single_network (
    contract_id TEXT PRIMARY KEY,
    added_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
INSERT INTO watched_contracts_single_network (contract_id, added_at)
    SELECT contract_id, added_at FROM watched_contracts WHERE network = 'default';
DROP TABLE watched_contracts;
ALTER TABLE watched_contracts_single_network RENAME TO watched_contracts;

