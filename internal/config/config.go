// Package config loads localk.yaml — the optional per-service override file
// that lives in the user's repo root. It lets developers say things like
// "build api from this Dockerfile instead of pulling the prod image",
// "skip the worker locally", or "use postgres:15-alpine instead of the
// custom prod image". The file is optional; missing means "no overrides".
package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

// Config is the parsed shape of localk.yaml.
type Config struct {
	Services map[string]ServiceOverride `yaml:"services,omitempty"`
}

// ServiceOverride is the set of per-service tweaks the user can apply.
// All fields are optional — only the ones set take effect.
type ServiceOverride struct {
	// Skip drops the service from the generated compose file entirely.
	// Useful for services the developer runs natively (a local Postgres
	// install, a worker they don't need locally, etc.).
	Skip bool `yaml:"skip,omitempty"`

	// Image overrides the container image. Common case: swap a custom
	// prod image for an upstream one (e.g. postgres:16 → postgres:15-alpine).
	Image string `yaml:"image,omitempty"`

	// Build replaces the image with a local build context, so the developer
	// edits source and `docker compose up --build` rebuilds locally.
	// Accepts either a string shorthand ("./services/api") or an object
	// with explicit context + dockerfile.
	Build *BuildOverride `yaml:"build,omitempty"`

	// PreserveImage opts out of localk's automatic dev-image swap for
	// known clustered chart patterns (Bitnami mongo/rabbit/etc.). By
	// default localk replaces the production image with a vanilla
	// upstream one when the chart's StatefulSet+replica setup can't
	// run sensibly under compose. Set this to true when you actually
	// need the chart-specific image (custom plugins, TLS, etc.).
	PreserveImage bool `yaml:"preserve_image,omitempty"`
}

// BuildOverride mirrors compose's `build:` field. The custom UnmarshalYAML
// below accepts both `build: ./path` and `build: { context: ./path, ... }`.
type BuildOverride struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile,omitempty"`
}

// UnmarshalYAML lets the user write either:
//
//	build: ./services/api
//
// or:
//
//	build:
//	  context: ./services/api
//	  dockerfile: Dockerfile.dev
//
// When the value is a bare string we treat it as the context.
func (b *BuildOverride) UnmarshalYAML(data []byte) error {
	var s string
	if err := yaml.Unmarshal(data, &s); err == nil && s != "" {
		b.Context = s
		return nil
	}
	// Use a type alias so the recursive call doesn't loop back into
	// UnmarshalYAML.
	type buildAlias BuildOverride
	var v buildAlias
	if err := yaml.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("build: must be either a path string or {context, dockerfile}: %w", err)
	}
	if v.Context == "" {
		return errors.New("build: context is required (either as `build: ./path` or `build.context: ./path`)")
	}
	*b = BuildOverride(v)
	return nil
}

// Load reads localk.yaml from the given path. Returns (nil, nil) when the
// file doesn't exist — that's a valid state and just means "no overrides".
// Any other error (parse failure, permission denied, …) is surfaced.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return &cfg, nil
}

// ServiceOverrideFor returns the override for the given service name, or a
// zero-value override if none is configured. Callers can therefore use the
// result unconditionally without nil checks.
func (c *Config) ServiceOverrideFor(name string) ServiceOverride {
	if c == nil {
		return ServiceOverride{}
	}
	return c.Services[name]
}

// ServiceNames returns the set of service names that have overrides defined.
// Used by the converter to warn about entries that didn't match any
// Deployment (typos, stale config). Safe to call on a nil receiver.
func (c *Config) ServiceNames() map[string]struct{} {
	if c == nil {
		return nil
	}
	out := make(map[string]struct{}, len(c.Services))
	for name := range c.Services {
		out[name] = struct{}{}
	}
	return out
}
