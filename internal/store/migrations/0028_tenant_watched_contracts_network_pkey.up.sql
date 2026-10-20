-- Include network in the tenant_watched_contracts primary key so a tenant can
-- watch the same contract_id across distinct networks (matching
-- watched_contracts' (network, contract_id) key from migration 0027).

DELETE FROM tenant_watched_contracts a
    USING tenant_watched_contracts b
    WHERE a.ctid > b.ctid
      AND a.tenant_id = b.tenant_id
      AND a.network = b.network
      AND a.contract_id = b.contract_id;

ALTER TABLE tenant_watched_contracts DROP CONSTRAINT IF EXISTS tenant_watched_contracts_pkey CASCADE;
ALTER TABLE tenant_watched_contracts ADD PRIMARY KEY (tenant_id, network, contract_id);
