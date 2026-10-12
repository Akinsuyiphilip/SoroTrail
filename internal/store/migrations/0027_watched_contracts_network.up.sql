-- Completes the network dimension on the operator watch list.
--
-- 0021_network_column added events.network, audit_state.network and
-- audit_findings.network, and 0023_network_ingestion_state re-keyed
-- ingestion_state by network, but deliberately left watched_contracts
-- alone because the Go code still treated it as a singleton. The store
-- now treats the watch list as network-scoped as well (issue #537), so
-- watched_contracts needs the same treatment: a contract watched on
-- testnet is not the same watch-list entry as the same contract watched
-- on mainnet, and the ingester builds its per-network filter batches
-- from this table.
--
-- watched_contracts predates golang-migrate's first release of this
-- series (it arrives from 0001_init via the legacy-upgrade path, which
-- can also rebuild it), so both the table statement and the
-- deduplicating rewrite step must tolerate a table that already carries
-- the network column — the guard pattern used by 0012 and 0014.

ALTER TABLE watched_contracts ADD COLUMN IF NOT EXISTS network text NOT NULL DEFAULT 'default';

-- Collapse any (network, contract_id) duplicates created by the defaulted
-- column into one row per pair before re-keying the primary key. Postgres
-- has no rowid pseudocolumn; ctid is the physical row address, and
-- comparing them gives a stable-enough total order for a self-join dedup.
-- If no duplicates exist (the normal case: contract_id was the primary key)
-- the statement deletes nothing.
DELETE FROM watched_contracts a
    USING watched_contracts b
    WHERE a.ctid > b.ctid
      AND a.network = b.network
      AND a.contract_id = b.contract_id;

ALTER TABLE watched_contracts DROP CONSTRAINT IF EXISTS watched_contracts_pkey CASCADE;
ALTER TABLE watched_contracts ADD PRIMARY KEY (network, contract_id);

-- tenant_watched_contracts carries the same dimension so the union query
-- in Postgres.ListWatchedContracts can group by the pair; existing rows
-- are attributed to the default network, which is the only network the
-- pre-0027 code ever ingested on.
ALTER TABLE tenant_watched_contracts ADD COLUMN IF NOT EXISTS network text NOT NULL DEFAULT 'default';

CREATE INDEX IF NOT EXISTS idx_tenant_watched_contract
    ON tenant_watched_contracts (network, contract_id);
