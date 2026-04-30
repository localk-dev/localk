package compose

import (
	"errors"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// LoadFile reads a docker-compose.yml from path and returns its parsed
// shape. Returns a clearly-marked "missing file" error when the file
// doesn't exist, so callers (CLI subcommands, the TUI loader) can
// surface a friendly "run `localk generate` first" hint instead of a
// raw os.ErrNotExist string.
//
// Use this whenever you need to read an existing localk-generated
// compose file. For producing one, the converter writes via
// yaml.Marshal directly.
func LoadFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("compose file not found at %s: %w", path, err)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if f.Services == nil {
		f.Services = map[string]Service{}
	}
	return &f, nil
}
