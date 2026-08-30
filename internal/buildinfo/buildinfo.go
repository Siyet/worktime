// Package buildinfo exposes metadata for the WorkTime application binary.
//
// Release builds replace these variables with -ldflags -X. They deliberately do
// not describe the MCP protocol implementation, whose version is maintained by
// the MCP server package independently.
package buildinfo

// These defaults make local and unversioned builds explicit instead of
// accidentally presenting them as a published release.
var (
	Version   = "dev"
	Revision  = "unknown"
	BuiltAt   = "unknown"
	Packaging = "dev"
)

// Info is the immutable snapshot of build metadata exposed by a running binary.
type Info struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	BuiltAt   string `json:"built_at"`
	Packaging string `json:"-"`
}

// Current returns the metadata linked into this binary.
func Current() Info {
	return Info{
		Version:   Version,
		Revision:  Revision,
		BuiltAt:   BuiltAt,
		Packaging: Packaging,
	}
}
