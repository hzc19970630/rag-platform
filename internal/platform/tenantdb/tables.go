package tenantdb

import "sort"

// tenantTables own a tenant_id column. The list is the runtime counterpart of
// the RLS migration arrays and the static tenancy gate; it must stay in sync
// with both. task_inbox gained a real tenant_id in 0023, so it is a parent
// table here, not a child scoped through pipeline/projection runs.
var tenantTables = []string{
	"answer_jobs",
	"api_keys",
	"audit_logs",
	"auth_sessions",
	"authorization_revisions",
	"chunks",
	"connector_items",
	"connectors",
	"conversations",
	"document_versions",
	"documents",
	"evidence_packages",
	"graph_entities",
	"graph_entity_aliases",
	"graph_relation_sources",
	"graph_relations",
	"graph_snapshots",
	"knowledge_bases",
	"knowledge_index_records",
	"knowledge_projection_states",
	"memberships",
	"message_citations",
	"messages",
	"outbox_events",
	"pipeline_runs",
	"projection_generation_cache",
	"projection_runs",
	"stream_events",
	"sync_runs",
	"task_dead_letters",
	"task_inbox",
	"wiki_page_sources",
	"wiki_page_versions",
	"wiki_pages",
}

var tenantTableSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(tenantTables))
	for _, table := range tenantTables {
		set[table] = struct{}{}
	}
	return set
}()

// TenantTables returns the canonical tenant-owned table catalog. Callers get a
// copy so the static gate cannot mutate the runtime list.
func TenantTables() []string {
	tables := append([]string(nil), tenantTables...)
	sort.Strings(tables)
	return tables
}
