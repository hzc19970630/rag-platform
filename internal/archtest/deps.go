// Package archtest validates the allowed package dependency edges. It is used
// by cmd/checkdeps in CI and by unit tests that prove an illegal edge fails.
package archtest

import "strings"

// Package is the subset of `go list -json` this check needs.
type Package struct {
	ImportPath string
	Imports    []string
}

// Violation is one illegal dependency edge.
type Violation struct {
	Package string
	Import  string
	Rule    string
}

const (
	RuleDomainConfig       = "domain must not import internal/config"
	RuleDomainComposition  = "domain must not import composition or transport"
	RuleDomainAdapter      = "domain must not import a provider adapter"
	RuleTransportApp       = "httpapi must not import internal/app"
	RuleTransportModels    = "httpapi must not import persistence models"
	RuleAdapterComposition = "platform adapters must not import composition or transport"
	RuleAdapterDomain      = "platform adapters may only import port-owner domains"
	RuleContractIsolation  = "contracts/httpapi must not import internal packages"
)

// providerAdapters are integration packages that own a provider SDK or wire
// protocol. Domains reach them through ports, never by import.
var providerAdapters = []string{
	"internal/platform/llm",
	"internal/platform/vector",
	"internal/platform/docreader",
	"internal/platform/storage",
	"internal/platform/queue",
}

// portPackages are the only domain-side packages platform adapters may import.
// ingestion/retrieval expose their port types through contracts/ports; the
// relationstore package is itself the port definition. Subpackages are not
// allowed, so an adapter cannot reach into an implementation file.
var portPackages = []string{
	"internal/contracts/ports",
	"internal/relationstore",
}

// Check returns every illegal edge in the graph. The module argument is the Go
// module path so the rules are independent of the checkout location.
func Check(module string, packages []Package) []Violation {
	root := strings.TrimSuffix(module, "/")
	var violations []Violation
	for _, pkg := range packages {
		rel, ok := trimPrefix(pkg.ImportPath, root+"/")
		if !ok {
			continue
		}
		for _, imported := range pkg.Imports {
			dep, ok := trimPrefix(imported, root+"/")
			if !ok {
				continue
			}
			if rule := classify(rel, dep); rule != "" {
				violations = append(violations, Violation{Package: pkg.ImportPath, Import: imported, Rule: rule})
			}
		}
	}
	return violations
}

func classify(pkg, dep string) string {
	switch {
	case hasPrefix(pkg, "contracts/httpapi"):
		if hasPrefix(dep, "internal") {
			return RuleContractIsolation
		}
	case isDomain(pkg):
		if dep == "internal/config" && !hasPrefix(pkg, "internal/knowledgebase") {
			return RuleDomainConfig
		}
		if isComposition(dep) {
			return RuleDomainComposition
		}
		if isProviderAdapter(dep) {
			return RuleDomainAdapter
		}
	case hasPrefix(pkg, "internal/httpapi"):
		if dep == "internal/app" {
			return RuleTransportApp
		}
		if hasPrefix(dep, "internal/platform/persistence/models") {
			return RuleTransportModels
		}
	case hasPrefix(pkg, "internal/platform"):
		if isComposition(dep) {
			return RuleAdapterComposition
		}
		if isDomain(dep) && !isPortOwner(dep) {
			return RuleAdapterDomain
		}
	}
	return ""
}

// isDomain treats every top-level internal package except the composition root,
// transport, shared contracts and the config loader as a domain package.
func isDomain(pkg string) bool {
	rest, ok := trimPrefix(pkg, "internal/")
	if !ok {
		return false
	}
	top := rest
	if index := strings.IndexByte(rest, '/'); index >= 0 {
		top = rest[:index]
	}
	switch top {
	case "app", "httpapi", "transport", "platform", "contracts", "config", "status", "apperror":
		return false
	}
	return true
}

func isComposition(dep string) bool {
	return dep == "internal/app" || dep == "internal/httpapi" || hasPrefix(dep, "internal/transport") || hasPrefix(dep, "cmd/")
}

func isProviderAdapter(dep string) bool {
	for _, adapter := range providerAdapters {
		if dep == adapter || hasPrefix(dep, adapter+"/") {
			return true
		}
	}
	return false
}

func isPortOwner(dep string) bool {
	for _, pkg := range portPackages {
		if dep == pkg {
			return true
		}
	}
	return false
}

func hasPrefix(value, prefix string) bool {
	return value == prefix || strings.HasPrefix(value, prefix+"/")
}

func trimPrefix(value, prefix string) (string, bool) {
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	return strings.TrimPrefix(value, prefix), true
}
