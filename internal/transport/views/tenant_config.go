package views

import (
	"encoding/json"

	"github.com/hzc19970630/rag-platform/contracts/httpapi"
	"github.com/hzc19970630/rag-platform/internal/confighealth"
	"github.com/hzc19970630/rag-platform/internal/configschema"
	"github.com/hzc19970630/rag-platform/internal/tenantconfig"
)

// TenantConfiguration maps the tenant-tier view to the wire shape. The schema
// is sent with the values so the workbench renders fields from one source
// instead of a hand-written field table.
func TenantConfiguration(view tenantconfig.View) httpapi.TenantConfiguration {
	return httpapi.TenantConfiguration{
		SchemaVersion: tenantconfig.SchemaVersion,
		Retrieval:     ConfigDomain(view.Retrieval),
		Chunking:      ConfigDomain(view.Chunking),
	}
}

func ConfigDomain(view tenantconfig.DomainView) httpapi.ConfigDomain {
	return httpapi.ConfigDomain{
		Schema:             ConfigFieldDefinitions(view.Fields),
		DeploymentDefaults: cloneRaw(view.Deployment.Values),
		TenantOverride:     cloneRaw(view.TenantOverride),
		Effective:          cloneRaw(view.Effective),
		Fields:             ConfigFieldStates(view.States),
	}
}

func ConfigFieldDefinitions(fields []configschema.Field) []httpapi.ConfigFieldDefinition {
	if len(fields) == 0 {
		return nil
	}
	items := make([]httpapi.ConfigFieldDefinition, 0, len(fields))
	for _, field := range fields {
		items = append(items, httpapi.ConfigFieldDefinition{
			Key: field.Key, Domain: field.Domain, Group: field.Group, Kind: string(field.Kind),
			Default: field.Default, Min: field.Min, Max: field.Max, Step: field.Step,
			UnitKey: field.UnitKey, ApplyMode: field.ApplyMode, Sensitivity: field.Sensitivity,
			LabelKey: field.LabelKey, DescriptionKey: field.DescriptionKey,
		})
	}
	return items
}

func ConfigFieldStates(states map[string]configschema.State) map[string]httpapi.ConfigFieldState {
	if len(states) == 0 {
		return nil
	}
	result := make(map[string]httpapi.ConfigFieldState, len(states))
	for key, state := range states {
		result[key] = httpapi.ConfigFieldState{
			Value: state.Value, Default: state.Default, Deployment: state.Deployment,
			Source: state.Source, ApplyMode: state.ApplyMode,
		}
	}
	return result
}

func cloneRaw(values map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// ConfigHealth maps the health report to the wire shape.
func ConfigHealth(report confighealth.Report) httpapi.ConfigHealth {
	checks := make([]httpapi.ConfigHealthCheck, 0, len(report.Checks))
	for _, check := range report.Checks {
		checks = append(checks, httpapi.ConfigHealthCheck{
			ID: check.ID, Status: check.Status, Severity: check.Severity,
			Summary: check.Summary, Detail: check.Detail, Remediation: check.Remediation,
			CheckedAt: check.CheckedAt,
		})
	}
	return httpapi.ConfigHealth{
		Status: report.Status, HighRisk: report.HighRisk, Checks: checks, GeneratedAt: report.GeneratedAt,
	}
}

func ConfigConnectionResult(result confighealth.ConnectionResult) httpapi.ConfigConnectionTestResult {
	return httpapi.ConfigConnectionTestResult{
		Healthy: result.Healthy, Error: result.Error, Dimension: result.Dimension, CheckedAt: result.CheckedAt,
	}
}
