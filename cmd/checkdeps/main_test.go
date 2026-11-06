package main

import (
	"testing"

	"github.com/example/rag-platform/internal/archtest"
)

func TestDependencyRuleRejectsAdapterImportingDomainImplementation(t *testing.T) {
	const module = "example.com/mod"
	violations := archtest.Check(module, []archtest.Package{{
		ImportPath: module + "/internal/platform/vector",
		Imports:    []string{module + "/internal/retrieval"},
	}})
	if len(violations) != 1 || violations[0].Rule != archtest.RuleAdapterDomain {
		t.Fatalf("adapter importing a domain implementation = %#v, want RuleAdapterDomain", violations)
	}
}

func TestDependencyRuleAllowsPortPackage(t *testing.T) {
	const module = "example.com/mod"
	violations := archtest.Check(module, []archtest.Package{{
		ImportPath: module + "/internal/platform/vector",
		Imports:    []string{module + "/internal/contracts/ports"},
	}})
	if len(violations) != 0 {
		t.Fatalf("port package import = %#v, want no violation", violations)
	}
}
