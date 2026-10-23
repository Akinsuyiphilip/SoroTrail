-- Contract spec override history (task: keep a history of contract spec overrides).
-- Every PUT and DELETE on contract_spec_overrides appends a row here, so
-- operators can answer "did someone change the override?", when, and with
-- which key. spec_json is NULL for a tombstone (a delete).
--
-- Rows are keyed by (contract_id, id DESC) so the current-override read path
-- never scans this table, and history listings walk it newest first.
--
-- Retention policy: all history rows are retained indefinitely. There is no
-- automatic pruning; operators who need to bound growth can delete old rows
-- manually. This is documented rather than enforced so the read path never
-- pays for retention sweeps.
CREATE TABLE IF NOT EXISTS contract_spec_override_history (
    id             bigserial PRIMARY KEY,
    contract_id    text NOT NULL,
    spec_json      jsonb,                  -- NULL = tombstone (override deleted)
    api_key_prefix text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_contract_spec_override_history_contract
    ON contract_spec_override_history (contract_id, id DESC);
