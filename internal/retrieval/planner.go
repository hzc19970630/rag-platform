package retrieval

import "context"

// EvidencePlanner is the default single-plan implementation: every resolved
// query becomes an evidence-only plan. History is recorded for diagnostics but
// does not rewrite the query; follow-up rewriting is a separate, opt-in plan.
type EvidencePlanner struct{}

func (EvidencePlanner) Plan(_ context.Context, input PlanInput) (QueryPlan, error) {
	var channels []string
	if input.Strategy.KeywordEnabled {
		channels = append(channels, ChannelKeyword)
	}
	if input.Strategy.VectorEnabled {
		channels = append(channels, ChannelVector)
	}
	return QueryPlan{
		SchemaVersion:             SchemaQueryPlan,
		RequestID:                 input.Query.RequestID,
		TenantID:                  input.Scope.TenantID,
		KnowledgeBaseIDs:          input.TargetKnowledgeBaseIDs,
		AuthorizationScopeVersion: input.Scope.ACLVersion,
		Intent:                    IntentFact,
		Mode:                      ModeEvidence,
		Rewrite:                   input.Query.Query,
		TopK:                      input.Params.TopK,
		VectorEnabled:             input.Strategy.VectorEnabled,
		KeywordEnabled:            input.Strategy.KeywordEnabled,
		RerankEnabled:             input.Params.RerankEnabled,
		Channels:                  channels,
		Fusion:                    FusionRRF,
		RequiredCapabilities:      input.Strategy.RequiredCapabilities,
		Params:                    input.Params,
		HistoryTurns:              len(input.History),
	}, nil
}
