INSERT INTO users (id, email, display_name)
VALUES
    ('dev-user', 'dev@example.local', 'Local Developer'),
    ('viewer-a', 'viewer-a@example.local', 'Tenant A Viewer'),
    ('editor-a', 'editor-a@example.local', 'Tenant A Editor'),
    ('other-user', 'other@example.local', 'Other Tenant User')
ON CONFLICT (id) DO NOTHING;

INSERT INTO tenants (id, name)
VALUES
    ('dev-tenant', 'Development Tenant'),
    ('other-tenant', 'Other Tenant')
ON CONFLICT (id) DO NOTHING;

INSERT INTO memberships (tenant_id, user_id, role)
VALUES
    ('dev-tenant', 'dev-user', 'owner'),
    ('dev-tenant', 'viewer-a', 'member'),
    ('dev-tenant', 'editor-a', 'member'),
    ('other-tenant', 'other-user', 'owner')
ON CONFLICT (tenant_id, user_id) DO UPDATE SET role = EXCLUDED.role, updated_at = NOW();

INSERT INTO knowledge_bases (id, tenant_id, owner_id, name, description)
VALUES
    ('kb-dev', 'dev-tenant', 'dev-user', 'Development Knowledge Base', 'Seed knowledge base for M1 acceptance tests.'),
    ('kb-other', 'other-tenant', 'other-user', 'Other Tenant Knowledge Base', 'Cross-tenant isolation fixture.')
ON CONFLICT (id) DO UPDATE SET tenant_id = EXCLUDED.tenant_id, owner_id = EXCLUDED.owner_id, name = EXCLUDED.name, description = EXCLUDED.description, updated_at = NOW();

INSERT INTO knowledge_base_members (knowledge_base_id, user_id, role)
VALUES
    ('kb-dev', 'dev-user', 'owner'),
    ('kb-dev', 'viewer-a', 'viewer'),
    ('kb-dev', 'editor-a', 'editor'),
    ('kb-other', 'other-user', 'owner')
ON CONFLICT (knowledge_base_id, user_id) DO UPDATE SET role = EXCLUDED.role, updated_at = NOW();
