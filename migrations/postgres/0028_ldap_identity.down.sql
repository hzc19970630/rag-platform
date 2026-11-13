DROP INDEX IF EXISTS users_ldap_dn_unique;
ALTER TABLE users DROP COLUMN IF EXISTS ldap_dn;
