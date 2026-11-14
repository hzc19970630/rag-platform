// Package presets owns the versioned, data-only enterprise preset packs. A pack
// carries defaults and declarations; it never adds engine branches or applies
// permissions/compliance that the deployment cannot yet enforce.
package presets

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hzc19970630/rag-platform/internal/apperror"
	"github.com/hzc19970630/rag-platform/internal/configschema"
	"github.com/hzc19970630/rag-platform/internal/contracts/formats"
)

const SchemaVersion = "EnterprisePresetV1"

var (
	namePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
)

// Manifest is one preset pack. Retrieval and chunking keys are the same keys
// configschema exposes, so a pack cannot invent a second configuration plane.
type Manifest struct {
	SchemaVersion string                     `json:"schemaVersion"`
	Name          string                     `json:"name"`
	Version       string                     `json:"version"`
	DisplayName   string                     `json:"displayName"`
	Description   string                     `json:"description"`
	Vertical      string                     `json:"vertical"`
	Retrieval     map[string]json.RawMessage `json:"retrieval"`
	Chunking      map[string]json.RawMessage `json:"chunking"`
	Formats       []string                   `json:"formats"`
	Terminology   json.RawMessage            `json:"terminology,omitempty"`
	Permissions   json.RawMessage            `json:"permissions,omitempty"`
	Masking       json.RawMessage            `json:"masking,omitempty"`
	Compliance    json.RawMessage            `json:"compliance,omitempty"`
	Evaluation    json.RawMessage            `json:"evaluation,omitempty"`
	Connectors    []string                   `json:"connectors,omitempty"`
	Questions     []string                   `json:"questions,omitempty"`
}

// Validate checks metadata, the configuration keys and the declared sections.
func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return apperror.Invalidf("unsupported preset schema %q", m.SchemaVersion)
	}
	if !namePattern.MatchString(m.Name) {
		return apperror.Invalidf("preset name must match %s", namePattern)
	}
	if !versionPattern.MatchString(m.Version) {
		return apperror.Invalidf("preset version must be semver, got %q", m.Version)
	}
	switch m.Vertical {
	case "general", "finance", "healthcare":
	default:
		return apperror.Invalidf("unsupported vertical %q", m.Vertical)
	}
	if strings.TrimSpace(m.DisplayName) == "" {
		return apperror.Invalidf("preset displayName is required")
	}
	if len(m.Retrieval) == 0 || len(m.Chunking) == 0 {
		return apperror.Invalidf("preset retrieval and chunking defaults are required")
	}
	if err := configschema.Validate(configschema.DomainRetrieval, m.Retrieval); err != nil {
		return fmt.Errorf("preset retrieval: %w", err)
	}
	if err := configschema.Validate(configschema.DomainChunking, m.Chunking); err != nil {
		return fmt.Errorf("preset chunking: %w", err)
	}
	known := map[string]bool{}
	for _, name := range formats.Names() {
		known[name] = true
	}
	if len(m.Formats) == 0 {
		return apperror.Invalidf("preset formats must not be empty")
	}
	for _, name := range m.Formats {
		if !known[name] {
			return apperror.Invalidf("unsupported format %q", name)
		}
	}
	for label, raw := range map[string]json.RawMessage{
		"terminology": m.Terminology, "permissions": m.Permissions,
		"masking": m.Masking, "compliance": m.Compliance, "evaluation": m.Evaluation,
	} {
		if len(raw) > 0 && !json.Valid(raw) {
			return apperror.Invalidf("preset %s is not valid JSON", label)
		}
	}
	return nil
}

// Change is one field difference between two packs.
type Change struct {
	Section string `json:"section"`
	Key     string `json:"key"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// Diff compares configuration sections and metadata.
func Diff(a, b Manifest) []Change {
	changes := make([]Change, 0)
	compare := func(section string, left, right map[string]json.RawMessage) {
		keys := map[string]bool{}
		for key := range left {
			keys[key] = true
		}
		for key := range right {
			keys[key] = true
		}
		sorted := make([]string, 0, len(keys))
		for key := range keys {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		for _, key := range sorted {
			from := compact(left[key])
			to := compact(right[key])
			if from != to {
				changes = append(changes, Change{Section: section, Key: key, From: from, To: to})
			}
		}
	}
	compare("retrieval", a.Retrieval, b.Retrieval)
	compare("chunking", a.Chunking, b.Chunking)
	if a.Version != b.Version {
		changes = append(changes, Change{Section: "metadata", Key: "version", From: a.Version, To: b.Version})
	}
	if strings.Join(a.Formats, ",") != strings.Join(b.Formats, ",") {
		changes = append(changes, Change{Section: "metadata", Key: "formats", From: strings.Join(a.Formats, ","), To: strings.Join(b.Formats, ",")})
	}
	return changes
}

func compact(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

//go:embed builtin/*.json
var builtinFS embed.FS

// Catalog is the deployment preset registry.
type Catalog struct {
	items    map[string]Manifest
	versions map[string][]string
}

// LoadBuiltin loads the embedded packs. A malformed built-in is a startup error.
func LoadBuiltin() (*Catalog, error) {
	catalog := &Catalog{items: map[string]Manifest{}, versions: map[string][]string{}}
	files, err := fs.Glob(builtinFS, "builtin/*.json")
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		raw, err := builtinFS.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var manifest Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return nil, fmt.Errorf("load preset %s: %w", file, err)
		}
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("load preset %s: %w", path.Base(file), err)
		}
		catalog.add(manifest)
	}
	return catalog, nil
}

func (c *Catalog) add(manifest Manifest) {
	key := manifest.Name + "@" + manifest.Version
	c.items[key] = manifest
	c.versions[manifest.Name] = append(c.versions[manifest.Name], manifest.Version)
	sort.Slice(c.versions[manifest.Name], func(i, j int) bool {
		return compareVersion(c.versions[manifest.Name][i], c.versions[manifest.Name][j]) > 0
	})
}

// Get resolves a pack by name and optional version (latest when empty).
func (c *Catalog) Get(name, version string) (Manifest, error) {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if name == "" {
		return Manifest{}, apperror.Invalidf("preset name is required")
	}
	if version == "" {
		versions := c.versions[name]
		if len(versions) == 0 {
			return Manifest{}, apperror.Invalidf("unknown preset %q", name)
		}
		version = versions[0]
	}
	manifest, ok := c.items[name+"@"+version]
	if !ok {
		return Manifest{}, apperror.Invalidf("unknown preset %q version %q", name, version)
	}
	return manifest, nil
}

// List returns every pack, newest version first per name.
func (c *Catalog) List() []Manifest {
	names := make([]string, 0, len(c.versions))
	for name := range c.versions {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]Manifest, 0, len(c.items))
	for _, name := range names {
		for _, version := range c.versions[name] {
			result = append(result, c.items[name+"@"+version])
		}
	}
	return result
}

func compareVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		na, _ := strconv.Atoi(pa[i])
		nb, _ := strconv.Atoi(pb[i])
		if na != nb {
			if na > nb {
				return 1
			}
			return -1
		}
	}
	return 0
}

// LoadFile reads and validates a manifest from disk.
func LoadFile(filename string) (Manifest, error) {
	raw, err := os.ReadFile(filename)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, manifest.Validate()
}
