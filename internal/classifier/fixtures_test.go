package classifier

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eterny13/mig-est/internal/model"
	"github.com/eterny13/mig-est/internal/mysqlver"
	"github.com/eterny13/mig-est/internal/parser"
)

type goldenEntry struct {
	File      string   `json:"file"`
	Algorithm string   `json:"algorithm"`
	Risk      string   `json:"risk"`
	RuleIDs   []string `json:"rule_ids"`
}

func TestGoldenClassifications(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")
	data, err := os.ReadFile(filepath.Join(root, "expected", "classification.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected []goldenEntry
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	ver, err := mysqlver.Parse("8.0.28")
	if err != nil {
		t.Fatal(err)
	}
	cl := New(ver)
	seen := make(map[string]bool, len(expected))
	alterPatterns := 0
	for _, want := range expected {
		seen[want.File] = true
		sqlBytes, err := os.ReadFile(filepath.Join(root, "migrations", want.File))
		if err != nil {
			t.Fatalf("%s: %v", want.File, err)
		}
		stmts, err := parser.ParseSQL(want.File, string(sqlBytes))
		if err != nil {
			t.Fatalf("%s: parse: %v", want.File, err)
		}
		if len(stmts) != 1 {
			t.Fatalf("%s: got %d statements, want 1", want.File, len(stmts))
		}
		switch stmts[0].Kind {
		case model.KindAlterTable, model.KindCreateIndex, model.KindDropIndex:
			alterPatterns++
		}
		t.Run(want.File, func(t *testing.T) {
			got := cl.Classify(stmts[0], nil)
			if string(got.Algorithm) != want.Algorithm || string(got.Risk) != want.Risk {
				t.Errorf("algorithm/risk = %s/%s, want %s/%s", got.Algorithm, got.Risk, want.Algorithm, want.Risk)
			}
			if !reflect.DeepEqual(got.RuleIDs, want.RuleIDs) {
				t.Errorf("rules = %v, want %v", got.RuleIDs, want.RuleIDs)
			}
		})
	}
	if alterPatterns < 20 {
		t.Errorf("got %d golden ALTER/index fixtures, want at least 20", alterPatterns)
	}

	entries, err := os.ReadDir(filepath.Join(root, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if !seen[entry.Name()] {
			t.Errorf("fixture %s in testdata/migrations is missing from testdata/expected/classification.json", entry.Name())
		}
	}
}
