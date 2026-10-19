-- Re-keys watched_contracts by (network, contract_id), matching the
-- Postgres 0027 and SQLite 0005 alignment (issue #537).
--
-- ALTER ... ADD COLUMN cannot change a ReplacingMergeTree sort key, and
-- the key must change: with ORDER BY contract_id alone, FINAL would
-- collapse the same contract watched on two networks into whichever row
-- has the newer added_at, silently dropping one network's entry. So the
-- migration builds a new table with the right key, copies the surviving
-- (non-removed) rows over — attributing them to the default network,
-- which is all the pre-0023 code ever ingested — and swaps the names.
-- The old table is left in place rather than dropped so no data is lost
-- if the swap needs manual inspection; it carries no versions the new
-- table lacks.

CREATE TABLE IF NOT EXISTS watched_contracts_network
(
    network     String DEFAULT 'default',
    contract_id String,
    added_at    DateTime64(3) DEFAULT now64(3),
    removed_at  Nullable(DateTime64(3)) DEFAULT NULL
)
ENGINE = ReplacingMergeTree(added_at)
ORDER BY (network, contract_id);

INSERT INTO watched_contracts_network (network, contract_id, added_at, removed_at)
    SELECT 'default' AS network, contract_id, added_at, removed_at
    FROM watched_contracts;

-- Swap: watch_table now carries the network key; the old table keeps the
-- pre-migration name suffixed with _legacy for inspection.
RENAME TABLE watched_contracts TO watched_contracts_legacy;
RENAME TABLE watched_contracts_network TO watched_contracts;
