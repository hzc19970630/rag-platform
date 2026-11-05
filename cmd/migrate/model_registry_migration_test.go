package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 0020 runs against existing deployments and must be safe to re-execute: every
// column addition is guarded by IF NOT EXISTS and the migration only widens
// knowledge_bases. The manual rollback is the inverse DROP COLUMN of the same
// four columns, so touching any other object would break that contract.
func TestModelRegistryMigrationIsRepeatable(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/postgres/0020_model_registry.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	var body strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if index := strings.Index(line, "--"); index >= 0 {
			line = line[:index]
		}
		body.WriteString(line)
		body.WriteString("\n")
	}

	statementPattern := regexp.MustCompile(`(?s)^ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS ([a-z_]+) VARCHAR\(\d+\) NOT NULL DEFAULT ''$`)
	expected := map[string]bool{
		"chat_model": false, "chat_model_version": false,
		"rerank_model": false, "rerank_model_version": false,
	}
	statements := 0
	for _, statement := range strings.Split(body.String(), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		statements++
		match := statementPattern.FindStringSubmatch(statement)
		if match == nil {
			t.Fatalf("migration statement is not a guarded column addition: %q", statement)
		}
		column := match[1]
		if _, ok := expected[column]; !ok {
			t.Fatalf("migration adds unexpected column %q", column)
		}
		if expected[column] {
			t.Fatalf("migration adds column %q twice", column)
		}
		expected[column] = true
	}
	if statements != 4 {
		t.Fatalf("migration statements = %d, want 4", statements)
	}
	for column, seen := range expected {
		if !seen {
			t.Fatalf("migration does not add %q", column)
		}
	}
	for _, destructive := range []string{"DROP ", "DELETE ", "UPDATE ", "INSERT ", "TRUNCATE "} {
		if strings.Contains(strings.ToUpper(body.String()), destructive) {
			t.Fatalf("0020 must stay additive; found %q", strings.TrimSpace(destructive))
		}
	}
}
