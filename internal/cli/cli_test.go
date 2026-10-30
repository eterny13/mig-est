package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eterny13/mig-est/internal/history"
	"github.com/eterny13/mig-est/internal/model"
)

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTestConfig(t *testing.T, dir, historyPath string) string {
	t.Helper()
	content := "mysql:\n  version: \"8.0.36\"\n"
	if historyPath == "" {
		content += "history:\n  enabled: false\n"
	} else {
		content += "history:\n  enabled: true\n  path: " + historyPath + "\n"
	}
	return writeTestFile(t, dir, "config.yaml", content)
}

func TestCheckOfflineJSONAndExitCodes(t *testing.T) {
	dir := t.TempDir()
	cfg := writeTestConfig(t, dir, "")
	sqlFile := writeTestFile(t, dir, "migration.sql", "ALTER TABLE orders CONVERT TO CHARACTER SET utf8mb4;\n")
	var out, errOut bytes.Buffer
	code := Run([]string{
		"check", "--file", sqlFile, "--offline", "--config", cfg,
		"--table-size", "orders=8200MB", "--table-rows", "orders=12400000",
		"--format", "json", "--fail-on", "high", "--no-color",
	}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%s", code, errOut.String())
	}
	var report model.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if report.OverallRisk != model.RiskHigh || report.ExitCode != 1 || report.Mode != "offline" {
		t.Errorf("report = %+v", report)
	}
	if len(report.Statements) != 1 || report.Statements[0].PredictedAlgorithm != model.AlgorithmCopy {
		t.Errorf("unexpected statements: %+v", report.Statements)
	}
	if len(report.Tables) != 1 || report.Tables[0].Rows != 12400000 {
		t.Errorf("unexpected tables: %+v", report.Tables)
	}
	if len(report.Unknowns) == 0 {
		t.Error("offline report should include unknowns")
	}
}

func TestCheckOfflineDefaultCriticalPasses(t *testing.T) {
	dir := t.TempDir()
	cfg := writeTestConfig(t, dir, "")
	sqlFile := writeTestFile(t, dir, "migration.sql", "ALTER TABLE orders ADD COLUMN note VARCHAR(20);\n")
	var out, errOut bytes.Buffer
	code := Run([]string{
		"check", "--file", sqlFile, "--offline", "--config", cfg,
		"--table-size", "orders=10MB", "--table-rows", "orders=1000",
		"--format", "json",
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", code, errOut.String())
	}
	var report model.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.OverallRisk == model.RiskCritical {
		t.Errorf("unexpected CRITICAL risk: %+v", report)
	}
}

func TestCheckMarkdownAndOutputFile(t *testing.T) {
	dir := t.TempDir()
	cfg := writeTestConfig(t, dir, "")
	sqlFile := writeTestFile(t, dir, "migration.sql", "CREATE INDEX idx_status ON orders (status);\n")
	output := filepath.Join(dir, "report.md")
	var out, errOut bytes.Buffer
	code := Run([]string{
		"check", "--file", sqlFile, "--offline", "--config", cfg,
		"--table-size", "orders=100MB", "--format", "markdown", "--output", output,
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout should be empty when --output is used: %q", out.String())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# mig-est report") || !strings.Contains(string(data), "CREATE INDEX") {
		t.Errorf("unexpected markdown:\n%s", data)
	}
}

func TestCheckRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	cfg := writeTestConfig(t, dir, "")
	bad := writeTestFile(t, dir, "bad.sql", "ALTER TABLE orders ADD COLUMN;\n")
	var out, errOut bytes.Buffer
	code := Run([]string{"check", "--file", bad, "--offline", "--config", cfg}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "cannot parse SQL") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestRecordWritesCalibration(t *testing.T) {
	dir := t.TempDir()
	historyPath := filepath.Join(dir, "history", "history.jsonl")
	cfg := writeTestConfig(t, dir, historyPath)
	sqlFile := writeTestFile(t, dir, "migration.sql", "ALTER TABLE orders CONVERT TO CHARACTER SET utf8mb4;\n")
	var out, errOut bytes.Buffer
	code := Run([]string{
		"record", "--file", sqlFile, "--actual", "8m12s", "--table", "orders",
		"--size", "8200MB", "--host", "db.example.com", "--database", "app",
		"--config", cfg,
	}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, errOut.String())
	}
	records, err := history.Load(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	rec := records[0]
	if rec.Table != "orders" || rec.Algorithm != "COPY" || rec.Operation != "CONVERT_CHARSET" {
		t.Errorf("record = %+v", rec)
	}
	if rec.ActualSeconds != 492 || rec.SizeMB != 8200 {
		t.Errorf("durations = %v/%v", rec.ActualSeconds, rec.SizeMB)
	}
	if rec.Host != "db.example.com" || rec.Database != "app" {
		t.Errorf("host/db = %q/%q", rec.Host, rec.Database)
	}
	if _, err := time.Parse(time.RFC3339, rec.At); err != nil {
		t.Errorf("timestamp = %q: %v", rec.At, err)
	}
}

func TestVersionAndHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Run([]string{"version"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "mig-est") {
		t.Errorf("version exit=%d out=%q err=%q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := Run([]string{"help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "never executes migrations") {
		t.Errorf("help exit=%d out=%q", code, out.String())
	}
	out.Reset()
	if code := Run(nil, &out, &errOut); code != 2 {
		t.Errorf("no args exit=%d, want 2", code)
	}
}
