package classifier

import (
	"testing"

	"github.com/eterny13/mig-est/internal/catalog"
	"github.com/eterny13/mig-est/internal/model"
	"github.com/eterny13/mig-est/internal/mysqlver"
	"github.com/eterny13/mig-est/internal/parser"
)

func mustVersion(t *testing.T, raw string) mysqlver.Version {
	t.Helper()
	v, err := mysqlver.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func classifySQL(t *testing.T, version string, table *catalog.TableInfo, sql string) Result {
	t.Helper()
	stmts, err := parser.ParseSQL("test.sql", sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1", len(stmts))
	}
	return New(mustVersion(t, version)).Classify(stmts[0], table)
}

func completeTable() *catalog.TableInfo {
	return &catalog.TableInfo{
		Schema: "app", Name: "orders", Exists: true, Source: "online",
		Engine: "InnoDB", RowFormat: "DYNAMIC", Rows: 100000,
		ColumnsKnown: true, IndexesKnown: true, TriggersKnown: true, FKsKnown: true,
		MetadataComplete: true,
		Columns: []catalog.Column{
			{Name: "id", TypeRaw: "bigint", Nullable: false},
			{Name: "status", TypeRaw: "varchar(50)", Nullable: true},
			{Name: "amount", TypeRaw: "int", Nullable: false},
			{Name: "kind", TypeRaw: "enum('a','b')", Nullable: false},
		},
		Indexes: []catalog.Index{{Name: "PRIMARY", Unique: true, Type: "BTREE", Columns: []string{"id"}}},
	}
}

func TestAddColumnVersionAndRisk(t *testing.T) {
	table := completeTable()
	res := classifySQL(t, "8.0.28", table, "ALTER TABLE orders ADD COLUMN note VARCHAR(20) NOT NULL DEFAULT 'new'")
	if res.Algorithm != model.AlgorithmInstant || res.Risk != model.RiskLow {
		t.Errorf("8.0.28 add column = %s/%s, want INSTANT/LOW; rules=%v", res.Algorithm, res.Risk, res.RuleIDs)
	}
	res = classifySQL(t, "8.0.28", table, "ALTER TABLE orders ADD COLUMN note VARCHAR(20) AFTER status")
	if res.Algorithm != model.AlgorithmInplace || res.Risk != model.RiskMedium {
		t.Errorf("8.0.28 positioned add = %s/%s, want INPLACE/MEDIUM", res.Algorithm, res.Risk)
	}
	res = classifySQL(t, "8.0.29", table, "ALTER TABLE orders ADD COLUMN note VARCHAR(20) AFTER status")
	if res.Algorithm != model.AlgorithmInstant {
		t.Errorf("8.0.29 positioned add = %s, want INSTANT", res.Algorithm)
	}
	res = classifySQL(t, "8.0.28", table, "ALTER TABLE orders ADD COLUMN note VARCHAR(20) NOT NULL")
	if res.Risk != model.RiskMedium {
		t.Errorf("NOT NULL without default risk = %s, want MEDIUM", res.Risk)
	}
	if !hasRule(res.RuleIDs, "ADD-COL-01") {
		t.Errorf("missing add-column rule: %v", res.RuleIDs)
	}
}

func TestDropColumnVersion(t *testing.T) {
	table := completeTable()
	res := classifySQL(t, "8.0.28", table, "ALTER TABLE orders DROP COLUMN status")
	if res.Algorithm != model.AlgorithmInplace {
		t.Errorf("8.0.28 drop = %s, want INPLACE", res.Algorithm)
	}
	res = classifySQL(t, "8.0.29", table, "ALTER TABLE orders DROP COLUMN status")
	if res.Algorithm != model.AlgorithmInstant {
		t.Errorf("8.0.29 drop = %s, want INSTANT", res.Algorithm)
	}
}

func TestIndexOperations(t *testing.T) {
	table := completeTable()
	res := classifySQL(t, "8.0.36", table, "CREATE INDEX idx_status ON orders (status)")
	if res.Algorithm != model.AlgorithmInplace || res.Lock != "NONE" {
		t.Errorf("create index = %s/%s, want INPLACE/NONE", res.Algorithm, res.Lock)
	}
	res = classifySQL(t, "8.0.36", table, "ALTER TABLE orders ADD PRIMARY KEY (status)")
	if res.Algorithm != model.AlgorithmCopy || res.Risk != model.RiskHigh {
		t.Errorf("add primary key = %s/%s, want COPY/HIGH", res.Algorithm, res.Risk)
	}
	res = classifySQL(t, "8.0.36", table, "ALTER TABLE orders ADD FULLTEXT INDEX ft (status)")
	if res.Algorithm != model.AlgorithmInplace || res.Lock != "SHARED" || res.Risk != model.RiskHigh {
		t.Errorf("fulltext index = %s/%s/%s, want INPLACE/SHARED/HIGH", res.Algorithm, res.Lock, res.Risk)
	}
}

func TestTypeChanges(t *testing.T) {
	table := completeTable()
	tests := []struct {
		name string
		sql  string
		alg  model.Algorithm
		risk model.Risk
	}{
		{"widen varchar", "ALTER TABLE orders MODIFY COLUMN status VARCHAR(100)", model.AlgorithmInplace, model.RiskMedium},
		{"narrow varchar", "ALTER TABLE orders MODIFY COLUMN status VARCHAR(20)", model.AlgorithmCopy, model.RiskHigh},
		{"widen int", "ALTER TABLE orders MODIFY COLUMN amount BIGINT", model.AlgorithmInplace, model.RiskMedium},
		{"narrow int", "ALTER TABLE orders MODIFY COLUMN amount SMALLINT", model.AlgorithmCopy, model.RiskHigh},
		{"signed change", "ALTER TABLE orders MODIFY COLUMN amount BIGINT UNSIGNED", model.AlgorithmCopy, model.RiskHigh},
		{"enum append", "ALTER TABLE orders MODIFY COLUMN kind ENUM('a','b','c')", model.AlgorithmInplace, model.RiskMedium},
		{"enum reorder", "ALTER TABLE orders MODIFY COLUMN kind ENUM('b','a')", model.AlgorithmCopy, model.RiskHigh},
		{"not null", "ALTER TABLE orders MODIFY COLUMN status VARCHAR(50) NOT NULL", model.AlgorithmInplace, model.RiskMedium},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := classifySQL(t, "8.0.36", table, tt.sql)
			if res.Algorithm != tt.alg || res.Risk != tt.risk {
				t.Errorf("got %s/%s, want %s/%s; rules=%v notes=%v", res.Algorithm, res.Risk, tt.alg, tt.risk, res.RuleIDs, res.Notes)
			}
		})
	}
}

func TestCharsetAndMultiOperation(t *testing.T) {
	table := completeTable()
	res := classifySQL(t, "8.0.36", table, "ALTER TABLE orders CONVERT TO CHARACTER SET utf8mb4")
	if res.Algorithm != model.AlgorithmCopy || res.Risk != model.RiskHigh {
		t.Errorf("convert charset = %s/%s, want COPY/HIGH", res.Algorithm, res.Risk)
	}
	res = classifySQL(t, "8.0.36", table, "ALTER TABLE orders ADD COLUMN note VARCHAR(20), ADD PRIMARY KEY (status)")
	if res.Algorithm != model.AlgorithmCopy {
		t.Errorf("combined alter = %s, want COPY", res.Algorithm)
	}
	if !hasRule(res.RuleIDs, "ADD-COL-01") || !hasRule(res.RuleIDs, "PK-ADD-01") {
		t.Errorf("combined rules = %v, want both operation rules", res.RuleIDs)
	}
}

func TestExplicitAlgorithmAndLockValidation(t *testing.T) {
	table := completeTable()
	res := classifySQL(t, "8.0.36", table, "CREATE INDEX idx_status ON orders (status) ALGORITHM=INSTANT")
	if res.Risk != model.RiskCritical || !hasRule(res.RuleIDs, "ALGO-01") {
		t.Errorf("invalid ALGORITHM=INSTANT = %s/%v, want CRITICAL/ALGO-01", res.Risk, res.RuleIDs)
	}
	res = classifySQL(t, "8.0.36", table, "ALTER TABLE orders ADD PRIMARY KEY (status), LOCK=NONE")
	if res.Risk != model.RiskCritical || !hasRule(res.RuleIDs, "ALGO-02") {
		t.Errorf("invalid LOCK=NONE = %s/%v, want CRITICAL/ALGO-02", res.Risk, res.RuleIDs)
	}
	res = classifySQL(t, "8.0.36", table, "ALTER TABLE orders ADD COLUMN note VARCHAR(20), ALGORITHM=INSTANT, LOCK=NONE")
	if res.Risk == model.RiskCritical || res.Algorithm != model.AlgorithmInstant {
		t.Errorf("valid explicit instant = %s/%s/%v", res.Algorithm, res.Risk, res.RuleIDs)
	}
	res = classifySQL(t, "8.0.36", table, "ALTER TABLE orders ADD COLUMN note VARCHAR(20), ALGORITHM=COPY")
	if res.Algorithm != model.AlgorithmCopy || res.Risk != model.RiskHigh || !hasRule(res.RuleIDs, "ALGO-03") {
		t.Errorf("explicit COPY = %s/%s/%v, want COPY/HIGH/ALGO-03", res.Algorithm, res.Risk, res.RuleIDs)
	}
}

func TestIncompleteMetadataIsConservative(t *testing.T) {
	res := classifySQL(t, "8.0.36", nil, "ALTER TABLE orders ADD COLUMN note VARCHAR(20)")
	if res.Risk != model.RiskMedium || !hasRule(res.RuleIDs, "META-01") {
		t.Errorf("nil metadata = %s/%v, want MEDIUM/META-01", res.Risk, res.RuleIDs)
	}
}

func TestCreateTableRules(t *testing.T) {
	res := classifySQL(t, "8.0.36", nil, "CREATE TABLE users (id BIGINT PRIMARY KEY, email VARCHAR(255))")
	if res.Risk != model.RiskLow {
		t.Errorf("create table with PK risk = %s, want LOW", res.Risk)
	}
	res = classifySQL(t, "8.0.36", nil, "CREATE TABLE users (id BIGINT, email VARCHAR(255))")
	if res.Risk != model.RiskMedium || !hasRule(res.RuleIDs, "PK-02") {
		t.Errorf("create table without PK = %s/%v, want MEDIUM/PK-02", res.Risk, res.RuleIDs)
	}
}

func hasRule(rules []string, want string) bool {
	for _, r := range rules {
		if r == want {
			return true
		}
	}
	return false
}
