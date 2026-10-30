package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.FailOn != "critical" || cfg.Output.Format != "text" || !cfg.HistoryEnabled() {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.Thresholds.LargeTableRows != 10_000_000 || cfg.Thresholds.DiskSafetyFactor != 1.5 {
		t.Errorf("unexpected thresholds: %+v", cfg.Thresholds)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
}

func TestLoadPartialConfigUsesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".mig-est.yaml")
	if err := os.WriteFile(path, []byte("fail_on: high\noutput:\n  format: markdown\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FailOn != "high" || cfg.Output.Format != "markdown" {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if cfg.Thresholds.LargeTableRows != 10_000_000 || cfg.History.Path == "" {
		t.Errorf("defaults lost: %+v", cfg)
	}
}

func TestLoadMissing(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), false)
	if err != nil || cfg.FailOn != "critical" {
		t.Fatalf("optional missing file: %+v %v", cfg, err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), true); err == nil {
		t.Fatal("required missing file should fail")
	}
}

func TestValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		mutate func(*Config)
		want   string
	}{
		{func(c *Config) { c.FailOn = "sometimes" }, "fail_on"},
		{func(c *Config) { c.Output.Format = "xml" }, "output.format"},
		{func(c *Config) { c.Thresholds.LargeTableRows = -1 }, "thresholds"},
		{func(c *Config) { c.Thresholds.LongTransactionMin = 0 }, "long_transaction_minutes"},
		{func(c *Config) { c.Thresholds.ReplicaLagSeconds = 0 }, "replica_lag_seconds"},
		{func(c *Config) { c.Thresholds.DiskSafetyFactor = 0.9 }, "disk_safety_factor"},
	}
	for _, tt := range tests {
		cfg := Default()
		tt.mutate(&cfg)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Validate() = %v, want error containing %q", err, tt.want)
		}
	}
}

func TestHistoryCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mig-est.yaml")
	if err := os.WriteFile(path, []byte("history:\n  enabled: false\n  path: custom.jsonl\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HistoryEnabled() {
		t.Error("history should be disabled")
	}
	if cfg.History.Path != "custom.jsonl" {
		t.Errorf("history path = %q", cfg.History.Path)
	}
}
