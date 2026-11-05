package relation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/example/rag-platform/internal/contracts/evidence"
	"github.com/example/rag-platform/internal/contracts/model"
	"github.com/example/rag-platform/internal/contracts/projectioncache"
	"github.com/example/rag-platform/internal/prompts"
)

const maxRelationChunksPerCall = 8

// Extractor is the Relation Projection generator. It asks the configured chat
// adapter for RelationProjectionPackageV1 JSON and always merges deterministic
// [[subject|predicate|object]] triples. The deterministic path keeps local and
// acceptance runs reproducible; a remote model can extract natural-language
// relations in the same schema.
type Extractor struct {
	chatter       Chatter
	template      string
	promptVersion string
	cache         projectioncache.Cache
}

// ExtractStats reports package-level cache effectiveness for one run.
type ExtractStats struct {
	Generated int
	Reused    int
}

// SetCache enables per-package extraction caching. A nil cache keeps the
// previous full-extraction behavior.
func (e *Extractor) SetCache(cache projectioncache.Cache) { e.cache = cache }

func NewExtractor(chatter Chatter, registry *prompts.Registry) (*Extractor, error) {
	if chatter == nil {
		return nil, fmt.Errorf("relation extraction requires a chat adapter")
	}
	if registry == nil {
		return nil, fmt.Errorf("relation extraction requires prompt templates")
	}
	template, ok := registry.Raw(prompts.RelationExtraction)
	if !ok || strings.TrimSpace(template) == "" {
		return nil, fmt.Errorf("prompt template %q is required for relation extraction", prompts.RelationExtraction)
	}
	digest := sha256.Sum256([]byte(template))
	return &Extractor{
		chatter: chatter, template: template,
		promptVersion: prompts.RelationExtraction + "/" + hex.EncodeToString(digest[:])[:12],
	}, nil
}

func (e *Extractor) PromptVersion() string { return e.promptVersion }
func (e *Extractor) Model() string         { return e.chatter.Name() }

// Extract consumes only EvidencePackage chunks. Relations produced by the model
// without a literal source chunk are dropped; the deterministic triples carry
// the chunk they were read from.
func (e *Extractor) Extract(ctx context.Context, tenantID, knowledgeBaseID string, aclVersion int64, packages []evidence.Package) (RelationProjectionPackage, ExtractStats, error) {
	result := RelationProjectionPackage{
		SchemaVersion: SchemaRelationProjection, TenantID: tenantID,
		KnowledgeBaseID: knowledgeBaseID, ACLVersion: aclVersion,
	}
	stats := ExtractStats{}
	sorted := append([]evidence.Package(nil), packages...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].DocumentID < sorted[j].DocumentID })
	for _, pkg := range sorted {
		if len(evidenceChunks(pkg)) == 0 {
			continue
		}
		key := e.cacheKey(pkg)
		if payload, ok := e.cached(ctx, key); ok {
			var extracted RelationProjectionPackage
			if err := json.Unmarshal(payload, &extracted); err == nil {
				result.Entities = append(result.Entities, extracted.Entities...)
				result.Relations = append(result.Relations, extracted.Relations...)
				stats.Reused++
				continue
			}
			slog.Warn("relation_cache_decode_failed", "package_id", pkg.ID)
		}
		extracted, err := e.ExtractPackage(ctx, pkg)
		if err != nil {
			return RelationProjectionPackage{}, stats, err
		}
		result.Entities = append(result.Entities, extracted.Entities...)
		result.Relations = append(result.Relations, extracted.Relations...)
		e.store(ctx, key, extracted)
		stats.Generated++
	}
	return MergeExtraction(result), stats, nil
}

// ExtractPackage extracts one EvidencePackage's batches and deterministic
// fallback triples. The global MergeExtraction still runs once over all
// packages, so a cached package result is indistinguishable from a fresh one.
func (e *Extractor) ExtractPackage(ctx context.Context, pkg evidence.Package) (RelationProjectionPackage, error) {
	result := RelationProjectionPackage{
		SchemaVersion: SchemaRelationProjection, TenantID: pkg.TenantID,
		KnowledgeBaseID: pkg.KnowledgeBaseID, ACLVersion: pkg.ACLVersion,
	}
	chunks := evidenceChunks(pkg)
	for start := 0; start < len(chunks); start += maxRelationChunksPerCall {
		end := start + maxRelationChunksPerCall
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[start:end]
		extracted, err := e.extractBatch(ctx, pkg, batch)
		if err != nil {
			return RelationProjectionPackage{}, err
		}
		fallback := DeterministicExtraction(batch)
		result.Entities = append(result.Entities, extracted.Entities...)
		result.Entities = append(result.Entities, fallback.Entities...)
		result.Relations = append(result.Relations, extracted.Relations...)
		result.Relations = append(result.Relations, fallback.Relations...)
	}
	return result, nil
}

func (e *Extractor) cacheKey(pkg evidence.Package) projectioncache.Key {
	digest := sha256.New()
	for _, part := range []string{
		"relation", pkg.ID, projectioncache.PackageDigest(pkg),
		e.chatter.Name(), e.promptVersion, fmt.Sprint(pkg.ACLVersion),
	} {
		digest.Write([]byte(part))
		digest.Write([]byte{0})
	}
	return projectioncache.Key{
		TenantID: pkg.TenantID, KnowledgeBaseID: pkg.KnowledgeBaseID,
		ProjectionType: "relation", Artifact: "extraction", PackageID: pkg.ID,
		InputHash:     hex.EncodeToString(digest.Sum(nil)),
		PromptVersion: e.promptVersion, Model: e.chatter.Name(), ACLVersion: pkg.ACLVersion,
	}
}

func (e *Extractor) cached(ctx context.Context, key projectioncache.Key) ([]byte, bool) {
	if e.cache == nil {
		return nil, false
	}
	payload, ok, err := e.cache.Get(ctx, key)
	if err != nil {
		slog.Warn("relation_cache_get_failed", "package_id", key.PackageID, "error", err)
		return nil, false
	}
	return payload, ok
}

func (e *Extractor) store(ctx context.Context, key projectioncache.Key, value any) {
	if e.cache == nil {
		return
	}
	payload, err := json.Marshal(value)
	if err != nil {
		slog.Warn("relation_cache_encode_failed", "package_id", key.PackageID, "error", err)
		return
	}
	if err := e.cache.Put(ctx, key, payload); err != nil {
		slog.Warn("relation_cache_put_failed", "package_id", key.PackageID, "error", err)
	}
}

func (e *Extractor) extractBatch(ctx context.Context, pkg evidence.Package, chunks []evidence.Chunk) (RelationProjectionPackage, error) {
	evidence := make([]model.EvidenceItem, 0, len(chunks))
	byIndex := map[int]string{}
	for index, chunk := range chunks {
		evidence = append(evidence, model.EvidenceItem{Index: index + 1, ChunkID: chunk.ID, HeadingPath: chunk.HeadingPath, Text: chunk.Content})
		byIndex[index+1] = chunk.ID
	}
	response, err := e.chatter.Complete(ctx, model.ChatRequest{
		ContextTemplate: e.template,
		Query:           "Extract entities and relations from the evidence. Return the JSON object only.",
		Evidence:        evidence,
		MaxTokens:       1024,
	})
	if err != nil {
		return RelationProjectionPackage{}, fmt.Errorf("relation extraction call failed: %w", err)
	}
	parsed, ok := parseRelationJSON(response.Text)
	if !ok {
		return RelationProjectionPackage{}, nil
	}
	parsed.TenantID = pkg.TenantID
	parsed.KnowledgeBaseID = pkg.KnowledgeBaseID
	parsed.ACLVersion = pkg.ACLVersion
	for index := range parsed.Entities {
		if len(parsed.Entities[index].SourceChunkIDs) == 0 {
			parsed.Entities[index].SourceChunkIDs = chunksMentioning(chunks, parsed.Entities[index].Name)
		}
	}
	for index := range parsed.Relations {
		if len(parsed.Relations[index].SourceChunkIDs) == 0 {
			parsed.Relations[index].SourceChunkIDs = chunksMentioning(chunks, parsed.Relations[index].SubjectKey, parsed.Relations[index].ObjectKey)
		}
	}
	return parsed, nil
}

var (
	triplePattern = regexp.MustCompile(`\[\[\s*([^\[\]|]+?)\s*\|\s*([^\[\]|]+?)\s*\|\s*([^\[\]|]+?)\s*(?:\|\s*([^\[\]]*?)\s*)?\]\]`)
)

// deterministicExtraction reads explicit [[subject|predicate|object]] triples
// and optional fourth-field aliases ("Acme;ACME Inc"). It is intentionally
// literal: no entity is invented from prose, so every relation can cite the
// chunk it came from.
func DeterministicExtraction(chunks []evidence.Chunk) RelationProjectionPackage {
	result := RelationProjectionPackage{SchemaVersion: SchemaRelationProjection}
	for _, chunk := range chunks {
		for _, match := range triplePattern.FindAllStringSubmatch(chunk.Content, -1) {
			subject := strings.TrimSpace(match[1])
			predicate := strings.TrimSpace(match[2])
			object := strings.TrimSpace(match[3])
			if subject == "" || predicate == "" || object == "" || strings.EqualFold(subject, "alias") {
				continue
			}
			var aliases []string
			if len(match) > 4 && strings.TrimSpace(match[4]) != "" {
				for _, alias := range strings.Split(match[4], ";") {
					if alias = strings.TrimSpace(alias); alias != "" {
						aliases = append(aliases, alias)
					}
				}
			}
			result.Entities = append(result.Entities,
				ExtractedEntity{Name: subject, Aliases: aliases, SourceChunkIDs: []string{chunk.ID}},
				ExtractedEntity{Name: object, SourceChunkIDs: []string{chunk.ID}},
			)
			result.Relations = append(result.Relations, ExtractedRelation{
				SubjectKey: subject, Predicate: predicate, ObjectKey: object,
				Confidence: 1, SourceChunkIDs: []string{chunk.ID},
			})
		}
	}
	return result
}

func parseRelationJSON(text string) (RelationProjectionPackage, bool) {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return RelationProjectionPackage{}, false
	}
	var parsed RelationProjectionPackage
	if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err != nil {
		return RelationProjectionPackage{}, false
	}
	return parsed, true
}

func chunksMentioning(chunks []evidence.Chunk, names ...string) []string {
	terms := make([]string, 0, len(names))
	for _, name := range names {
		if term := strings.ToLower(strings.TrimSpace(name)); term != "" {
			terms = append(terms, term)
		}
	}
	var ids []string
	for _, chunk := range chunks {
		lowered := strings.ToLower(chunk.Content)
		for _, term := range terms {
			if strings.Contains(lowered, term) {
				ids = append(ids, chunk.ID)
				break
			}
		}
	}
	return ids
}

// mergeExtraction normalizes entity names, merges aliases and duplicates, then
// rewrites relations to canonical keys. The rules are deterministic and each
// alias row records why it belongs to an entity.
func MergeExtraction(raw RelationProjectionPackage) RelationProjectionPackage {
	type entityAccumulator struct {
		name       string
		display    string
		entityType string
		aliases    map[string]string // normalized -> original
		aliasRules map[string]string
		sources    map[string]bool
		properties map[string]string
	}
	entities := map[string]*entityAccumulator{}
	parent := map[string]string{}
	var find func(string) string
	find = func(value string) string {
		root, ok := parent[value]
		if !ok || root == value {
			return value
		}
		resolved := find(root)
		parent[value] = resolved
		return resolved
	}
	union := func(left, right string) {
		leftRoot, rightRoot := find(left), find(right)
		if leftRoot == rightRoot {
			return
		}
		if leftRoot < rightRoot {
			parent[rightRoot] = leftRoot
		} else {
			parent[leftRoot] = rightRoot
		}
	}
	ensure := func(name string, explicit bool) *entityAccumulator {
		key := NormalizeEntityKey(name)
		if key == "" {
			return nil
		}
		if _, ok := parent[key]; !ok {
			parent[key] = key
		}
		accumulator, ok := entities[key]
		if !ok {
			accumulator = &entityAccumulator{name: strings.TrimSpace(name), aliases: map[string]string{}, aliasRules: map[string]string{}, sources: map[string]bool{}, properties: map[string]string{}}
			entities[key] = accumulator
		}
		if explicit && accumulator.display == "" {
			accumulator.display = strings.TrimSpace(name)
		}
		return accumulator
	}
	for _, entity := range raw.Entities {
		name := strings.TrimSpace(entity.Name)
		if name == "" {
			name = strings.TrimSpace(entity.Key)
		}
		accumulator := ensure(name, true)
		if accumulator == nil {
			continue
		}
		if accumulator.entityType == "" {
			accumulator.entityType = strings.TrimSpace(entity.Type)
		}
		for key, value := range entity.Properties {
			if _, exists := accumulator.properties[key]; !exists {
				accumulator.properties[key] = value
			}
		}
		for _, source := range entity.SourceChunkIDs {
			accumulator.sources[source] = true
		}
		for _, alias := range entity.Aliases {
			normalized := NormalizeEntityKey(alias)
			if normalized == "" {
				continue
			}
			accumulator.aliases[normalized] = strings.TrimSpace(alias)
			accumulator.aliasRules[normalized] = "declared_alias"
			ensure(alias, false)
			union(NormalizeEntityKey(name), normalized)
		}
	}
	// A relation endpoint is an entity even when the model omitted the entity
	// list. This keeps the graph closed, while sources still gate visibility.
	for _, relation := range raw.Relations {
		ensure(relation.SubjectKey, true)
		ensure(relation.ObjectKey, true)
	}

	components := map[string][]string{}
	for key := range entities {
		root := find(key)
		components[root] = append(components[root], key)
	}
	result := RelationProjectionPackage{SchemaVersion: SchemaRelationProjection, TenantID: raw.TenantID, KnowledgeBaseID: raw.KnowledgeBaseID, ACLVersion: raw.ACLVersion}
	for root, members := range components {
		sort.Strings(members)
		display := ""
		aliases := map[string]string{}
		aliasRules := map[string]string{}
		entityType := ""
		properties := map[string]string{}
		sources := map[string]bool{}
		for _, member := range members {
			accumulator := entities[member]
			if accumulator.display != "" {
				if display == "" || strings.ToLower(accumulator.display) < strings.ToLower(display) {
					display = accumulator.display
				}
			}
			if entityType == "" {
				entityType = accumulator.entityType
			}
			for key, value := range accumulator.properties {
				if _, exists := properties[key]; !exists {
					properties[key] = value
				}
			}
			for source := range accumulator.sources {
				sources[source] = true
			}
			if member != root {
				// A key that was already recorded as a declared alias keeps that
				// rule; the structural merge must not downgrade its audit reason.
				if _, declared := aliasRules[member]; !declared {
					aliases[member] = accumulator.name
					aliasRules[member] = "normalized_merge"
				}
			}
			for normalized, alias := range accumulator.aliases {
				if normalized != root {
					aliases[normalized] = alias
					aliasRules[normalized] = accumulator.aliasRules[normalized]
					if aliasRules[normalized] == "" {
						aliasRules[normalized] = "declared_alias"
					}
				}
			}
		}
		if display == "" {
			display = root
		}
		aliasValues := make([]string, 0, len(aliases))
		for _, alias := range aliases {
			aliasValues = append(aliasValues, alias)
		}
		sort.Strings(aliasValues)
		sourceIDs := mapKeys(sources)
		result.Entities = append(result.Entities, ExtractedEntity{
			Key: root, Name: display, Type: entityType, Aliases: aliasValues,
			AliasRules: aliasRules, Properties: properties, SourceChunkIDs: sourceIDs,
		})
	}
	sort.SliceStable(result.Entities, func(i, j int) bool { return result.Entities[i].Key < result.Entities[j].Key })

	relations := map[string]ExtractedRelation{}
	for _, relation := range raw.Relations {
		subject := find(NormalizeEntityKey(relation.SubjectKey))
		object := find(NormalizeEntityKey(relation.ObjectKey))
		predicate := strings.TrimSpace(relation.Predicate)
		if subject == "" || object == "" || predicate == "" {
			continue
		}
		key := subject + "\x00" + strings.ToLower(predicate) + "\x00" + object
		merged, exists := relations[key]
		if !exists {
			merged = ExtractedRelation{
				SubjectKey: subject, Predicate: predicate, ObjectKey: object,
				Properties: map[string]string{}, Confidence: relation.Confidence,
			}
		}
		if merged.Confidence == 0 || (relation.Confidence > 0 && relation.Confidence < merged.Confidence) {
			merged.Confidence = relation.Confidence
		}
		if merged.Confidence == 0 {
			merged.Confidence = 1
		}
		for property, value := range relation.Properties {
			if _, ok := merged.Properties[property]; !ok {
				merged.Properties[property] = value
			}
		}
		seen := map[string]bool{}
		for _, source := range merged.SourceChunkIDs {
			seen[source] = true
		}
		for _, source := range relation.SourceChunkIDs {
			if source != "" && !seen[source] {
				merged.SourceChunkIDs = append(merged.SourceChunkIDs, source)
				seen[source] = true
			}
		}
		relations[key] = merged
	}
	for _, relation := range relations {
		if len(relation.SourceChunkIDs) == 0 {
			continue
		}
		result.Relations = append(result.Relations, relation)
	}
	sort.SliceStable(result.Relations, func(i, j int) bool {
		left, right := result.Relations[i], result.Relations[j]
		if left.SubjectKey != right.SubjectKey {
			return left.SubjectKey < right.SubjectKey
		}
		if left.Predicate != right.Predicate {
			return left.Predicate < right.Predicate
		}
		return left.ObjectKey < right.ObjectKey
	})
	return result
}

func NormalizeEntityKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	space := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			space = false
			continue
		}
		if unicode.IsSpace(r) || r == '-' || r == '_' || r == '.' {
			if builder.Len() > 0 && !space {
				builder.WriteRune(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(builder.String())
}

func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func evidenceChunks(pkg evidence.Package) []evidence.Chunk {
	chunks := make([]evidence.Chunk, 0, len(pkg.Chunks))
	for _, chunk := range pkg.Chunks {
		if chunk.ParentID == "" || strings.TrimSpace(chunk.Content) == "" {
			continue
		}
		chunks = append(chunks, chunk)
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].Ordinal != chunks[j].Ordinal {
			return chunks[i].Ordinal < chunks[j].Ordinal
		}
		return chunks[i].ID < chunks[j].ID
	})
	return chunks
}
