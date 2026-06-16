package engine

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

//go:embed source_ids.json
var embeddedSourceIDs []byte

// sourceIDsFile is the JSON shape for the source-id registry file.
type sourceIDsFile struct {
	SpecRevision string      `json:"spec_revision"`
	Ranges       [][2]uint16 `json:"ranges"`
}

// sourceRegistry implements SourceRegistry using a set of allowed [lo, hi] ranges.
type sourceRegistry struct {
	ranges [][2]uint16
}

// Allowed reports whether id is a permitted Source ID. 0 is always disallowed
// (reserved by spec, regardless of range configuration).
func (s *sourceRegistry) Allowed(id uint16) bool {
	if id == 0 {
		return false
	}
	for _, r := range s.ranges {
		if id >= r[0] && id <= r[1] {
			return true
		}
	}
	return false
}

// parseSourceIDs unmarshals raw JSON bytes into a *sourceRegistry.
func parseSourceIDs(data []byte) *sourceRegistry {
	var f sourceIDsFile
	if err := json.Unmarshal(data, &f); err != nil {
		panic(fmt.Sprintf("engine: malformed source_ids.json: %v", err))
	}
	return &sourceRegistry{ranges: f.Ranges}
}

// DefaultSourceRegistry returns the embedded, pinned Source ID registry.
// It panics only if the embedded JSON is malformed (a compile-time defect).
func DefaultSourceRegistry() SourceRegistry {
	return parseSourceIDs(embeddedSourceIDs)
}

// LoadSourceRegistry reads a source-registry JSON file from path and returns a
// SourceRegistry. Returns an error if the file cannot be read or parsed.
func LoadSourceRegistry(path string) (SourceRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("source registry: read %q: %w", path, err)
	}
	var f sourceIDsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("source registry: parse %q: %w", path, err)
	}
	return &sourceRegistry{ranges: f.Ranges}, nil
}
