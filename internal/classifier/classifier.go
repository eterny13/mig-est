// Package classifier predicts the MySQL execution algorithm and base risk for
// parsed migration statements. It uses only SQL, table metadata, and the
// target MySQL version; it never contacts a database.
package classifier

import (
	"fmt"
	"strings"

	"github.com/eterny13/mig-est/internal/catalog"
	"github.com/eterny13/mig-est/internal/model"
	"github.com/eterny13/mig-est/internal/mysqlver"
	"github.com/eterny13/mig-est/internal/parser"
)

// Result is the classifier output for one statement.
type Result struct {
	Algorithm  model.Algorithm
	Lock       string
	Risk       model.Risk
	Operations []model.Operation
	RuleIDs    []string
	Notes      []string
}

// Classifier predicts algorithms for a fixed MySQL version.
type Classifier struct {
	Version mysqlver.Version
}

// New creates a classifier.
func New(v mysqlver.Version) *Classifier { return &Classifier{Version: v} }

// Classify analyzes one parsed statement. table may be nil in offline mode.
func (c *Classifier) Classify(stmt *parser.Statement, table *catalog.TableInfo) Result {
	switch stmt.Kind {
	case model.KindAlterTable, model.KindCreateIndex, model.KindDropIndex:
		return c.classifyAlter(stmt, table)
	case model.KindCreateTable:
		return classifyCreateTable(stmt)
	case model.KindDropTable:
		return classifyDropTable(stmt, table)
	case model.KindRenameTable:
		return Result{
			Algorithm: model.AlgorithmInstant,
			Lock:      "METADATA",
			Risk:      model.RiskLow,
			Operations: []model.Operation{{
				Kind: model.OpRenameTable, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
				RuleID: "RENAME-TABLE-01", Risk: model.RiskLow, Note: "metadata-only operation",
			}},
			RuleIDs: []string{"RENAME-TABLE-01"},
			Notes:   []string{"RENAME TABLE is atomic and metadata-only; it still needs a brief metadata lock."},
		}
	case model.KindUnsupported:
		return Result{
			Algorithm: model.AlgorithmUnknown,
			Lock:      "UNKNOWN",
			Risk:      model.RiskMedium,
			Operations: []model.Operation{{
				Kind: model.OpOther, Algorithm: model.AlgorithmUnknown, Lock: "UNKNOWN",
				RuleID: "UNSUPPORTED-01", Risk: model.RiskMedium, Note: stmt.UnsupportedReason,
			}},
			RuleIDs: []string{"UNSUPPORTED-01"},
			Notes:   []string{"This statement is not analyzed in v0.1. It is not reported as safe; review it manually."},
		}
	default:
		return Result{
			Algorithm: model.AlgorithmUnknown,
			Lock:      "UNKNOWN",
			Risk:      model.RiskLow,
			Operations: []model.Operation{{
				Kind: model.OpOther, Algorithm: model.AlgorithmUnknown, Lock: "UNKNOWN",
				RuleID: "OUT-OF-SCOPE-01", Risk: model.RiskLow, Note: "not a target DDL statement",
			}},
			RuleIDs: []string{"OUT-OF-SCOPE-01"},
			Notes:   []string{"Out of scope for v0.1; excluded from the overall risk."},
		}
	}
}

func (c *Classifier) classifyAlter(stmt *parser.Statement, table *catalog.TableInfo) Result {
	res := Result{}
	for _, opt := range stmt.Options {
		op := c.classifyOption(opt, table)
		res.Operations = append(res.Operations, op)
		addRule(&res, op.RuleID)
		if op.Note != "" {
			res.Notes = append(res.Notes, op.Note)
		}
	}
	algos := make([]model.Algorithm, 0, len(res.Operations))
	locks := make([]string, 0, len(res.Operations))
	for _, op := range res.Operations {
		algos = append(algos, op.Algorithm)
		locks = append(locks, op.Lock)
	}
	res.Algorithm = model.CombineAlgorithms(algos)
	res.Lock = combineLocks(locks)
	res.Risk = model.RiskLow
	for _, op := range res.Operations {
		res.Risk = model.MaxRisk(res.Risk, op.Risk)
	}
	res.Risk = maxRiskForAlgorithm(res.Risk, res.Algorithm)

	// Without table metadata, the SQL-only prediction is only a theoretical
	// best case. Raise the risk one level and say so.
	if (table == nil || !table.MetadataComplete) && res.Algorithm != model.AlgorithmCopy {
		res.Risk = model.MaxRisk(res.Risk, model.RiskMedium)
		res.Notes = append(res.Notes, "table metadata is incomplete; the algorithm assumes InnoDB, DYNAMIC row format, no FULLTEXT index, and a supported version.")
		addRule(&res, "META-01")
	}
	if table != nil && table.Partitioned {
		res.Notes = append(res.Notes, "table is partitioned; verify algorithm support on the target version.")
	}
	c.applyExplicit(stmt, &res)
	return res
}

func (c *Classifier) classifyOption(opt parser.Option, table *catalog.TableInfo) model.Operation {
	switch opt.Kind {
	case model.OpAddColumn:
		return c.addColumn(opt, table)
	case model.OpDropColumn:
		return c.dropColumn(opt, table)
	case model.OpModifyColumn, model.OpChangeColumn:
		return c.modifyColumn(opt, table)
	case model.OpRenameColumn:
		return model.Operation{
			Kind: model.OpRenameColumn, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
			RuleID: "RENAME-COL-01", Risk: model.RiskLow,
			OldColumnName: opt.OldColumn, NewColumnName: opt.Column.Name,
			Note: "RENAME COLUMN is metadata-only in MySQL 8.0+.",
		}
	case model.OpAddIndex, model.OpAddPrimaryKey:
		return c.addIndex(opt)
	case model.OpDropIndex, model.OpDropPrimaryKey:
		return c.dropIndex(opt)
	case model.OpRenameIndex:
		return model.Operation{
			Kind: model.OpRenameIndex, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
			RuleID: "IDX-RENAME-01", Risk: model.RiskLow,
			IndexName: opt.Note, Value: opt.OldColumn,
			Note: "RENAME INDEX is metadata-only.",
		}
	case model.OpSetDefault, model.OpDropDefault:
		return model.Operation{
			Kind: opt.Kind, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
			RuleID: "DEFAULT-01", Risk: model.RiskLow, ColumnName: columnName(opt),
			Note: "changing a column default is a metadata-only operation.",
		}
	case model.OpSetVisibility:
		if c.Version.AtLeast(8, 0, 23) {
			return model.Operation{
				Kind: model.OpSetVisibility, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
				RuleID: "VIS-01", Risk: model.RiskLow, ColumnName: columnName(opt), IndexName: opt.Note,
				Note: "column/index visibility is metadata-only on MySQL 8.0.23+.",
			}
		}
		return unknownOp(opt, "visibility changes require MySQL 8.0.23+; confirm support on the target version.")
	case model.OpAddForeignKey:
		fk := opt.ForeignKey
		return model.Operation{
			Kind: model.OpAddForeignKey, Algorithm: model.AlgorithmInplace, Lock: "SHARED",
			RuleID: "FK-ADD-01", Risk: model.RiskMedium,
			ConstraintName: fkName(fk), IndexColumns: fkColumns(fk), ReferencedTable: fkTable(fk), ReferencedColumns: fkRefColumns(fk),
			Note: "ADD FOREIGN KEY validates all existing rows; MySQL has no NOT VALID equivalent.",
		}
	case model.OpDropForeignKey:
		return model.Operation{
			Kind: model.OpDropForeignKey, Algorithm: model.AlgorithmInplace, Lock: "NONE",
			RuleID: "FK-DROP-01", Risk: model.RiskLow, ConstraintName: opt.Note,
			Note: "dropping a foreign key is metadata-only.",
		}
	case model.OpAddCheck:
		return model.Operation{
			Kind: model.OpAddCheck, Algorithm: model.AlgorithmInplace, Lock: "SHARED",
			RuleID: "CHECK-ADD-01", Risk: model.RiskMedium, ConstraintName: opt.Note,
			Note: "ADD CHECK validates all existing rows.",
		}
	case model.OpDropCheck:
		return model.Operation{
			Kind: model.OpDropCheck, Algorithm: model.AlgorithmInplace, Lock: "NONE",
			RuleID: "CHECK-DROP-01", Risk: model.RiskLow, ConstraintName: opt.Note,
		}
	case model.OpConvertCharset:
		return model.Operation{
			Kind: model.OpConvertCharset, Algorithm: model.AlgorithmCopy, Lock: "EXCLUSIVE",
			RuleID: "CHARSET-01", Risk: model.RiskHigh, Value: opt.Note,
			Note: "CONVERT TO CHARACTER SET rewrites every row and changes column definitions.",
		}
	case model.OpSetTableCharset:
		return model.Operation{
			Kind: model.OpSetTableCharset, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
			RuleID: "CHARSET-02", Risk: model.RiskLow, TableOptionName: opt.TableOptionName, TableOptionValue: opt.TableOptionValue,
			Note: "changing only the table default charset does not convert existing data.",
		}
	case model.OpSetEngine:
		current := ""
		if table != nil {
			current = table.Engine
		}
		if current != "" && strings.EqualFold(current, opt.TableOptionValue) {
			return model.Operation{
				Kind: model.OpSetEngine, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
				RuleID: "ENGINE-02", Risk: model.RiskLow, TableOptionName: opt.TableOptionName, TableOptionValue: opt.TableOptionValue,
				Note: "engine is already " + current + "; this is a no-op.",
			}
		}
		return model.Operation{
			Kind: model.OpSetEngine, Algorithm: model.AlgorithmCopy, Lock: "EXCLUSIVE",
			RuleID: "ENGINE-01", Risk: model.RiskHigh, TableOptionName: opt.TableOptionName, TableOptionValue: opt.TableOptionValue,
			Note: "changing the storage engine rebuilds the table.",
		}
	case model.OpSetRowFormat:
		if table != nil && strings.EqualFold(table.RowFormat, opt.TableOptionValue) {
			return model.Operation{
				Kind: model.OpSetRowFormat, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
				RuleID: "ROWFMT-02", Risk: model.RiskLow, TableOptionName: opt.TableOptionName, TableOptionValue: opt.TableOptionValue,
				Note: "row format is already " + table.RowFormat + "; this is a no-op.",
			}
		}
		return model.Operation{
			Kind: model.OpSetRowFormat, Algorithm: model.AlgorithmCopy, Lock: "EXCLUSIVE",
			RuleID: "ROWFMT-01", Risk: model.RiskHigh, TableOptionName: opt.TableOptionName, TableOptionValue: opt.TableOptionValue,
			Note: "changing ROW_FORMAT rebuilds the table.",
		}
	case model.OpSetTableOption:
		return model.Operation{
			Kind: model.OpSetTableOption, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
			RuleID: "TABLEOPT-01", Risk: model.RiskLow, TableOptionName: opt.TableOptionName, TableOptionValue: opt.TableOptionValue,
			Note: "table option changes are metadata-only.",
		}
	case model.OpPartition:
		return model.Operation{
			Kind: model.OpPartition, Algorithm: model.AlgorithmUnknown, Lock: "UNKNOWN",
			RuleID: "PART-01", Risk: model.RiskMedium,
			Note: "partition operations vary by action and version; review on staging.",
		}
	case model.OpRenameTable:
		return model.Operation{
			Kind: model.OpRenameTable, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
			RuleID: "RENAME-TABLE-01", Risk: model.RiskLow, Value: opt.Note,
			Note: "renaming a table is metadata-only.",
		}
	case model.OpForce:
		return model.Operation{
			Kind: model.OpForce, Algorithm: model.AlgorithmCopy, Lock: "EXCLUSIVE",
			RuleID: "FORCE-01", Risk: model.RiskHigh,
			Note: "ALTER TABLE ... FORCE rebuilds the table.",
		}
	case model.OpOrderBy:
		return model.Operation{
			Kind: model.OpOrderBy, Algorithm: model.AlgorithmCopy, Lock: "EXCLUSIVE",
			RuleID: "ORDER-01", Risk: model.RiskHigh,
			Note: "ALTER TABLE ... ORDER BY rebuilds and reorders the table.",
		}
	case model.OpKeyState:
		return model.Operation{
			Kind: model.OpKeyState, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
			RuleID: "KEYSTATE-01", Risk: model.RiskLow,
			Note: "enabling/disabling keys is metadata-only for InnoDB.",
		}
	case model.OpTablespace:
		return model.Operation{
			Kind: model.OpTablespace, Algorithm: model.AlgorithmUnknown, Lock: "UNKNOWN",
			RuleID: "TSP-01", Risk: model.RiskMedium,
			Note: "tablespace import/discard is not classified in v0.1.",
		}
	default:
		return unknownOp(opt, "operation is not covered by the v0.1 classification matrix.")
	}
}

func (c *Classifier) addColumn(opt parser.Option, table *catalog.TableInfo) model.Operation {
	col := opt.Column
	op := model.Operation{
		Kind: model.OpAddColumn, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
		RuleID: "ADD-COL-01", Risk: model.RiskLow,
		ColumnName: col.Name, ColumnType: col.Type.String(), Position: col.Position,
	}
	note := "ADD COLUMN is INSTANT on MySQL 8.0.12+."
	if !c.Version.AtLeast(8, 0, 12) {
		op.Algorithm = model.AlgorithmInplace
		op.Lock = "NONE"
		op.RuleID = "ADD-COL-03"
		op.Risk = model.RiskMedium
		note = "ADD COLUMN is INPLACE before MySQL 8.0.12."
	} else if col.Position != "" && !c.Version.AtLeast(8, 0, 29) {
		op.Algorithm = model.AlgorithmInplace
		op.Lock = "NONE"
		op.RuleID = "ADD-COL-02"
		op.Risk = model.RiskMedium
		note = "ADD COLUMN with FIRST/AFTER is INPLACE before MySQL 8.0.29; in 8.0.29+ it can be INSTANT."
	} else if table != nil && table.HasFullText() {
		op.Algorithm = model.AlgorithmInplace
		op.Lock = "NONE"
		op.RuleID = "FTS-01"
		op.Risk = model.RiskMedium
		note = "table has a FULLTEXT index; INSTANT ADD COLUMN is not supported, so INPLACE is used."
	} else if table != nil && strings.EqualFold(table.RowFormat, "COMPRESSED") {
		op.Algorithm = model.AlgorithmInplace
		op.Lock = "NONE"
		op.RuleID = "ROWFMT-01"
		op.Risk = model.RiskMedium
		note = "ROW_FORMAT=COMPRESSED tables do not support INSTANT ADD COLUMN; INPLACE is used."
	} else if col.AutoIncrement {
		op.Algorithm = model.AlgorithmInplace
		op.Lock = "NONE"
		op.RuleID = "ADD-COL-04"
		op.Risk = model.RiskMedium
		note = "adding an AUTO_INCREMENT column requires a table rebuild."
	}
	if col.Nullable != nil && !*col.Nullable && !col.HasDefault {
		op.Risk = model.MaxRisk(op.Risk, model.RiskMedium)
		op.Note = note + " New column is NOT NULL without a default; existing rows need a value."
		return op
	}
	op.Note = note
	return op
}

func (c *Classifier) dropColumn(opt parser.Option, table *catalog.TableInfo) model.Operation {
	op := model.Operation{
		Kind: model.OpDropColumn, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
		RuleID: "DROP-COL-01", Risk: model.RiskMedium, OldColumnName: opt.OldColumn,
	}
	note := "DROP COLUMN is INSTANT on MySQL 8.0.29+; dropped data remains in the tablespace until rebuild."
	if !c.Version.AtLeast(8, 0, 29) {
		op.Algorithm = model.AlgorithmInplace
		op.Lock = "NONE"
		op.RuleID = "DROP-COL-02"
		note = "DROP COLUMN is INPLACE before MySQL 8.0.29."
	} else if table != nil && (table.HasFullText() || strings.EqualFold(table.RowFormat, "COMPRESSED")) {
		op.Algorithm = model.AlgorithmInplace
		op.Lock = "NONE"
		op.RuleID = "DROP-COL-03"
		note = "table has FULLTEXT or COMPRESSED row format; DROP COLUMN may not be INSTANT."
	}
	op.Note = note + " Dropping a column is destructive; verify a backup or a two-phase expand/contract plan."
	return op
}

func (c *Classifier) modifyColumn(opt parser.Option, table *catalog.TableInfo) model.Operation {
	col := opt.Column
	op := model.Operation{
		Kind: opt.Kind, Algorithm: model.AlgorithmUnknown, Lock: "UNKNOWN",
		RuleID: "MOD-COL-01", Risk: model.RiskMedium,
		ColumnName: col.Name, ColumnType: col.Type.String(), OldColumnName: opt.OldColumn, Position: col.Position,
	}
	var old *catalog.Column
	if table != nil {
		old = table.FindColumn(opt.OldColumn)
		if old == nil {
			old = table.FindColumn(col.Name)
		}
	}
	if old == nil {
		op.Note = "old column metadata is unavailable; type compatibility cannot be confirmed. Review on staging."
		return op
	}
	oldType := CatalogType(old.TypeRaw)
	change := CompareColumnTypes(oldType, col.Type)
	op.Algorithm = change.Algorithm
	op.Risk = change.Risk
	op.RuleID = change.RuleID
	note := change.Note
	oldNullable := old.Nullable
	alg, risk, rule, nullNote := CompareNullability(&oldNullable, col.Nullable)
	if rule != "" {
		if model.AlgorithmRank(alg) > model.AlgorithmRank(op.Algorithm) || op.Algorithm == model.AlgorithmUnknown {
			op.Algorithm = alg
		}
		op.Risk = model.MaxRisk(op.Risk, risk)
		op.RuleID = rule
		note += " " + nullNote
	}
	if col.Position != "" {
		if op.Algorithm == model.AlgorithmInstant {
			op.Algorithm = model.AlgorithmInplace
			op.Risk = model.MaxRisk(op.Risk, model.RiskMedium)
			op.RuleID = "MOD-COL-03"
		}
		note += " Moving a column with FIRST/AFTER may require a rebuild."
	}
	if change.Algorithm == model.AlgorithmInstant && op.Algorithm == model.AlgorithmInstant {
		note += " If only the default changed, this is metadata-only."
	}
	switch op.Algorithm {
	case model.AlgorithmInstant:
		op.Lock = "METADATA"
	case model.AlgorithmInplace:
		op.Lock = "NONE"
	case model.AlgorithmCopy:
		op.Lock = "EXCLUSIVE"
	default:
		op.Lock = "UNKNOWN"
	}
	op.Note = note
	return op
}

func (c *Classifier) addIndex(opt parser.Option) model.Operation {
	idx := opt.Index
	op := model.Operation{
		Kind: model.OpAddIndex, Algorithm: model.AlgorithmInplace, Lock: "NONE",
		RuleID: "IDX-ADD-01", Risk: model.RiskMedium,
		IndexName: idx.Name, IndexKind: idx.Kind, IndexColumns: idx.Columns,
	}
	switch idx.Kind {
	case "PRIMARY":
		op.Kind = model.OpAddPrimaryKey
		op.Algorithm = model.AlgorithmCopy
		op.Lock = "EXCLUSIVE"
		op.RuleID = "PK-ADD-01"
		op.Risk = model.RiskHigh
		op.Note = "ADD PRIMARY KEY reorders existing rows and copies the table."
	case "FULLTEXT":
		op.Lock = "SHARED"
		op.RuleID = "IDX-ADD-03"
		op.Risk = model.RiskHigh
		op.Note = "adding a FULLTEXT index builds an auxiliary index and allows only shared locks."
	case "SPATIAL":
		op.Lock = "SHARED"
		op.RuleID = "IDX-ADD-04"
		op.Risk = model.RiskMedium
		op.Note = "adding a SPATIAL index uses INPLACE and permits only shared locks."
	case "UNIQUE":
		op.RuleID = "IDX-ADD-02"
		op.Note = "adding a UNIQUE index builds the index in place and validates uniqueness."
	default:
		op.Note = "adding a secondary index builds the index in place; LOCK=NONE is usually supported."
	}
	return op
}

func (c *Classifier) dropIndex(opt parser.Option) model.Operation {
	if opt.Kind == model.OpDropPrimaryKey {
		return model.Operation{
			Kind: model.OpDropPrimaryKey, Algorithm: model.AlgorithmCopy, Lock: "EXCLUSIVE",
			RuleID: "PK-DROP-01", Risk: model.RiskHigh,
			Note: "dropping a primary key usually copies the table and changes row organization.",
		}
	}
	return model.Operation{
		Kind: model.OpDropIndex, Algorithm: model.AlgorithmInplace, Lock: "NONE",
		RuleID: "IDX-DROP-01", Risk: model.RiskLow, IndexName: opt.Note,
		Note: "dropping a secondary index is metadata-only in most cases.",
	}
}

func classifyCreateTable(stmt *parser.Statement) Result {
	op := model.Operation{
		Kind: model.OpCreateTable, Algorithm: model.AlgorithmInstant, Lock: "METADATA",
		RuleID: "CREATE-01", Risk: model.RiskLow,
	}
	res := Result{
		Algorithm: model.AlgorithmInstant, Lock: "METADATA", Risk: model.RiskLow,
		Operations: []model.Operation{op},
		RuleIDs:    []string{"CREATE-01"},
	}
	if stmt.CreateTable != nil && stmt.CreateTable.HasSelect {
		res.Algorithm = model.AlgorithmCopy
		res.Lock = "EXCLUSIVE"
		res.Risk = model.RiskHigh
		res.Operations[0].Algorithm = model.AlgorithmCopy
		res.Operations[0].Lock = "EXCLUSIVE"
		res.Operations[0].RuleID = "CREATE-02"
		res.Operations[0].Risk = model.RiskHigh
		res.RuleIDs = []string{"CREATE-02"}
		res.Notes = append(res.Notes, "CREATE TABLE ... SELECT copies the result set into a new table.")
	}
	if stmt.CreateTable != nil && !stmt.CreateTable.HasPrimaryKey {
		if res.Risk == model.RiskLow {
			res.Risk = model.RiskMedium
			res.Operations[0].Risk = model.RiskMedium
		}
		addRule(&res, "PK-02")
		res.Notes = append(res.Notes, "new table has no primary key; online schema-change tools may be unable to operate on it later.")
	}
	return res
}

func classifyDropTable(stmt *parser.Statement, table *catalog.TableInfo) Result {
	op := model.Operation{
		Kind: model.OpDropTable, Algorithm: model.AlgorithmInstant, Lock: "EXCLUSIVE",
		RuleID: "DROP-TABLE-01", Risk: model.RiskHigh,
	}
	res := Result{
		Algorithm: model.AlgorithmInstant, Lock: "EXCLUSIVE", Risk: model.RiskHigh,
		Operations: []model.Operation{op},
		RuleIDs:    []string{"DROP-TABLE-01"},
		Notes:      []string{"DROP TABLE is destructive and cannot be rolled back; verify backups first."},
	}
	if table != nil && len(table.IncomingFKs) > 0 {
		res.Risk = model.RiskCritical
		res.Operations[0].Risk = model.RiskCritical
		addRule(&res, "FK-01")
		res.Notes = append(res.Notes, "other tables reference this table with foreign keys; DROP TABLE will fail until those constraints are removed.")
	}
	return res
}

func (c *Classifier) applyExplicit(stmt *parser.Statement, res *Result) {
	reqA := model.Algorithm(strings.ToUpper(stmt.Algorithm))
	if stmt.Algorithm != "" && reqA != "DEFAULT" {
		switch reqA {
		case model.AlgorithmInstant:
			if res.Algorithm != model.AlgorithmInstant {
				explicitFailure(res, "ALGO-01", fmt.Sprintf("ALGORITHM=INSTANT is not supported for this statement; MySQL would choose %s and reject the statement.", res.Algorithm))
			}
		case model.AlgorithmInplace:
			if res.Algorithm == model.AlgorithmCopy {
				explicitFailure(res, "ALGO-01", "ALGORITHM=INPLACE is not supported for this statement; MySQL would choose COPY and reject the statement.")
			} else if res.Algorithm == model.AlgorithmUnknown {
				res.Risk = model.MaxRisk(res.Risk, model.RiskHigh)
				res.Notes = append(res.Notes, "ALGORITHM=INPLACE was requested, but the catalog rule is UNKNOWN; MySQL may reject it or use a slower algorithm.")
				addRule(res, "ALGO-02")
			}
		case model.AlgorithmCopy:
			if res.Algorithm == model.AlgorithmInstant || res.Algorithm == model.AlgorithmInplace {
				previous := res.Algorithm
				res.Algorithm = model.AlgorithmCopy
				res.Lock = "EXCLUSIVE"
				res.Risk = model.RiskHigh
				res.Notes = append(res.Notes, fmt.Sprintf("ALGORITHM=COPY was requested; MySQL will copy the table instead of using %s.", previous))
				addRule(res, "ALGO-03")
			} else if res.Algorithm == model.AlgorithmUnknown {
				res.Risk = model.MaxRisk(res.Risk, model.RiskHigh)
				res.Notes = append(res.Notes, "ALGORITHM=COPY was requested, but the catalog rule is UNKNOWN; confirm that this statement supports COPY.")
				addRule(res, "ALGO-02")
			}
		default:
			res.Risk = model.RiskCritical
			res.Notes = append(res.Notes, "unknown ALGORITHM value in SQL; MySQL will reject the statement.")
			addRule(res, "ALGO-04")
		}
	}
	reqL := strings.ToUpper(stmt.Lock)
	if reqL != "" && reqL != "DEFAULT" {
		invalid := false
		switch reqL {
		case "NONE":
			if res.Lock == "SHARED" || res.Lock == "EXCLUSIVE" || res.Lock == "UNKNOWN" {
				invalid = true
			}
		case "SHARED":
			if res.Lock == "EXCLUSIVE" || res.Lock == "UNKNOWN" {
				invalid = true
			}
		case "EXCLUSIVE":
			// Always valid.
		default:
			invalid = true
		}
		if invalid {
			explicitFailure(res, "ALGO-02", fmt.Sprintf("LOCK=%s is not supported for this statement (predicted lock: %s); MySQL will reject the statement.", reqL, res.Lock))
		} else if res.Lock != "METADATA" {
			res.Lock = reqL
		}
	}
}

func explicitFailure(res *Result, rule, msg string) {
	res.Risk = model.RiskCritical
	res.Notes = append(res.Notes, msg)
	addRule(res, rule)
}

func unknownOp(opt parser.Option, note string) model.Operation {
	return model.Operation{
		Kind: opt.Kind, Algorithm: model.AlgorithmUnknown, Lock: "UNKNOWN",
		RuleID: "UNKNOWN-01", Risk: model.RiskMedium, Note: note,
	}
}

func maxRiskForAlgorithm(risk model.Risk, alg model.Algorithm) model.Risk {
	switch alg {
	case model.AlgorithmCopy:
		return model.MaxRisk(risk, model.RiskHigh)
	case model.AlgorithmInplace, model.AlgorithmUnknown:
		return model.MaxRisk(risk, model.RiskMedium)
	default:
		return risk
	}
}

func combineLocks(locks []string) string {
	rank := map[string]int{"METADATA": 0, "NONE": 1, "SHARED": 2, "EXCLUSIVE": 3, "UNKNOWN": 4}
	best := "METADATA"
	for _, l := range locks {
		if rank[l] > rank[best] {
			best = l
		}
	}
	return best
}

func addRule(res *Result, rule string) {
	if rule == "" {
		return
	}
	for _, r := range res.RuleIDs {
		if r == rule {
			return
		}
	}
	res.RuleIDs = append(res.RuleIDs, rule)
}

func columnName(opt parser.Option) string {
	if opt.Column != nil {
		return opt.Column.Name
	}
	return opt.OldColumn
}

func fkName(fk *model.ForeignKey) string {
	if fk == nil {
		return ""
	}
	return fk.Name
}

func fkTable(fk *model.ForeignKey) string {
	if fk == nil {
		return ""
	}
	return fk.ReferencedTable
}

func fkColumns(fk *model.ForeignKey) []string {
	if fk == nil {
		return nil
	}
	return fk.Columns
}

func fkRefColumns(fk *model.ForeignKey) []string {
	if fk == nil {
		return nil
	}
	return fk.ReferencedColumns
}
