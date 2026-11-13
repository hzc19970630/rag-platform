ALTER TABLE users ADD COLUMN ldap_dn VARCHAR(512) NOT NULL DEFAULT '';
CREATE UNIQUE INDEX users_ldap_dn_unique ON users (ldap_dn) WHERE ldap_dn <> '';
