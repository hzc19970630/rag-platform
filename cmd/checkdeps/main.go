// Command checkdeps enforces the package dependency direction. It fails the
// build when a domain package imports configuration, the composition root or a
// provider adapter, when transport imports the composition root, or when a
// platform adapter imports a domain package other than the port it implements.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/hzc19970630/rag-platform/internal/archtest"
)

func main() {
	module, err := goListModule()
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkdeps:", err)
		os.Exit(1)
	}
	packages, err := goListPackages()
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkdeps:", err)
		os.Exit(1)
	}
	violations := archtest.Check(module, packages)
	if len(violations) == 0 {
		return
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Package == violations[j].Package {
			return violations[i].Import < violations[j].Import
		}
		return violations[i].Package < violations[j].Package
	})
	for _, violation := range violations {
		fmt.Fprintf(os.Stderr, "%s imports %s: %s\n", violation.Package, violation.Import, violation.Rule)
	}
	os.Exit(1)
}

func goListModule() (string, error) {
	output, err := exec.Command("go", "list", "-m", "-f", "{{.Path}}").Output()
	if err != nil {
		return "", fmt.Errorf("go list -m: %w", err)
	}
	return string(trimNewline(output)), nil
}

func goListPackages() ([]archtest.Package, error) {
	output, err := exec.Command("go", "list", "-json", "./...").Output()
	if err != nil {
		return nil, fmt.Errorf("go list ./...: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	var packages []archtest.Package
	for decoder.More() {
		var pkg archtest.Package
		if err := decoder.Decode(&pkg); err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		packages = append(packages, pkg)
	}
	return packages, nil
}

func trimNewline(value []byte) []byte {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}
