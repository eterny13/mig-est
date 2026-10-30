// Package config loads .mig-est.yaml and applies CLI overrides.
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/eterny13/mig-est/internal/model"
)

// Config is the complete configuration file schema. Every field is optional;
// defaults are applied before unmarshalling.
type Config struct {
	MySQL      MySQLConfig      `yaml:"mysql"`
	Thresholds model.Thresholds `yaml:"thresholds"`
	FailOn     string           `yaml:"fail_on"`
	Output     OutputConfig     `yaml:"output"`
	History    HistoryConfig    `yaml:"history"`
}

// MySQLConfig selects the version used in offline mode.
type MySQLConfig struct {
	Version string `yaml:"version"`
}

// OutputConfig controls report formatting.
type OutputConfig struct {
	Format string `yaml:"format"`
}

// HistoryConfig controls calibration storage.
type HistoryConfig struct {
	Path    string `yaml:"path"`
	Enabled *bool  `yaml:"enabled"`
}

// Default returns the documented defaults.
func Default() Config {
	enabled := true
	return Config{
		MySQL: MySQLConfig{},
		Thresholds: model.Thresholds{
			LargeTableRows:     10_000_000,
			LargeTableSizeMB:   5_000,
			LongTransactionMin: 5,
			ReplicaLagSeconds:  10,
			DiskSafetyFactor:   1.5,
		},
		FailOn: "critical",
		Output: OutputConfig{Format: "text"},
		History: HistoryConfig{
			Path:    "./.mig-est/history.jsonl",
			Enabled: &enabled,
		},
	}
}

// Load reads path. If the file does not exist, defaults are returned when
// required is false; otherwise an error is returned.
func Load(path string, required bool) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return cfg, nil
		}
		return cfg, fmt.Errorf("cannot read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("cannot parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks enumerations and numeric limits.
func (c *Config) Validate() error {
	switch strings.ToLower(c.FailOn) {
	case "low", "medium", "high", "critical", "never":
		c.FailOn = strings.ToLower(c.FailOn)
	default:
		return fmt.Errorf("fail_on must be low, medium, high, critical, or never (got %q)", c.FailOn)
	}
	c.Output.Format = strings.ToLower(strings.TrimSpace(c.Output.Format))
	switch c.Output.Format {
	case "text", "markdown", "json":
	default:
		return fmt.Errorf("output.format must be text, markdown, or json (got %q)", c.Output.Format)
	}
	if c.Thresholds.LargeTableRows < 0 || c.Thresholds.LargeTableSizeMB < 0 {
		return fmt.Errorf("large table thresholds cannot be negative")
	}
	if c.Thresholds.LongTransactionMin <= 0 {
		return fmt.Errorf("thresholds.long_transaction_minutes must be positive")
	}
	if c.Thresholds.ReplicaLagSeconds <= 0 {
		return fmt.Errorf("thresholds.replica_lag_seconds must be positive")
	}
	if c.Thresholds.DiskSafetyFactor < 1 {
		return fmt.Errorf("thresholds.disk_safety_factor must be >= 1")
	}
	if c.History.Path == "" {
		c.History.Path = "./.mig-est/history.jsonl"
	}
	return nil
}

// HistoryEnabled reports whether calibration storage is enabled.
func (c Config) HistoryEnabled() bool {
	return c.History.Enabled == nil || *c.History.Enabled
}
