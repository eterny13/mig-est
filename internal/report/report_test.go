package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eterny13/mig-est/internal/model"
)

func sampleReport() model.Report {
	min, max := 1.0, 2.0
	disk := 100.0
	return model.Report{
		Tool: "mig-est", Version: "test", GeneratedAt: "2026-01-01T00:00:00Z", Mode: "offline",
		MySQL:       model.MySQLInfo{Version: "8.0.36", Database: "app", HostMasked: "db:3306"},
		OverallRisk: model.RiskHigh,
		Statements: []model.Statement{{
			Index: 1, File: "m.sql", Line: 1, SQLSummary: "ALTER TABLE orders ADD COLUMN note TEXT",
			Kind: model.KindAlterTable, Table: "orders", Operation: model.OpAddColumn,
			PredictedAlgorithm: model.AlgorithmInplace, Lock: "NONE", Risk: model.RiskMedium,
			RuleIDs: []string{"ADD-COL-01"}, RawSQL: "ALTER TABLE orders ADD COLUMN note TEXT /* secret raw SQL */",
			Estimate:       model.Estimate{MinSeconds: &min, MaxSeconds: &max, Confidence: "low", Basis: "test basis"},
			DiskRequiredMB: &disk, Suggestions: []string{"Test this on staging."},
		}},
		Blockers: []model.Finding{{ID: "MDL-01", Severity: model.SeverityCritical, Table: "orders", StatementIndex: 1, Message: "lock detected", Suggestion: "wait"}},
		Unknowns: []string{"disk_free: not provided"},
		Tables:   []model.TableSummary{{Name: "orders", Rows: 100, RowsApprox: true, SizeBytes: 1024}},
		FailOn:   "high", ExitCode: 1,
	}
}

func TestText(t *testing.T) {
	out := Text(sampleReport(), Options{})
	for _, want := range []string{"Overall risk: HIGH", "ADD-COL-01", "MDL-01", "Unknowns:", "Exit code: 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("text report missing %q:\n%s", want, out)
		}
	}
	quiet := Text(sampleReport(), Options{Quiet: true})
	if strings.Count(quiet, "\n") != 1 || !strings.Contains(quiet, "1 statement(s)") {
		t.Errorf("quiet output = %q", quiet)
	}
}

func TestMarkdown(t *testing.T) {
	out := Markdown(sampleReport())
	for _, want := range []string{"# mig-est report", "| Overall risk | **HIGH** |", "## Findings", "ALTER TABLE orders"} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q:\n%s", want, out)
		}
	}
}

func TestJSONOmitsRawSQL(t *testing.T) {
	out, err := JSON(sampleReport())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "secret raw SQL") {
		t.Errorf("JSON leaked raw SQL:\n%s", out)
	}
	if decoded["overall_risk"] != "high" {
		t.Errorf("overall_risk = %v", decoded["overall_risk"])
	}
}
