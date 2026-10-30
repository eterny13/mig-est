// Package history stores and reads calibration records in JSON Lines format.
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Record is one real-world migration timing. SQL text is deliberately not
// stored.
type Record struct {
	At            string  `json:"at"`
	Table         string  `json:"table"`
	Operation     string  `json:"operation,omitempty"`
	Algorithm     string  `json:"algorithm,omitempty"`
	Rows          int64   `json:"rows,omitempty"`
	SizeMB        float64 `json:"size_mb,omitempty"`
	ActualSeconds float64 `json:"actual_seconds"`
	Host          string  `json:"host,omitempty"`
	Database      string  `json:"database,omitempty"`
	MySQLVersion  string  `json:"mysql_version,omitempty"`
}

// Append adds one record to path, creating parent directories as needed.
func Append(path string, rec Record) error {
	if rec.At == "" {
		rec.At = time.Now().Format(time.RFC3339)
	}
	if rec.ActualSeconds <= 0 {
		return fmt.Errorf("actual duration must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cannot create history directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("cannot open history file: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("cannot write history record: %w", err)
	}
	return nil
}

// Load reads JSON Lines records. Malformed lines are skipped so a damaged
// history file cannot break a check run.
func Load(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot open history file: %w", err)
	}
	defer f.Close()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("cannot read history file: %w", err)
	}
	return out, nil
}

// Calibrate returns the median observed MB/s for the latest matching records.
// A minimum of three records is required before a calibration is usable.
func Calibrate(records []Record, table, algorithm, host, database string, limit int) (medianMBps float64, n int, applied bool) {
	if limit <= 0 {
		limit = 10
	}
	var candidates []Record
	for _, r := range records {
		if !strings.EqualFold(r.Table, table) {
			continue
		}
		if algorithm != "" && !strings.EqualFold(r.Algorithm, algorithm) {
			continue
		}
		if host != "" && r.Host != "" && !strings.EqualFold(r.Host, host) {
			continue
		}
		if database != "" && r.Database != "" && !strings.EqualFold(r.Database, database) {
			continue
		}
		if r.SizeMB <= 0 || r.ActualSeconds <= 0 {
			continue
		}
		candidates = append(candidates, r)
	}
	if len(candidates) == 0 {
		return 0, 0, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].At > candidates[j].At
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	speeds := make([]float64, 0, len(candidates))
	for _, r := range candidates {
		speeds = append(speeds, r.SizeMB/r.ActualSeconds)
	}
	sort.Float64s(speeds)
	medianMBps = median(speeds)
	n = len(candidates)
	applied = n >= 3
	return medianMBps, n, applied
}

func median(sorted []float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
