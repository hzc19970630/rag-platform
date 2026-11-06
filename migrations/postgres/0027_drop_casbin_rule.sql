-- casbin_rule has no reader or writer: authorization policies are projected
-- from memberships/knowledge bases in memory and never persisted (DESIGN.md).
-- Dropping it removes an unused table and its ptype index.
DROP TABLE IF EXISTS casbin_rule;
