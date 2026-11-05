package archtest

// TenancyAllowlistEntry is one deliberate exception to the raw-SQL tenancy
// rule. Entries are matched by file and table so a new statement in the same
// file cannot inherit an old exemption; the violation reports the exact line.
type TenancyAllowlistEntry struct {
	File   string
	Tables []string
	Reason string
}

// TenancyAllowlist is locked by tenancy_test.go: adding an entry requires
// editing that test, which keeps every cross-tenant statement reviewable.
var TenancyAllowlist = []TenancyAllowlistEntry{
	{
		File:   "internal/authz/service.go",
		Tables: []string{"audit_logs"},
		Reason: "retention sweep: deletes one bounded batch of expired audit rows across all tenants",
	},
	{
		File:   "internal/conversation/events.go",
		Tables: []string{"stream_events"},
		Reason: "retention sweep: deletes one bounded batch of expired replay buffers across all tenants",
	},
	{
		File:   "internal/conversation/async.go",
		Tables: []string{"answer_jobs"},
		Reason: "stale-lease sweep: fails RUNNING jobs whose worker disappeared across all tenants; it reads tenant_id only to address the job's own terminal events",
	},
	{
		File:   "internal/platform/evidenceindex/postgres.go",
		Tables: []string{"chunks"},
		Reason: "embedding writer addresses one chunk by primary key (plus version id) from a pipeline task or the managed re-embed sweep; no list or scan is involved",
	},
	{
		File:   "internal/platform/projectioncache/postgres.go",
		Tables: []string{"evidence_packages", "projection_generation_cache"},
		Reason: "maintenance sweep: removes cache rows whose package is gone across all tenants; deleting a cache row only forces regeneration",
	},
}

func tenancyAllowed(file, table string) bool {
	for _, entry := range TenancyAllowlist {
		if entry.File != file {
			continue
		}
		for _, allowed := range entry.Tables {
			if allowed == table {
				return true
			}
		}
	}
	return false
}
