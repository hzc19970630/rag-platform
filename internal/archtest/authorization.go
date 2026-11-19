package archtest

import (
	"fmt"
	"strings"
)

// AuthorizationBypass is one production statement that reaches around the
// resource-authorization seam.
type AuthorizationBypass struct {
	File   string
	Line   int
	Reason string
}

func (v AuthorizationBypass) String() string {
	return fmt.Sprintf("%s:%d: %s", v.File, v.Line, v.Reason)
}

// CheckAuthorization enforces the B6-b seam: handler-facing services resolve
// resource checks through authz.AuthorizeResource, which owns the
// target→policy-path mapping. Building the path or calling the generic
// Authorize from a domain package re-scatters the decision and is rejected.
// Collection checks (AuthorizeKnowledgeBases), BuildScope, CanRead and
// FilterReadable are separate, allowed surfaces.
func CheckAuthorization(files []TenancyFile) []AuthorizationBypass {
	var violations []AuthorizationBypass
	for _, file := range files {
		if strings.HasSuffix(file.Path, "_test.go") {
			continue
		}
		if strings.HasPrefix(file.Path, "internal/authz/") {
			continue
		}
		// This checker names the banned patterns in its own literals.
		if file.Path == "internal/archtest/authorization.go" {
			continue
		}
		for index, line := range strings.Split(string(file.Source), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			switch {
			case strings.Contains(line, "AuthorizeKnowledgeBase("):
				violations = append(violations, AuthorizationBypass{
					File: file.Path, Line: index + 1,
					Reason: "use authz.AuthorizeResource with an authz.ResourceTarget",
				})
			case strings.Contains(line, ".Authorize("):
				violations = append(violations, AuthorizationBypass{
					File: file.Path, Line: index + 1,
					Reason: "resource checks must use authz.AuthorizeResource; only the authz package may call Authorize",
				})
			case strings.Contains(line, "resourcePath("):
				violations = append(violations, AuthorizationBypass{
					File: file.Path, Line: index + 1,
					Reason: "policy paths are built inside authz.AuthorizeResource",
				})
			}
		}
	}
	return violations
}
