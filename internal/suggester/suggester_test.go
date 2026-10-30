package suggester

import (
	"strings"
	"testing"

	"github.com/eterny13/mig-est/internal/catalog"
	"github.com/eterny13/mig-est/internal/model"
)

func TestExplicitAlgorithmAndSplitSuggestions(t *testing.T) {
	stmt := &model.Statement{
		Table: "orders", Kind: model.KindAlterTable, PredictedAlgorithm: model.AlgorithmInplace,
		Operations: []model.Operation{
			{Kind: model.OpAddColumn, ColumnName: "note"},
			{Kind: model.OpAddIndex, IndexColumns: []string{"note"}},
		},
	}
	out := strings.Join(Suggest(stmt, nil), "\n")
	if !strings.Contains(out, "ALGORITHM=INPLACE") {
		t.Errorf("missing explicit algorithm suggestion:\n%s", out)
	}
	if !strings.Contains(out, "Split this ALTER") {
		t.Errorf("missing split suggestion:\n%s", out)
	}
}

func TestForeignKeyOrphanCheck(t *testing.T) {
	stmt := &model.Statement{
		Table: "orders", Kind: model.KindAlterTable, PredictedAlgorithm: model.AlgorithmInplace,
		Operations: []model.Operation{{
			Kind: model.OpAddForeignKey, ReferencedTable: "customers",
			IndexColumns: []string{"customer_id"}, ReferencedColumns: []string{"id"},
		}},
	}
	out := strings.Join(Suggest(stmt, nil), "\n")
	if !strings.Contains(out, "LEFT JOIN `customers`") || !strings.Contains(out, "c.`customer_id` = p.`id`") {
		t.Errorf("missing orphan SQL:\n%s", out)
	}
}

func TestGhostCaveatsAreSpecific(t *testing.T) {
	table := &catalog.TableInfo{Name: "orders", Columns: []catalog.Column{{Name: "id"}}, Indexes: []catalog.Index{{Name: "PRIMARY"}}}
	stmt := &model.Statement{
		Table: "orders", Kind: model.KindAlterTable, PredictedAlgorithm: model.AlgorithmCopy,
		Operations: []model.Operation{{Kind: model.OpModifyColumn}},
	}
	out := strings.Join(Suggest(stmt, table), "\n")
	if !strings.Contains(out, "expand/contract") || strings.Count(out, "gh-ost") > 2 {
		t.Errorf("unexpected copy suggestions:\n%s", out)
	}
	table.Triggers = []catalog.Trigger{{Name: "trg"}}
	out = strings.Join(Suggest(stmt, table), "\n")
	if !strings.Contains(out, "table has triggers") {
		t.Errorf("missing trigger caveat:\n%s", out)
	}
}
