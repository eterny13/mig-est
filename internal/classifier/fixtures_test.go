package classifier

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

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
	for _, want := range expected {
		t.Run(want.File, func(t *testing.T) {
			sql, err := os.ReadFile(filepath.Join(root, "migrations", want.File))
			if err != nil {
				t.Fatal(err)
			}
			stmts, err := parser.ParseSQL(want.File, string(sql))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(stmts) != 1 {
				t.Fatalf("got %d statements, want 1", len(stmts))
			}
			got := cl.Classify(stmts[0], nil)
			if string(got.Algorithm) != want.Algorithm || string(got.Risk) != want.Risk {
				t.Errorf("algorithm/risk = %s/%s, want %s/%s", got.Algorithm, got.Risk, want.Algorithm, want.Risk)
			}
			if !reflect.DeepEqual(got.RuleIDs, want.RuleIDs) {
				t.Errorf("rules = %v, want %v", got.RuleIDs, want.RuleIDs)
			}
		})
	}
}
