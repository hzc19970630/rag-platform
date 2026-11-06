// Command checktenancy fails when a raw SQL statement touches a tenant-owned
// table without a tenant predicate and without an explicit allowlist reason.
// It is the raw-SQL counterpart to the tenantdb guard used by GORM chains.
package main

import (
	"fmt"
	"os"

	"github.com/example/rag-platform/internal/archtest"
)

func main() {
	files, err := archtest.LoadTenancyFiles(".", "internal", "cmd")
	if err != nil {
		fmt.Fprintln(os.Stderr, "checktenancy:", err)
		os.Exit(1)
	}
	violations, err := archtest.CheckTenancy(files)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checktenancy:", err)
		os.Exit(1)
	}
	gormViolations, err := archtest.CheckGormTenancy(files)
	if err != nil {
		fmt.Fprintln(os.Stderr, "checktenancy:", err)
		os.Exit(1)
	}
	for _, violation := range violations {
		fmt.Fprintln(os.Stderr, violation)
	}
	for _, violation := range gormViolations {
		fmt.Fprintln(os.Stderr, violation)
	}
	if len(violations) == 0 && len(gormViolations) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "checktenancy: %d raw statement(s) and %d GORM chain(s) can cross tenants\n", len(violations), len(gormViolations))
	os.Exit(1)
}
