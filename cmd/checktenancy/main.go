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
	if len(violations) == 0 {
		return
	}
	for _, violation := range violations {
		fmt.Fprintln(os.Stderr, violation)
	}
	fmt.Fprintf(os.Stderr, "checktenancy: %d raw statement(s) can cross tenants\n", len(violations))
	os.Exit(1)
}
