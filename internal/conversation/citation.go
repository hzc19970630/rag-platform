package conversation

import (
	"regexp"
	"sort"

	"github.com/hzc19970630/rag-platform/internal/retrieval"
)

// citationPattern matches the short citation ids the model is allowed to use.
var citationPattern = regexp.MustCompile(`\[(\d{1,3})\]`)

// injectionPatterns detect instruction-like text inside retrieved documents.
// Document content is untrusted data: these patterns only raise a flag, they
// never change the permission or citation rules.
var injectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore (all )?(previous|above|prior) instructions`),
	regexp.MustCompile(`(?i)disregard (the )?(system|previous) (prompt|instructions)`),
	regexp.MustCompile(`(?i)you are now`),
	regexp.MustCompile(`忽略(以上|上述|之前|前面)?(所有)?(的)?(指令|提示|规则)`),
	regexp.MustCompile(`(忽视|无视)(以上|上述|之前|前面)(的)?(指令|提示|规则)`),
}

// Registry holds the citations registered during retrieval. Indexes are
// assigned here, so a model can only reference ids it was given.
type Registry struct {
	byIndex map[int]retrieval.Candidate
	order   []int
}

func NewRegistry(evidence retrieval.EvidenceSet) *Registry {
	registry := &Registry{byIndex: map[int]retrieval.Candidate{}}
	for position, candidate := range evidence.Items {
		index := position + 1
		registry.byIndex[index] = candidate
		registry.order = append(registry.order, index)
	}
	return registry
}

func (r *Registry) Len() int { return len(r.order) }

func (r *Registry) Lookup(index int) (retrieval.Candidate, bool) {
	candidate, ok := r.byIndex[index]
	return candidate, ok
}

// Indexes returns the registered citation ids in ascending order.
func (r *Registry) Indexes() []int {
	result := make([]int, len(r.order))
	copy(result, r.order)
	sort.Ints(result)
	return result
}

// ExtractIndexes returns the citation ids referenced by an answer, in first
// appearance order and without duplicates.
func ExtractIndexes(answer string) []int {
	seen := map[int]bool{}
	var indexes []int
	for _, match := range citationPattern.FindAllStringSubmatch(answer, -1) {
		index := 0
		for _, digit := range match[1] {
			index = index*10 + int(digit-'0')
		}
		if index == 0 || seen[index] {
			continue
		}
		seen[index] = true
		indexes = append(indexes, index)
	}
	return indexes
}

// Validate checks the answer against the registered citations and the evidence
// set. A citation that was not registered is rejected: the model may not
// invent references. The returned lists are always allocated so the JSON
// contract carries arrays instead of nulls.
func Validate(answer string, evidence retrieval.EvidenceSet) Validation {
	validation := validate(answer, evidence)
	if validation.CitedIndexes == nil {
		validation.CitedIndexes = []int{}
	}
	if validation.InvalidIndexes == nil {
		validation.InvalidIndexes = []int{}
	}
	if validation.CitedDocuments == nil {
		validation.CitedDocuments = []string{}
	}
	if validation.EvidenceDocuments == nil {
		validation.EvidenceDocuments = []string{}
	}
	return validation
}

func validate(answer string, evidence retrieval.EvidenceSet) Validation {
	registered := len(evidence.Items)
	cited := ExtractIndexes(answer)

	validation := Validation{
		RegisteredIndexes:  registered,
		CitedIndexes:       cited,
		EvidenceDocuments:  evidenceDocuments(evidence),
		InjectionSuspected: injectionSuspected(evidence),
	}
	for _, index := range cited {
		if index < 1 || index > registered {
			validation.InvalidIndexes = append(validation.InvalidIndexes, index)
		}
	}
	if len(validation.InvalidIndexes) > 0 {
		validation.Valid = false
		validation.Reason = CodeCitationRejected
		return validation
	}
	validation.CitedDocuments = citedDocuments(cited, evidence)
	validation.Ungrounded = len(cited) == 0
	if validation.Ungrounded {
		validation.Valid = false
		validation.Reason = CodeUngroundedAnswer
		return validation
	}
	// Conflicting sources are flagged, never silently resolved: when the
	// evidence spans several documents but the answer draws on only one, the
	// caller decides whether that is acceptable.
	validation.ConflictSuspected = len(validation.EvidenceDocuments) > 1 && len(validation.CitedDocuments) < len(validation.EvidenceDocuments)
	validation.Valid = true
	return validation
}

func evidenceDocuments(evidence retrieval.EvidenceSet) []string {
	seen := map[string]bool{}
	var documents []string
	for _, item := range evidence.Items {
		if item.DocumentID == "" || seen[item.DocumentID] {
			continue
		}
		seen[item.DocumentID] = true
		documents = append(documents, item.DocumentID)
	}
	sort.Strings(documents)
	return documents
}

func citedDocuments(indexes []int, evidence retrieval.EvidenceSet) []string {
	seen := map[string]bool{}
	var documents []string
	for _, index := range indexes {
		if index < 1 || index > len(evidence.Items) {
			continue
		}
		documentID := evidence.Items[index-1].DocumentID
		if documentID == "" || seen[documentID] {
			continue
		}
		seen[documentID] = true
		documents = append(documents, documentID)
	}
	sort.Strings(documents)
	return documents
}

func injectionSuspected(evidence retrieval.EvidenceSet) bool {
	for _, item := range evidence.Items {
		for _, pattern := range injectionPatterns {
			if pattern.MatchString(item.Content) {
				return true
			}
		}
	}
	return false
}
