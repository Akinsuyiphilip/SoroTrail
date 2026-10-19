-- Per-contract ingestion resume cursors, mirroring the Postgres series'
-- 0012_contract_cursors. Watched-contract ingestion on SQLite used to run
-- off the single global ingestion_state row, so one lagging contract held
-- back every other watched contract; this table gives each watched
-- contract its own resume position instead.
CREATE TABLE IF NOT EXISTS contract_cursors (
    contract_id          TEXT PRIMARY KEY,
    last_ingested_ledger INTEGER NOT NULL DEFAULT 0,
    last_cursor          TEXT NOT NULL DEFAULT '',
    updated_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_contract_cursors_updated_at
    ON contract_cursors (updated_at);
