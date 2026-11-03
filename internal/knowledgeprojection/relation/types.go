package relation

const SchemaRelationProjection = "RelationProjectionPackageV1"

// RelationProjectionPackage is the versioned output of relation extraction. It
// is persisted as graph rows; only ids travel in queue messages or API
// responses. SourceChunkIDs are EvidencePackage chunk ids and are mandatory for
// every relation.
type RelationProjectionPackage struct {
	SchemaVersion   string              `json:"schema_version"`
	TenantID        string              `json:"tenant_id"`
	KnowledgeBaseID string              `json:"knowledge_base_id"`
	ACLVersion      int64               `json:"acl_version"`
	Entities        []ExtractedEntity   `json:"entities"`
	Relations       []ExtractedRelation `json:"relations"`
}

type ExtractedEntity struct {
	Key            string            `json:"key"`
	Name           string            `json:"name"`
	Type           string            `json:"type,omitempty"`
	Aliases        []string          `json:"aliases,omitempty"`
	AliasRules     map[string]string `json:"alias_rules,omitempty"`
	Properties     map[string]string `json:"properties,omitempty"`
	SourceChunkIDs []string          `json:"source_chunk_ids,omitempty"`
}

type ExtractedRelation struct {
	SubjectKey     string            `json:"subject_key"`
	Predicate      string            `json:"predicate"`
	ObjectKey      string            `json:"object_key"`
	Properties     map[string]string `json:"properties,omitempty"`
	Confidence     float64           `json:"confidence,omitempty"`
	SourceChunkIDs []string          `json:"source_chunk_ids"`
}
