// Package suggester produces concrete, copy-pasteable alternatives. It never
// rewrites or executes SQL.
package suggester

import (
	"fmt"
	"strings"

	"github.com/eterny13/mig-est/internal/catalog"
	"github.com/eterny13/mig-est/internal/model"
)

// Suggest returns de-duplicated suggestions for a statement.
func Suggest(stmt *model.Statement, table *catalog.TableInfo) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, existing := range out {
			if existing == s {
				return
			}
		}
		out = append(out, s)
	}

	if stmt.Excluded {
		return nil
	}
	if stmt.Unsupported {
		add("Review this statement manually. mig-est does not analyze stored programs, triggers, events, or DELIMITER blocks in v0.1.")
		return out
	}
	if len(stmt.Operations) > 1 {
		add("Split this ALTER into separate statements: the whole statement inherits the most expensive operation. Put INSTANT operations together and run COPY operations separately.")
	}
	if stmt.RequestedAlgorithm == "" && stmt.RequestedLock == "" && stmt.Kind == model.KindAlterTable {
		switch stmt.PredictedAlgorithm {
		case model.AlgorithmInstant:
			add("Make the expectation explicit: append ALGORITHM=INSTANT, LOCK=NONE so MySQL fails instead of silently choosing a slower algorithm.")
		case model.AlgorithmInplace:
			add("Make the expectation explicit: append ALGORITHM=INPLACE, LOCK=NONE where supported, so an unexpected COPY fails immediately.")
		}
	}

	for _, op := range stmt.Operations {
		switch op.Kind {
		case model.OpAddColumn:
			if strings.Contains(op.Note, "NOT NULL without a default") {
				add(expandContractColumn(op.ColumnName))
			}
			if stmt.PredictedAlgorithm == model.AlgorithmInstant {
				add("Verify the INSTANT preconditions on the target version: no FULLTEXT index, ROW_FORMAT is not COMPRESSED, and the column position is supported.")
			}
		case model.OpDropColumn:
			add("Dropping a column is irreversible. Take a backup and prefer expand/contract: stop writing the column, deploy the application, then drop it after an observation period.")
		case model.OpModifyColumn, model.OpChangeColumn:
			if stmt.PredictedAlgorithm == model.AlgorithmCopy {
				add("A COPY type change rewrites the whole table. First check whether a compatible widening (for example VARCHAR(50) to VARCHAR(100)) is enough.")
				add("Otherwise use expand/contract: add a new column, backfill in batches, switch the application, then remove the old column.")
				if table == nil {
					add(ghostCaveat(nil))
				}
			} else if stmt.PredictedAlgorithm == model.AlgorithmUnknown {
				add("The type pair is not in the compatibility table. Test the exact ALTER on a staging copy and inspect the chosen algorithm with EXPLAIN or the performance_schema DDL log.")
			}
			if strings.Contains(strings.ToUpper(op.ColumnType), "ENUM") {
				add("ENUM/SET value removal or reordering changes the meaning of existing stored values. Add new values at the end and keep old values until a backfill is complete.")
			}
		case model.OpAddIndex, model.OpAddPrimaryKey:
			if op.Kind == model.OpAddPrimaryKey {
				cols := quoteColumns(op.IndexColumns)
				add(fmt.Sprintf("Check for duplicate or NULL key values before ADD PRIMARY KEY, for example: SELECT %s, COUNT(*) FROM `%s` GROUP BY %s HAVING COUNT(*) > 1 LIMIT 100;", cols, stmt.Table, cols))
				add("ADD PRIMARY KEY is a COPY operation. Confirm that a primary key is really required and consider a UNIQUE index if the goal is only uniqueness.")
			} else {
				lock := "NONE"
				if strings.EqualFold(op.IndexKind, "FULLTEXT") || strings.EqualFold(op.IndexKind, "SPATIAL") {
					lock = "SHARED"
				}
				add(fmt.Sprintf("Use ALGORITHM=INPLACE, LOCK=%s to avoid an unexpected COPY; schedule large index builds off-peak.", lock))
				if table != nil {
					if existing := table.FindIndexByColumns(op.IndexColumns); existing != nil {
						add(fmt.Sprintf("A similar index already exists: %s. Confirm that the new index is not redundant.", existing.Name))
					}
				}
			}
		case model.OpAddForeignKey:
			child := stmt.Table
			parent := op.ReferencedTable
			if child != "" && parent != "" && len(op.IndexColumns) == 0 && len(op.ReferencedColumns) == 0 {
				add("Validate orphan rows before adding the foreign key; MySQL must scan the full table and has no NOT VALID option.")
			} else {
				add(orphanCheck(child, op.IndexColumns, parent, op.ReferencedColumns))
			}
		case model.OpAddCheck:
			add("Validate existing rows before adding the CHECK constraint; the validation scan cannot be skipped in MySQL.")
		case model.OpConvertCharset:
			add("Character-set conversion rewrites every row and can be irreversible. Test a full copy on staging and take a backup first.")
			if table == nil {
				add(ghostCaveat(nil))
			}
		case model.OpSetEngine, model.OpSetRowFormat, model.OpForce, model.OpOrderBy:
			add("This operation rebuilds the table. Schedule an off-peak window, verify free disk, and consider gh-ost/pt-osc if the table meets their constraints.")
		case model.OpPartition:
			add("Partition operations are conservatively classified as UNKNOWN. Test the exact statement on a staging copy and confirm the algorithm on the target MySQL version.")
		case model.OpDropPrimaryKey:
			add("Dropping a primary key changes InnoDB row organization and usually copies the table. Confirm that no replication or online-schema-change workflow depends on it.")
		}
	}

	if stmt.PredictedAlgorithm == model.AlgorithmCopy && table != nil {
		var reasons []string
		if !table.HasPrimaryKey() {
			reasons = append(reasons, "the table has no primary key")
		}
		if len(table.Triggers) > 0 {
			reasons = append(reasons, "the table has triggers")
		}
		if len(table.IncomingFKs) > 0 {
			reasons = append(reasons, "other tables reference it with foreign keys")
		}
		if len(reasons) > 0 {
			add("gh-ost/pt-osc may not work for this table because " + strings.Join(reasons, ", ") + ". Prefer expand/contract or fix the prerequisite first.")
		}
	}
	if stmt.Kind == model.KindDropTable {
		add("Take a backup and verify that no application or foreign key still references this table before dropping it.")
	}
	return out
}

func expandContractColumn(col string) string {
	if col == "" {
		col = "<column>"
	}
	return fmt.Sprintf("Use expand/contract for `%s`: (1) add it as NULL, (2) backfill in batches, (3) add the default and make it NOT NULL.", col)
}

func orphanCheck(child string, childCols []string, parent string, parentCols []string) string {
	if child == "" || parent == "" || len(childCols) == 0 || len(parentCols) == 0 || len(childCols) != len(parentCols) {
		return "Validate orphan rows before adding the foreign key; MySQL has no NOT VALID option."
	}
	var conds []string
	for i := range childCols {
		conds = append(conds, fmt.Sprintf("c.%s = p.%s", quoteIdent(childCols[i]), quoteIdent(parentCols[i])))
	}
	return fmt.Sprintf("Check for orphan rows first: SELECT COUNT(*) FROM %s c LEFT JOIN %s p ON %s WHERE %s IS NULL;",
		quoteIdent(child), quoteIdent(parent), strings.Join(conds, " AND "), "p."+quoteIdent(parentCols[0]))
}

func ghostCaveat(table *catalog.TableInfo) string {
	if table == nil {
		return "If a native ALTER is too risky, evaluate gh-ost/pt-osc on staging; they require a primary or unique key, no triggers, and compatible foreign keys/replication."
	}
	if !table.HasPrimaryKey() {
		return "gh-ost/pt-osc may not work because the table has no primary key. Prefer expand/contract or add a suitable unique key first."
	}
	if len(table.Triggers) > 0 {
		return "gh-ost cannot manage tables with triggers. Remove or account for the triggers before considering it."
	}
	return "If a native ALTER is too risky, evaluate gh-ost/pt-osc on staging; confirm primary/unique key, trigger, foreign key, and replication prerequisites."
}

func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

func quoteColumns(cols []string) string {
	if len(cols) == 0 {
		return "<key columns>"
	}
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, quoteIdent(c))
	}
	return strings.Join(out, ", ")
}
