DELETE FROM tenant_watched_contracts a
    USING tenant_watched_contracts b
    WHERE a.ctid > b.ctid
      AND a.tenant_id = b.tenant_id
      AND a.contract_id = b.contract_id;

ALTER TABLE tenant_watched_contracts DROP CONSTRAINT IF EXISTS tenant_watched_contracts_pkey CASCADE;
ALTER TABLE tenant_watched_contracts ADD PRIMARY KEY (tenant_id, contract_id);
