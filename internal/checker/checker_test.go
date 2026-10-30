package checker

import (
	"strings"
	"testing"

	"github.com/eterny13/mig-est/internal/catalog"
	"github.com/eterny13/mig-est/internal/model"
)

func snapWithTable(t *catalog.TableInfo) *catalog.Snapshot {
	return &catalog.Snapshot{
		Database:         "app",
		Tables:           map[string]*catalog.TableInfo{catalog.Key(t.Schema, t.Name): t},
		LongTransactions: make(map[string][]catalog.LongTransaction),
		MDLWaits:         make(map[string][]catalog.MDLWait),
	}
}

func TestHeavyTableFindingOnlyBumpsTheHeavyStatement(t *testing.T) {
	table := &catalog.TableInfo{
		Schema: "app", Name: "orders", Exists: true, Source: "online",
		Rows: 20_000_000, DataLength: 10 * 1024 * mb, MetadataComplete: true,
		Columns: []catalog.Column{{Name: "id"}},
		Indexes: []catalog.Index{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
	}
	stmts := []*model.Statement{
		{Index: 1, Table: "orders", Kind: model.KindAlterTable, Operation: model.OpAddColumn, PredictedAlgorithm: model.AlgorithmInstant, Risk: model.RiskLow, Operations: []model.Operation{{Kind: model.OpAddColumn, Algorithm: model.AlgorithmInstant}}},
		{Index: 2, Table: "orders", Kind: model.KindAlterTable, Operation: model.OpConvertCharset, PredictedAlgorithm: model.AlgorithmCopy, Risk: model.RiskHigh, Operations: []model.Operation{{Kind: model.OpConvertCharset, Algorithm: model.AlgorithmCopy}}, DiskRequiredMB: floatPtr(15000)},
	}
	findings := Check(stmts, snapWithTable(table), model.Thresholds{LargeTableRows: 10_000_000}, nil)
	if !findingsContain(findings, "SIZE-01") {
		t.Fatalf("expected SIZE-01, got %+v", findings)
	}
	if stmts[0].Risk != model.RiskLow {
		t.Errorf("instant statement risk was raised to %s by a later COPY", stmts[0].Risk)
	}
	if stmts[1].Risk != model.RiskHigh {
		t.Errorf("COPY statement risk = %s, want HIGH", stmts[1].Risk)
	}
	for _, f := range findings {
		if f.ID == "SIZE-01" && f.StatementIndex != 2 {
			t.Errorf("SIZE finding should point at statement 2: %+v", f)
		}
	}
}

func TestMDLAndDiskFindings(t *testing.T) {
	table := &catalog.TableInfo{
		Schema: "app", Name: "orders", Exists: true, Source: "online",
		Rows: 100, DataLength: 100 * mb, MetadataComplete: true,
		Columns:  []catalog.Column{{Name: "id"}},
		Indexes:  []catalog.Index{{Name: "PRIMARY", Unique: true, Columns: []string{"id"}}},
		Triggers: []catalog.Trigger{{Name: "trg"}},
	}
	snap := snapWithTable(table)
	snap.LongTransactions[catalog.Key("app", "orders")] = []catalog.LongTransaction{{ConnectionID: 42, AgeSeconds: 600}}
	stmts := []*model.Statement{{
		Index: 1, Table: "orders", Kind: model.KindAlterTable, Operation: model.OpAddIndex,
		PredictedAlgorithm: model.AlgorithmInplace, Risk: model.RiskMedium,
		Operations:     []model.Operation{{Kind: model.OpAddIndex, Algorithm: model.AlgorithmInplace}},
		DiskRequiredMB: floatPtr(200),
	}}
	free := int64(10 * mb)
	findings := Check(stmts, snap, model.Thresholds{LargeTableRows: 10_000_000}, &free)
	if !findingsContain(findings, "MDL-01") || !findingsContain(findings, "TRG-01") || !findingsContain(findings, "DISK-01") {
		t.Fatalf("missing findings: %+v", findings)
	}
	if stmts[0].Risk != model.RiskCritical {
		t.Errorf("risk = %s, want CRITICAL after MDL/disk blockers", stmts[0].Risk)
	}
}

func TestCreateTableWithoutPrimaryKey(t *testing.T) {
	snap := &catalog.Snapshot{
		Database: "app",
		Tables: map[string]*catalog.TableInfo{
			catalog.Key("", "new_table"): {
				Name: "new_table", Exists: true, Source: "script", MetadataComplete: true,
				Columns: []catalog.Column{{Name: "id"}},
			},
		},
	}
	stmts := []*model.Statement{{
		Index: 1, Table: "new_table", Kind: model.KindCreateTable,
		Operation: model.OpCreateTable, PredictedAlgorithm: model.AlgorithmInstant, Risk: model.RiskLow,
	}}
	findings := Check(stmts, snap, model.Thresholds{}, nil)
	if !findingsContain(findings, "PK-02") {
		t.Fatalf("expected PK-02, got %+v", findings)
	}
	if stmts[0].Risk != model.RiskMedium {
		t.Errorf("risk = %s, want MEDIUM", stmts[0].Risk)
	}
}

func TestUnsupportedEngineAndMissingTable(t *testing.T) {
	myisam := &catalog.TableInfo{
		Schema: "app", Name: "legacy", Exists: true, Source: "online", Engine: "MyISAM",
		MetadataComplete: true, Columns: []catalog.Column{{Name: "id"}},
	}
	missing := &catalog.TableInfo{Schema: "app", Name: "ghost", Exists: false, Source: "online"}
	snap := &catalog.Snapshot{Database: "app", Tables: map[string]*catalog.TableInfo{
		catalog.Key("app", "legacy"): myisam,
		catalog.Key("app", "ghost"):  missing,
	}}
	stmts := []*model.Statement{
		{Index: 1, Table: "legacy", Kind: model.KindAlterTable, Operation: model.OpAddColumn, PredictedAlgorithm: model.AlgorithmInstant, Risk: model.RiskLow},
		{Index: 2, Table: "ghost", Kind: model.KindAlterTable, Operation: model.OpAddColumn, PredictedAlgorithm: model.AlgorithmInstant, Risk: model.RiskLow},
	}
	findings := Check(stmts, snap, model.Thresholds{}, nil)
	if !findingsContain(findings, "ENGINE-01") || !findingsContain(findings, "TARGET-01") {
		t.Fatalf("missing findings: %+v", findings)
	}
	if stmts[0].Risk != model.RiskHigh || stmts[1].Risk != model.RiskMedium {
		t.Errorf("risks = %s/%s, want HIGH/MEDIUM", stmts[0].Risk, stmts[1].Risk)
	}
}

func TestSortFindingsBySeverity(t *testing.T) {
	findings := []model.Finding{
		{ID: "B", Severity: model.SeverityLow},
		{ID: "A", Severity: model.SeverityCritical},
		{ID: "C", Severity: model.SeverityMedium},
	}
	SortFindings(findings)
	if findings[0].ID != "A" || findings[1].ID != "C" || findings[2].ID != "B" {
		t.Errorf("unexpected order: %+v", findings)
	}
}

func findingsContain(findings []model.Finding, id string) bool {
	for _, f := range findings {
		if strings.EqualFold(f.ID, id) {
			return true
		}
	}
	return false
}

func floatPtr(v float64) *float64 { return &v }
