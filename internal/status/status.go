// Package status holds lifecycle values shared by SQL recall, projections and
// workers. Keeping them in one package prevents a status rename from silently
// missing a query.
package status

const (
	EvidenceReady = "EVIDENCE_READY"
	Parent        = "PARENT"
	Stale         = "STALE"
)
