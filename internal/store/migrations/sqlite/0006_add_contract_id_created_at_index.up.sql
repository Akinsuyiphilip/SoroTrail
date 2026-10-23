-- Mirrors the Postgres 0009_add_contract_id_created_at_index migration so
-- the two backends carry the same index for the same query shape.
--
-- The contract inventory aggregates by contract_id and reads created_at for
-- last_seen (ListContracts, GetContractSummary). A btree on
-- (contract_id, created_at) keeps that per-contract range scan in one index
-- instead of falling back to the primary key plus a sort.
--
-- IF NOT EXISTS keeps this safe across partial re-applies, matching the
-- other SQLite index migrations.
CREATE INDEX IF NOT EXISTS idx_events_contract_id_created_at
    ON events (contract_id, created_at);
