// Package configschema is the single declaration of the business configuration
// fields the product exposes: retrieval and chunking parameters, their safe
// defaults, ranges, groups, apply modes and labels. Server validation, the
// OpenAPI schema document and the workbench forms all read this package, so a
// new parameter is declared once instead of in the YAML defaults, the override
// DTO, the settings page and the contract separately.
package configschema

import (
	"encoding/json"

	"github.com/hzc19970630/rag-platform/internal/apperror"
)

const (
	DomainRetrieval = "retrieval"
	DomainChunking  = "chunking"
)

const (
	SourceDefault       = "default"
	SourceDeployment    = "deployment"
	SourceTenant        = "tenant"
	SourceKnowledgeBase = "knowledge_base"
	SourceSession       = "session"
)

const (
	ApplyNextRetrieval = "next_retrieval"
	ApplyNextIngest    = "next_ingest"
)

const (
	SensitivityNormal = "normal"
	SensitivityHigh   = "high"
)

// Impact describes when a change takes effect and whether data must be rebuilt.
const (
	ImpactHot        = "hot"
	ImpactNextIngest = "next_ingest"
	ImpactRebuild    = "rebuild"
	ImpactRestart    = "restart"
)

// RebuildScope names the data that must be rebuilt for an impact=rebuild field.
const (
	RebuildNone    = "none"
	RebuildIndex   = "index"
	RebuildRechunk = "rechunk"
	RebuildReembed = "reembed"
	RebuildFull    = "full"
)

const (
	RiskNormal = "normal"
	RiskHigh   = "high"
)

// Kind is the JSON value type of one field.
type Kind string

const (
	KindInt        Kind = "int"
	KindFloat      Kind = "float"
	KindBool       Kind = "bool"
	KindStringList Kind = "string_list"
)

// Field is one business configuration parameter. Default is stored as raw JSON
// so one declaration covers int, float, bool and string-list parameters.
type Field struct {
	Key            string          `json:"key"`
	Domain         string          `json:"domain"`
	Group          string          `json:"group"`
	Kind           Kind            `json:"kind"`
	Default        json.RawMessage `json:"default"`
	Min            *float64        `json:"min,omitempty"`
	Max            *float64        `json:"max,omitempty"`
	Step           *float64        `json:"step,omitempty"`
	UnitKey        string          `json:"unitKey,omitempty"`
	ApplyMode      string          `json:"applyMode"`
	Impact         string          `json:"impact"`
	RebuildScope   string          `json:"rebuildScope"`
	Risk           string          `json:"risk"`
	Sensitivity    string          `json:"sensitivity"`
	LabelKey       string          `json:"labelKey"`
	DescriptionKey string          `json:"descriptionKey,omitempty"`
}

// State reports one effective value with the tier it came from.
type State struct {
	Value      json.RawMessage `json:"value"`
	Default    json.RawMessage `json:"default"`
	Deployment json.RawMessage `json:"deployment,omitempty"`
	Source     string          `json:"source"`
	ApplyMode  string          `json:"applyMode"`
}

// Tier is one configuration layer. Values holds the keys the layer states;
// Explicit records which keys an operator actually set, so a value equal to the
// safe default is still reported as coming from the default tier.
type Tier struct {
	Values   map[string]json.RawMessage
	Explicit map[string]bool
}

// NewTier encodes native Go values into a tier. A nil explicit set marks every
// value as operator-set; a non-nil set limits the deployment tier to the keys
// the operator actually stated.
func NewTier(values map[string]any, explicit map[string]bool) Tier {
	tier := Tier{Values: map[string]json.RawMessage{}, Explicit: map[string]bool{}}
	for key, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		tier.Values[key] = encoded
		if explicit == nil {
			tier.Explicit[key] = true
		}
	}
	for key := range explicit {
		tier.Explicit[key] = true
	}
	return tier
}

func raw(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func ptr(value float64) *float64 { return &value }

var fields = []Field{
	// retrieval: recall
	{Key: "top_k", Domain: DomainRetrieval, Group: "fusion", Kind: KindInt, Default: raw(8), Min: ptr(0), Max: ptr(200), Step: ptr(1), UnitKey: "retrieval.unitCount", ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.top_k"},
	{Key: "embedding_top_k", Domain: DomainRetrieval, Group: "recall", Kind: KindInt, Default: raw(20), Min: ptr(0), Max: ptr(200), Step: ptr(1), UnitKey: "retrieval.unitCount", ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.embedding_top_k"},
	{Key: "keyword_top_k", Domain: DomainRetrieval, Group: "recall", Kind: KindInt, Default: raw(20), Min: ptr(0), Max: ptr(200), Step: ptr(1), UnitKey: "retrieval.unitCount", ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.keyword_top_k"},
	{Key: "vector_threshold", Domain: DomainRetrieval, Group: "recall", Kind: KindFloat, Default: raw(0.2), Min: ptr(0), Max: ptr(1), Step: ptr(0.01), ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.vector_threshold"},
	{Key: "keyword_threshold", Domain: DomainRetrieval, Group: "recall", Kind: KindFloat, Default: raw(0), Min: ptr(0), Step: ptr(0.01), ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.keyword_threshold"},
	{Key: "vector_ef_search", Domain: DomainRetrieval, Group: "recall", Kind: KindInt, Default: raw(0), Min: ptr(0), Max: ptr(1000), Step: ptr(1), ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.vector_ef_search"},
	{Key: "vector_oversample", Domain: DomainRetrieval, Group: "recall", Kind: KindInt, Default: raw(1), Min: ptr(1), Max: ptr(20), Step: ptr(1), ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.vector_oversample"},
	{Key: "vector_iterative_scan", Domain: DomainRetrieval, Group: "recall", Kind: KindBool, Default: raw(true), ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.vector_iterative_scan"},
	// retrieval: fusion
	{Key: "rrf_k", Domain: DomainRetrieval, Group: "fusion", Kind: KindInt, Default: raw(60), Min: ptr(1), Max: ptr(200), Step: ptr(1), UnitKey: "retrieval.unitRrf", ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.rrf_k"},
	// retrieval: expansion
	{Key: "adjacent_expansion", Domain: DomainRetrieval, Group: "expansion", Kind: KindInt, Default: raw(1), Min: ptr(0), Step: ptr(1), UnitKey: "retrieval.unitChunks", ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.adjacent_expansion"},
	{Key: "max_adjacent_chunks", Domain: DomainRetrieval, Group: "expansion", Kind: KindInt, Default: raw(2), Min: ptr(0), Step: ptr(1), UnitKey: "retrieval.unitChunks", ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.max_adjacent_chunks"},
	{Key: "expansion_score_factor", Domain: DomainRetrieval, Group: "expansion", Kind: KindFloat, Default: raw(0.5), Min: ptr(0), Max: ptr(1), Step: ptr(0.05), ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.expansion_score_factor"},
	// retrieval: rerank
	{Key: "rerank_enabled", Domain: DomainRetrieval, Group: "rerank", Kind: KindBool, Default: raw(false), ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, Risk: RiskHigh, LabelKey: "retrieval.rerank_enabled"},
	{Key: "rerank_top_k", Domain: DomainRetrieval, Group: "rerank", Kind: KindInt, Default: raw(8), Min: ptr(0), Max: ptr(200), Step: ptr(1), UnitKey: "retrieval.unitCount", ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.rerank_top_k"},
	{Key: "rerank_threshold", Domain: DomainRetrieval, Group: "rerank", Kind: KindFloat, Default: raw(-10), Min: ptr(-10), Max: ptr(10), Step: ptr(0.01), ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.rerank_threshold"},
	// retrieval: context
	{Key: "context_token_limit", Domain: DomainRetrieval, Group: "context", Kind: KindInt, Default: raw(3000), Min: ptr(1), Step: ptr(1), UnitKey: "retrieval.unitTokens", ApplyMode: ApplyNextRetrieval, Sensitivity: SensitivityNormal, LabelKey: "retrieval.context_token_limit"},
	// chunking
	{Key: "size", Domain: DomainChunking, Group: "chunking", Kind: KindInt, Default: raw(900), Min: ptr(1), Step: ptr(1), UnitKey: "chunking.unitChars", ApplyMode: ApplyNextIngest, Sensitivity: SensitivityNormal, LabelKey: "chunking.size"},
	{Key: "overlap", Domain: DomainChunking, Group: "chunking", Kind: KindInt, Default: raw(120), Min: ptr(0), Step: ptr(1), UnitKey: "chunking.unitChars", ApplyMode: ApplyNextIngest, Sensitivity: SensitivityNormal, LabelKey: "chunking.overlap"},
	{Key: "min_size", Domain: DomainChunking, Group: "chunking", Kind: KindInt, Default: raw(0), Min: ptr(0), Step: ptr(1), UnitKey: "chunking.unitChars", ApplyMode: ApplyNextIngest, Sensitivity: SensitivityNormal, LabelKey: "chunking.minSize"},
	{Key: "split_markers", Domain: DomainChunking, Group: "chunking", Kind: KindStringList, Default: raw([]string{"\n\n", "\n", "。", ".", "！", "!", "？", "?"}), ApplyMode: ApplyNextIngest, Sensitivity: SensitivityNormal, LabelKey: "chunking.splitMarkers"},
}

// fillImpact keeps the declaration terse: retrieval parameters are query-time
// and chunking parameters apply to the next ingestion. A future data-contract
// field states Impact/RebuildScope explicitly.
func init() {
	for index := range fields {
		if fields[index].Impact == "" {
			if fields[index].Domain == DomainChunking {
				fields[index].Impact = ImpactNextIngest
				fields[index].RebuildScope = RebuildRechunk
			} else {
				fields[index].Impact = ImpactHot
				fields[index].RebuildScope = RebuildNone
			}
		}
		if fields[index].RebuildScope == "" {
			fields[index].RebuildScope = RebuildNone
		}
		if fields[index].Risk == "" {
			fields[index].Risk = RiskNormal
		}
	}
}

// FieldsFor returns the fields of one domain in declaration order.
func FieldsFor(domain string) []Field {
	result := make([]Field, 0, len(fields))
	for _, field := range fields {
		if field.Domain == domain {
			result = append(result, field)
		}
	}
	return result
}

// FieldByKey looks a field up inside one domain.
func FieldByKey(domain, key string) (Field, bool) {
	for _, field := range fields {
		if field.Domain == domain && field.Key == key {
			return field, true
		}
	}
	return Field{}, false
}

func defaultOf(key string) json.RawMessage {
	for _, field := range fields {
		if field.Key == key {
			return field.Default
		}
	}
	panic("configschema: unknown default " + key)
}

// IntDefault, FloatDefault and BoolDefault expose typed safe defaults so the
// config package does not repeat them.
func IntDefault(key string) int {
	var value int
	if err := json.Unmarshal(defaultOf(key), &value); err != nil {
		panic("configschema: " + key + " is not an int")
	}
	return value
}

func FloatDefault(key string) float64 {
	var value float64
	if err := json.Unmarshal(defaultOf(key), &value); err != nil {
		panic("configschema: " + key + " is not a float")
	}
	return value
}

func BoolDefault(key string) bool {
	var value bool
	if err := json.Unmarshal(defaultOf(key), &value); err != nil {
		panic("configschema: " + key + " is not a bool")
	}
	return value
}

func StringsDefault(key string) []string {
	var value []string
	if err := json.Unmarshal(defaultOf(key), &value); err != nil {
		panic("configschema: " + key + " is not a string list")
	}
	return value
}

// Validate checks a partial or complete layer for one domain. Unknown keys are
// rejected so a typo in a knowledge-base override or a stale stored document is
// reported instead of silently ignored.
func Validate(domain string, values map[string]json.RawMessage) error {
	decoded := map[string]any{}
	for key, value := range values {
		field, ok := FieldByKey(domain, key)
		if !ok {
			return apperror.Invalidf("unknown %s parameter %q", domain, key)
		}
		parsed, err := decode(field, value)
		if err != nil {
			return err
		}
		decoded[key] = parsed
	}
	if domain == DomainChunking {
		return validateChunkingRelations(decoded)
	}
	return nil
}

// ValidateNative validates native Go values, which is how the typed config and
// override structs feed the same schema.
func ValidateNative(domain string, values map[string]any) error {
	encoded := map[string]json.RawMessage{}
	for key, value := range values {
		encodedValue, err := json.Marshal(value)
		if err != nil {
			return apperror.Invalidf("%s has an invalid value: %v", key, err)
		}
		encoded[key] = encodedValue
	}
	return Validate(domain, encoded)
}

func decode(field Field, rawValue json.RawMessage) (any, error) {
	switch field.Kind {
	case KindInt:
		var number json.Number
		if err := json.Unmarshal(rawValue, &number); err != nil {
			return nil, apperror.Invalidf("%s must be an integer", field.Key)
		}
		value, err := number.Int64()
		if err != nil {
			return nil, apperror.Invalidf("%s must be an integer", field.Key)
		}
		if err := validateRange(field, float64(value), true); err != nil {
			return nil, err
		}
		return value, nil
	case KindFloat:
		var number json.Number
		if err := json.Unmarshal(rawValue, &number); err != nil {
			return nil, apperror.Invalidf("%s must be a number", field.Key)
		}
		value, err := number.Float64()
		if err != nil {
			return nil, apperror.Invalidf("%s must be a number", field.Key)
		}
		if err := validateRange(field, value, false); err != nil {
			return nil, err
		}
		return value, nil
	case KindBool:
		var value bool
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return nil, apperror.Invalidf("%s must be a boolean", field.Key)
		}
		return value, nil
	case KindStringList:
		var value []string
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return nil, apperror.Invalidf("%s must be a string list", field.Key)
		}
		for _, item := range value {
			if item == "" {
				return nil, apperror.Invalidf("%s must not contain empty strings", field.Key)
			}
		}
		return value, nil
	default:
		return nil, apperror.Invalidf("%s has an unsupported type", field.Key)
	}
}

func validateRange(field Field, value float64, integer bool) error {
	if field.Max != nil {
		minimum := 0.0
		if field.Min != nil {
			minimum = *field.Min
		}
		if value < minimum || value > *field.Max {
			if integer {
				return apperror.Invalidf("%s must be between %d and %d, got %d", field.Key, int(minimum), int(*field.Max), int(value))
			}
			return apperror.Invalidf("%s must be between %v and %v, got %v", field.Key, minimum, *field.Max, value)
		}
		return nil
	}
	if field.Min == nil {
		return nil
	}
	switch {
	case *field.Min == 0 && value < 0:
		return apperror.Invalidf("%s must not be negative", field.Key)
	case *field.Min == 1 && integer && value <= 0:
		return apperror.Invalidf("%s must be a positive integer", field.Key)
	case value < *field.Min:
		return apperror.Invalidf("%s must be at least %v, got %v", field.Key, *field.Min, value)
	}
	return nil
}

func validateChunkingRelations(values map[string]any) error {
	size, hasSize := values["size"]
	if !hasSize {
		return nil
	}
	sizeValue, ok := size.(int64)
	if !ok {
		return nil
	}
	if overlap, ok := values["overlap"].(int64); ok && overlap >= sizeValue {
		return apperror.Invalidf("overlap must be smaller than size, got %d and %d", overlap, sizeValue)
	}
	if minSize, ok := values["min_size"].(int64); ok && minSize >= sizeValue {
		return apperror.Invalidf("min_size must be smaller than size, got %d and %d", minSize, sizeValue)
	}
	return nil
}

// Resolve reports the effective value of every field in one domain. Tiers are
// applied in order: schema default, deployment, tenant, knowledge base,
// session. A deployment key only counts when the operator actually set it.
func Resolve(domain string, deployment Tier, tenant, knowledgeBase, session map[string]json.RawMessage) map[string]State {
	result := map[string]State{}
	for _, field := range FieldsFor(domain) {
		state := State{Value: field.Default, Default: field.Default, Source: SourceDefault, ApplyMode: field.ApplyMode}
		if value, ok := deployment.Values[field.Key]; ok && deployment.Explicit[field.Key] {
			state.Value = value
			state.Deployment = value
			state.Source = SourceDeployment
		}
		if value, ok := tenant[field.Key]; ok {
			state.Value = value
			state.Source = SourceTenant
		}
		if value, ok := knowledgeBase[field.Key]; ok {
			state.Value = value
			state.Source = SourceKnowledgeBase
		}
		if value, ok := session[field.Key]; ok {
			state.Value = value
			state.Source = SourceSession
		}
		result[field.Key] = state
	}
	return result
}
