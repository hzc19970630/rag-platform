-- Keep this fixture in a new seed revision so existing local databases receive it.
INSERT INTO knowledge_bases (id, tenant_id, owner_id, name, description)
VALUES ('kb-other', 'other-tenant', 'other-user', 'Other Tenant Knowledge Base', 'Cross-tenant isolation fixture.')
ON CONFLICT (id) DO UPDATE SET tenant_id = EXCLUDED.tenant_id, owner_id = EXCLUDED.owner_id, name = EXCLUDED.name, description = EXCLUDED.description, updated_at = NOW();

INSERT INTO knowledge_base_members (knowledge_base_id, user_id, role)
VALUES ('kb-other', 'other-user', 'owner')
ON CONFLICT (knowledge_base_id, user_id) DO UPDATE SET role = EXCLUDED.role, updated_at = NOW();
