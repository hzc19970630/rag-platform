-- Rollback for 0027: restore the historical empty Casbin table so an older
-- binary that still expects the schema can start. It intentionally contains no
-- data; the application never wrote to it.
CREATE TABLE IF NOT EXISTS casbin_rule (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ptype VARCHAR(100) NOT NULL,
    v0 VARCHAR(100) NOT NULL DEFAULT '',
    v1 VARCHAR(100) NOT NULL DEFAULT '',
    v2 VARCHAR(100) NOT NULL DEFAULT '',
    v3 VARCHAR(100) NOT NULL DEFAULT '',
    v4 VARCHAR(100) NOT NULL DEFAULT '',
    v5 VARCHAR(100) NOT NULL DEFAULT '',
    v6 VARCHAR(100) NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS casbin_rule_ptype_idx ON casbin_rule(ptype);
