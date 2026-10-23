-- Contract spec override history, mirroring the Postgres series'
-- 0029_contract_spec_override_history. Every PUT and DELETE on
-- contract_spec_overrides appends a row here, so operators can answer "did
-- someone change the override?", when, and with which key. spec_json is NULL
-- for a tombstone (a delete).
--
-- Rows are keyed by (contract_id, id DESC) so the current-override read path
-- never scans this table, and history listings walk it newest first.
--
-- Retention policy: all history rows are retained indefinitely; there is no
-- automatic pruning.
CREATE TABLE IF NOT EXISTS contract_spec_override_history (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    contract_id    TEXT NOT NULL,
    spec_json      TEXT,                 -- NULL = tombstone (override deleted)
    api_key_prefix TEXT NOT NULL,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_contract_spec_override_history_contract
    ON contract_spec_override_history (contract_id, id DESC);
