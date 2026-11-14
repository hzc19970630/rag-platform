package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/presets"
)

const usage = `usage: preset <command> [flags]

commands:
  list                         list built-in presets
  validate <file|name[@ver]>   validate one manifest
  export <name[@ver]>          print one manifest as JSON
  diff <a> <b>                 compare two manifests
  import <file> [--dir DIR]    validate and copy a manifest into DIR

A manifest is data only: retrieval/chunking keys must exist in configschema, and
permissions/compliance sections are declarations until B6/B8 enforce them.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	catalog, err := presets.LoadBuiltin()
	if err != nil {
		fatal(err)
	}
	switch os.Args[1] {
	case "list":
		for _, manifest := range catalog.List() {
			fmt.Printf("%s@%s\t%s\t%s\n", manifest.Name, manifest.Version, manifest.Vertical, manifest.DisplayName)
		}
	case "validate":
		manifest, err := resolve(catalog, os.Args[2:])
		if err != nil {
			fatal(err)
		}
		fmt.Printf("valid %s@%s\n", manifest.Name, manifest.Version)
	case "export":
		manifest, err := resolve(catalog, os.Args[2:])
		if err != nil {
			fatal(err)
		}
		raw, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			fatal(err)
		}
		fmt.Println(string(raw))
	case "diff":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		left, err := resolve(catalog, os.Args[2:3])
		if err != nil {
			fatal(err)
		}
		right, err := resolve(catalog, os.Args[3:4])
		if err != nil {
			fatal(err)
		}
		changes := presets.Diff(left, right)
		if len(changes) == 0 {
			fmt.Println("no differences")
			return
		}
		for _, change := range changes {
			fmt.Printf("%s.%s: %s -> %s\n", change.Section, change.Key, change.From, change.To)
		}
	case "import":
		flags := flag.NewFlagSet("import", flag.ContinueOnError)
		dir := flags.String("dir", "config/presets", "destination directory")
		if err := flags.Parse(os.Args[2:]); err != nil {
			os.Exit(2)
		}
		if flags.NArg() != 1 {
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(2)
		}
		manifest, err := presets.LoadFile(flags.Arg(0))
		if err != nil {
			fatal(err)
		}
		if err := os.MkdirAll(*dir, 0o755); err != nil {
			fatal(err)
		}
		target := filepath.Join(*dir, fmt.Sprintf("%s-%s.json", manifest.Name, manifest.Version))
		raw, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			fatal(err)
		}
		if err := os.WriteFile(target, append(raw, '\n'), 0o644); err != nil {
			fatal(err)
		}
		fmt.Printf("imported %s@%s -> %s\n", manifest.Name, manifest.Version, target)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

func resolve(catalog *presets.Catalog, args []string) (presets.Manifest, error) {
	if len(args) != 1 {
		return presets.Manifest{}, fmt.Errorf("expected one preset name or file")
	}
	value := args[0]
	if strings.HasSuffix(value, ".json") {
		return presets.LoadFile(value)
	}
	name, version := value, ""
	if before, after, ok := strings.Cut(value, "@"); ok {
		name, version = before, after
	}
	return catalog.Get(name, version)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "preset: %v\n", err)
	os.Exit(1)
}
