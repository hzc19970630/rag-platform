// Package formats is the storage-neutral contract for accepted upload formats.
// The engine asks a Registry instead of importing a static table, so a future
// docreader- or tenant-provided format set can replace the default without
// touching ingestion or connector code.
package formats

import (
	"path"
	"strings"
)

// Spec is one accepted document format.
type Spec struct {
	// Name is the value exposed by /readyz and configuration.
	Name string
	// MIME is the canonical type stored on document versions.
	MIME string
	// Extensions are matched case-insensitively.
	Extensions []string
	// Accepts reports whether an upload's declared MIME is compatible. The
	// value arrives lowercased and without parameters; an empty value is
	// accepted because browsers often omit it.
	Accepts func(string) bool
}

// Registry resolves filenames and upload MIME types.
type Registry interface {
	Names() []string
	Lookup(filename string) (Spec, bool)
	Canonicalize(filename, mimeType string) (string, bool)
	MIMEForFilename(filename string) string
	ExtensionSupported(filename string) bool
}

// Static is the built-in format set shipped with the deployment.
type Static struct{}

var _ Registry = Static{}

func exact(mime string) func(string) bool {
	return func(value string) bool { return value == "" || value == mime }
}

func textPrefix(value string) bool {
	return value == "" || strings.HasPrefix(value, "text/")
}

var specs = []Spec{
	{Name: "pdf-text", MIME: "application/pdf", Extensions: []string{".pdf"}, Accepts: exact("application/pdf")},
	{Name: "docx", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Extensions: []string{".docx"}, Accepts: exact("application/vnd.openxmlformats-officedocument.wordprocessingml.document")},
	{Name: "xlsx", MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Extensions: []string{".xlsx"}, Accepts: exact("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")},
	{Name: "pptx", MIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Extensions: []string{".pptx"}, Accepts: exact("application/vnd.openxmlformats-officedocument.presentationml.presentation")},
	{Name: "html", MIME: "text/html", Extensions: []string{".html", ".htm"}, Accepts: func(value string) bool {
		return value == "" || value == "text/html" || value == "application/xhtml+xml" || strings.HasPrefix(value, "text/")
	}},
	{Name: "markdown", MIME: "text/markdown", Extensions: []string{".md"}, Accepts: func(value string) bool {
		return value == "" || value == "text/markdown" || value == "text/x-markdown" || value == "text/plain"
	}},
	{Name: "txt", MIME: "text/plain", Extensions: []string{".txt"}, Accepts: textPrefix},
}

// Default returns the built-in registry. Callers that need a deployment- or
// tenant-specific set inject another Registry instead.
func Default() Registry { return Static{} }

// Names returns the supported-format names in their published order.
func (Static) Names() []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

// Lookup finds the spec for a filename by extension.
func (Static) Lookup(filename string) (Spec, bool) {
	ext := strings.ToLower(path.Ext(filename))
	for _, spec := range specs {
		for _, candidate := range spec.Extensions {
			if ext == candidate {
				return spec, true
			}
		}
	}
	return Spec{}, false
}

// Canonicalize validates an upload and returns the MIME type to store. The
// declared type is lowercased and stripped of parameters first.
func (s Static) Canonicalize(filename, mimeType string) (string, bool) {
	spec, ok := s.Lookup(filename)
	if !ok {
		return "", false
	}
	value := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if !spec.Accepts(value) {
		return "", false
	}
	return spec.MIME, true
}

// MIMEForFilename returns the canonical MIME for a known extension, or
// text/plain for an unknown one. Recovered object keys have no declared type.
func (s Static) MIMEForFilename(filename string) string {
	if spec, ok := s.Lookup(filename); ok {
		return spec.MIME
	}
	return "text/plain"
}

// ExtensionSupported reports whether the filename has an accepted extension.
func (s Static) ExtensionSupported(filename string) bool {
	_, ok := s.Lookup(filename)
	return ok
}

// Names is the convenience form for the built-in registry, used by the
// configuration loader before the composition root exists.
func Names() []string { return Static{}.Names() }
