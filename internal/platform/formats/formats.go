// Package formats is the single source of truth for accepted upload formats:
// extension, canonical MIME, supported-format name and the upload MIME policy.
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
	// accepts reports whether an upload's declared MIME is compatible. The
	// value arrives lowercased and without parameters; an empty value is
	// accepted because browsers often omit it.
	accepts func(string) bool
}

func exact(mime string) func(string) bool {
	return func(value string) bool { return value == "" || value == mime }
}

func textPrefix(value string) bool {
	return value == "" || strings.HasPrefix(value, "text/")
}

var specs = []Spec{
	{Name: "pdf-text", MIME: "application/pdf", Extensions: []string{".pdf"}, accepts: exact("application/pdf")},
	{Name: "docx", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Extensions: []string{".docx"}, accepts: exact("application/vnd.openxmlformats-officedocument.wordprocessingml.document")},
	{Name: "xlsx", MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Extensions: []string{".xlsx"}, accepts: exact("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")},
	{Name: "pptx", MIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Extensions: []string{".pptx"}, accepts: exact("application/vnd.openxmlformats-officedocument.presentationml.presentation")},
	{Name: "html", MIME: "text/html", Extensions: []string{".html", ".htm"}, accepts: func(value string) bool {
		return value == "" || value == "text/html" || value == "application/xhtml+xml" || strings.HasPrefix(value, "text/")
	}},
	{Name: "markdown", MIME: "text/markdown", Extensions: []string{".md"}, accepts: func(value string) bool {
		return value == "" || value == "text/markdown" || value == "text/x-markdown" || value == "text/plain"
	}},
	{Name: "txt", MIME: "text/plain", Extensions: []string{".txt"}, accepts: textPrefix},
}

// Names returns the supported-format names in their published order.
func Names() []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

// Lookup finds the spec for a filename by extension.
func Lookup(filename string) (Spec, bool) {
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
func Canonicalize(filename, mimeType string) (string, bool) {
	spec, ok := Lookup(filename)
	if !ok {
		return "", false
	}
	value := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if !spec.accepts(value) {
		return "", false
	}
	return spec.MIME, true
}

// MIMEForFilename returns the canonical MIME for a known extension, or
// text/plain for an unknown one. Recovered object keys have no declared type,
// so callers rely on the extension alone.
func MIMEForFilename(filename string) string {
	if spec, ok := Lookup(filename); ok {
		return spec.MIME
	}
	return "text/plain"
}

// ExtensionSupported reports whether the filename has an accepted extension.
func ExtensionSupported(filename string) bool {
	_, ok := Lookup(filename)
	return ok
}
