-- Reverse of 0022: collapse the network dimension back to a single
-- 'default' value. Any rows on a non-default network are dropped — the
-- down path only has to restore the pre-0027 shape, not preserve
-- multi-network data.

DELETE FROM watched_contracts WHERE network <> 'default';
ALTER TABLE watched_contracts ALTER COLUMN network SET DEFAULT 'default';
ALTER TABLE watched_contracts DROP CONSTRAINT IF EXISTS watched_contracts_pkey CASCADE;
ALTER TABLE watched_contracts DROP COLUMN IF EXISTS network;
ALTER TABLE watched_contracts ADD PRIMARY KEY (contract_id);

DELETE FROM tenant_watched_contracts WHERE network <> 'default';
DROP INDEX IF EXISTS idx_tenant_watched_contract;
ALTER TABLE tenant_watched_contracts DROP COLUMN IF EXISTS network;
CREATE INDEX IF NOT EXISTS idx_tenant_watched_contract
    ON tenant_watched_contracts (contract_id);
