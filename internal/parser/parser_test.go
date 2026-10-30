package parser

import (
	"strings"
	"testing"

	"github.com/eterny13/mig-est/internal/model"
)

func TestSplitSQLCommentsQuotesAndLines(t *testing.T) {
	input := "-- leading comment\n" +
		"CREATE TABLE t (note varchar(20) DEFAULT ';');\n\n" +
		"/* block ; comment */\n" +
		"ALTER TABLE t ADD COLUMN n INT; # trailing ; comment\n"
	pieces := SplitSQL(input)
	if len(pieces) != 2 {
		t.Fatalf("got %d pieces, want 2: %#v", len(pieces), pieces)
	}
	if pieces[0].Line != 2 {
		t.Errorf("first line = %d, want 2", pieces[0].Line)
	}
	if pieces[1].Line != 5 {
		t.Errorf("second line = %d, want 5", pieces[1].Line)
	}
	if !strings.Contains(pieces[0].SQL, "DEFAULT ';'") {
		t.Errorf("quoted semicolon was split: %q", pieces[0].SQL)
	}
}

func TestSplitSQLDelimiter(t *testing.T) {
	input := "CREATE TABLE a (id INT);\n" +
		"DELIMITER $$\n" +
		"CREATE PROCEDURE p()\n" +
		"BEGIN\n" +
		"  SELECT 1;\n" +
		"  SELECT 2;\n" +
		"END$$\n" +
		"DELIMITER ;\n" +
		"ALTER TABLE a ADD COLUMN b INT;\n"
	pieces := SplitSQL(input)
	if len(pieces) != 3 {
		t.Fatalf("got %d pieces, want 3: %#v", len(pieces), pieces)
	}
	if !strings.HasPrefix(pieces[1].SQL, "CREATE PROCEDURE") || !strings.Contains(pieces[1].SQL, "SELECT 2") {
		t.Errorf("procedure body was not kept together: %q", pieces[1].SQL)
	}
	if pieces[2].Line != 9 {
		t.Errorf("third line = %d, want 9", pieces[2].Line)
	}
}

func TestParseSQLExtractsOperations(t *testing.T) {
	sql := "ALTER TABLE `shop`.`orders`\n" +
		"  ADD COLUMN `note` TEXT AFTER `status`,\n" +
		"  MODIFY COLUMN `amount` BIGINT UNSIGNED NOT NULL,\n" +
		"  RENAME COLUMN `old_name` TO `new_name`,\n" +
		"  ADD UNIQUE INDEX `uniq_code` (`code`),\n" +
		"  ADD CONSTRAINT `fk_customer` FOREIGN KEY (`customer_id`) REFERENCES `customers` (`id`),\n" +
		"  ALGORITHM=INPLACE, LOCK=NONE"
	stmts, err := ParseSQL("migration.sql", sql)
	if err != nil {
		t.Fatalf("ParseSQL: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1", len(stmts))
	}
	s := stmts[0]
	if s.Kind != model.KindAlterTable || s.Table != "orders" || s.Schema != "shop" {
		t.Fatalf("unexpected statement: %+v", s)
	}
	if s.Algorithm != "INPLACE" || s.Lock != "NONE" {
		t.Errorf("algorithm/lock = %q/%q, want INPLACE/NONE", s.Algorithm, s.Lock)
	}
	if len(s.Options) != 5 {
		t.Fatalf("got %d options, want 5: %+v", len(s.Options), s.Options)
	}
	if s.Options[0].Kind != model.OpAddColumn || s.Options[0].Column.Position != "AFTER status" {
		t.Errorf("unexpected add column: %+v", s.Options[0])
	}
	if s.Options[1].Kind != model.OpModifyColumn || s.Options[1].Column.Type.Base != "bigint" || !s.Options[1].Column.Type.Unsigned {
		t.Errorf("unexpected modify column: %+v", s.Options[1])
	}
	if s.Options[2].Kind != model.OpRenameColumn || s.Options[2].OldColumn != "old_name" {
		t.Errorf("unexpected rename column: %+v", s.Options[2])
	}
	if s.Options[3].Kind != model.OpAddIndex || s.Options[3].Index.Kind != "UNIQUE" {
		t.Errorf("unexpected add index: %+v", s.Options[3])
	}
	if s.Options[4].Kind != model.OpAddForeignKey || s.Options[4].ForeignKey.ReferencedTable != "customers" {
		t.Errorf("unexpected foreign key: %+v", s.Options[4])
	}
}

func TestParseIndexKinds(t *testing.T) {
	tests := []struct {
		sql  string
		kind model.StatementKind
	}{
		{"CREATE INDEX idx ON users (email)", model.KindCreateIndex},
		{"CREATE UNIQUE INDEX idx ON users (email)", model.KindCreateIndex},
		{"DROP INDEX idx ON users", model.KindDropIndex},
	}
	for _, tt := range tests {
		stmts, err := ParseSQL("x.sql", tt.sql)
		if err != nil {
			t.Fatalf("%s: %v", tt.sql, err)
		}
		if len(stmts) != 1 || stmts[0].Kind != tt.kind {
			t.Errorf("%s: kind = %+v, want %s", tt.sql, stmts, tt.kind)
		}
		if stmts[0].Table != "users" {
			t.Errorf("%s: table = %q, want users", tt.sql, stmts[0].Table)
		}
	}
}

func TestParseCreateTable(t *testing.T) {
	stmts, err := ParseSQL("x.sql", "CREATE TABLE `users` (`id` BIGINT NOT NULL, `email` VARCHAR(255), PRIMARY KEY (`id`), UNIQUE KEY `uniq_email` (`email`)) ENGINE=InnoDB")
	if err != nil {
		t.Fatalf("ParseSQL: %v", err)
	}
	s := stmts[0]
	if s.Kind != model.KindCreateTable || s.CreateTable == nil {
		t.Fatalf("unexpected statement: %+v", s)
	}
	if !s.CreateTable.HasPrimaryKey || !s.CreateTable.HasUniqueKey {
		t.Errorf("PK/unique not detected: %+v", s.CreateTable)
	}
	if len(s.CreateTable.Columns) != 2 || s.CreateTable.Columns[0].Type.Base != "bigint" {
		t.Errorf("columns not extracted: %+v", s.CreateTable.Columns)
	}
}

func TestParseSQLRejectsInvalidSQL(t *testing.T) {
	if _, err := ParseSQL("bad.sql", "ALTER TABLE orders ADD COLUMN"); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestParseSQLUnsupportedStoredProgram(t *testing.T) {
	sql := "DELIMITER $$\nCREATE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW BEGIN SET @x = 1; END$$\nDELIMITER ;\n"
	stmts, err := ParseSQL("x.sql", sql)
	if err != nil {
		t.Fatalf("ParseSQL: %v", err)
	}
	if len(stmts) != 1 || !stmts[0].Unsupported || stmts[0].Kind != model.KindUnsupported {
		t.Fatalf("unexpected statements: %+v", stmts)
	}
}
