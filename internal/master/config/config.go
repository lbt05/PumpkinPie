// Package config loads the master's YAML configuration file.
//
// One file (`pp-master.yaml` by default, override with `--config`)
// holds every knob the master exposes — listen addresses, SQLite
// path, optional GreptimeDB metrics sink. The schema mirrors what
// the operator would previously have passed as CLI flags; see the
// sample at the bottom of this file or `pp-master.yaml` in the repo
// for the canonical form.
//
// Loading rules:
//   - If the file at `path` does not exist, Load returns a Config
//     populated with defaults (no GreptimeDB sink, sensible local
//     defaults). A missing file is never an error.
//   - If the file exists but is malformed, Load returns a parse error
//     so misconfigurations are caught at startup, not silently ignored.
//   - Fields left blank in the YAML keep their built-in defaults, so
//     you can override only what you care about.
package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/pumpkinpie/pumpkinpie/internal/master/metrics"
)

// DefaultPath is the canonical config location used when --config is
// not provided. Co-locating it with the systemd-installed binary's
// conventional etc dir keeps deployments predictable.
const DefaultPath = "/etc/pp/pp-master.yaml"

// Config is the master process's full configuration. YAML field
// names are the lowercase / kebab-cased versions of the runtime
// names; the YAML tags below are the source of truth.
type Config struct {
	HTTP string `yaml:"http"`
	GRPC string `yaml:"grpc"`
	DB   string `yaml:"db"`

	// Greptime holds the optional metrics sink. An empty Greptime.URL
	// disables the sink — the master keeps every metric in SQLite
	// only, exactly as before the metrics feature existed.
	Greptime metrics.Config `yaml:"greptime"`
}

// defaults returns a Config populated with the same defaults the
// previous flag-based version used, so behaviour stays identical
// for operators who never created a YAML.
func defaults() Config {
	return Config{
		HTTP: ":8080",
		GRPC: ":7000",
		DB:   "pumpkinpie.db",
		Greptime: metrics.Config{
			Database: "public",
			Table:    "node_metrics",
		},
	}
}

// Load reads and parses the YAML at `path`. If path is empty, the
// DefaultPath is used. A missing file is not an error — defaults are
// returned — but a malformed file is.
//
// The second return value reports whether the file existed, which
// the caller uses to decide between "loaded from <path>" and
// "no config file, using defaults" in its startup log line.
func Load(path string) (Config, bool, error) {
	if path == "" {
		path = DefaultPath
	}
	cfg := defaults()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, true, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	return cfg, true, nil
}

// applyDefaults fills any blank field with its built-in default so
// partial YAMLs work the same as the full file. Called after
// unmarshalling so explicit-but-empty values still pick up defaults
// (yaml.v3 treats an empty field as "" rather than "unset").
func (c *Config) applyDefaults() {
	d := defaults()
	if c.HTTP == "" {
		c.HTTP = d.HTTP
	}
	if c.GRPC == "" {
		c.GRPC = d.GRPC
	}
	if c.DB == "" {
		c.DB = d.DB
	}
	if c.Greptime.Database == "" {
		c.Greptime.Database = d.Greptime.Database
	}
	if c.Greptime.Table == "" {
		c.Greptime.Table = d.Greptime.Table
	}
}