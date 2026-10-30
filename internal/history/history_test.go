package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "history.jsonl")
	rec := Record{Table: "orders", Algorithm: "COPY", SizeMB: 8200, ActualSeconds: 492}
	if err := Append(path, rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := Append(path, Record{Table: "users", Algorithm: "INPLACE", SizeMB: 10, ActualSeconds: 2}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0].Table != "orders" || got[0].At == "" {
		t.Fatalf("unexpected records: %+v", got)
	}
}

func TestLoadMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	got, err := Load(filepath.Join(dir, "missing.jsonl"))
	if err != nil || got != nil {
		t.Fatalf("missing file: %v %+v", err, got)
	}
	path := filepath.Join(dir, "history.jsonl")
	if err := os.WriteFile(path, []byte("{bad json}\n"+`{"table":"orders","size_mb":100,"actual_seconds":10}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Table != "orders" {
		t.Fatalf("malformed line should be skipped: %+v", got)
	}
}

func TestCalibrateMedianAndFiltering(t *testing.T) {
	now := time.Now().Format(time.RFC3339)
	records := []Record{
		{At: now, Table: "orders", Algorithm: "COPY", SizeMB: 100, ActualSeconds: 10, Host: "db", Database: "app"},
		{At: now, Table: "orders", Algorithm: "COPY", SizeMB: 100, ActualSeconds: 20, Host: "db", Database: "app"},
		{At: now, Table: "orders", Algorithm: "COPY", SizeMB: 100, ActualSeconds: 40, Host: "db", Database: "app"},
		{At: now, Table: "orders", Algorithm: "INPLACE", SizeMB: 100, ActualSeconds: 1, Host: "db", Database: "app"},
		{At: now, Table: "orders", Algorithm: "COPY", SizeMB: 100, ActualSeconds: 1, Host: "other", Database: "app"},
		{At: now, Table: "orders", Algorithm: "COPY", SizeMB: 0, ActualSeconds: 1, Host: "db", Database: "app"},
	}
	median, n, applied := Calibrate(records, "ORDERS", "copy", "db", "app", 10)
	if !applied || n != 3 || median != 5 {
		t.Fatalf("calibrate = %v/%d/%v, want 5/3/true", median, n, applied)
	}
	_, n, applied = Calibrate(records, "orders", "COPY", "db", "app", 2)
	if applied || n != 2 {
		t.Fatalf("limit two = applied %v n %d, want false/2", applied, n)
	}
}

func TestCalibrateUsesLatestRecords(t *testing.T) {
	records := []Record{
		{At: "2026-01-01T00:00:00Z", Table: "orders", Algorithm: "COPY", SizeMB: 100, ActualSeconds: 1},
		{At: "2026-01-02T00:00:00Z", Table: "orders", Algorithm: "COPY", SizeMB: 40, ActualSeconds: 10},
		{At: "2026-01-03T00:00:00Z", Table: "orders", Algorithm: "COPY", SizeMB: 50, ActualSeconds: 10},
		{At: "2026-01-04T00:00:00Z", Table: "orders", Algorithm: "COPY", SizeMB: 60, ActualSeconds: 10},
	}
	median, n, applied := Calibrate(records, "orders", "COPY", "", "", 3)
	if !applied || n != 3 || median != 5 {
		t.Fatalf("latest three = %v/%d/%v, want 5/3/true", median, n, applied)
	}
}
