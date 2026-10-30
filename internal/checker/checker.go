// Package checker evaluates blockers and metadata-dependent risk. It never
// executes SQL; it only interprets metadata already collected.
package checker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/eterny13/mig-est/internal/catalog"
	"github.com/eterny13/mig-est/internal/model"
)

const mb = 1024 * 1024

// Check runs all checks and updates statement risk levels in place. It returns
// findings; overall risk is the maximum of statement risk and non-info
// findings.
func Check(stmts []*model.Statement, snap *catalog.Snapshot, th model.Thresholds, diskFree *int64) []model.Finding {
	var findings []model.Finding
	seen := make(map[string]bool)
	add := func(f model.Finding) {
		key := fmt.Sprintf("%s|%s|%s|%d", f.ID, f.Table, f.Message, f.StatementIndex)
		if seen[key] {
			return
		}
		seen[key] = true
		findings = append(findings, f)
	}
	// Table metadata findings are reported once per table, regardless of how
	// many statements touch it. If any statement makes the finding more
	// severe, keep the highest severity.
	tableFindingPos := make(map[string]int)
	addTable := func(f model.Finding) {
		key := fmt.Sprintf("%s|%s|%s", f.ID, f.Table, f.Message)
		if pos, ok := tableFindingPos[key]; ok {
			if model.SeverityRank(f.Severity) > model.SeverityRank(findings[pos].Severity) {
				findings[pos].Severity = f.Severity
				findings[pos].StatementIndex = f.StatementIndex
				findings[pos].Detail = f.Detail
			}
			return
		}
		tableFindingPos[key] = len(findings)
		findings = append(findings, f)
	}

	uses := collectUses(stmts, snap)
	firstStmt := firstStatementIndex(stmts)

	// Per-table checks. Each statement is evaluated against its own table so a
	// heavy COPY later in a migration cannot raise the risk of an earlier,
	// unrelated INSTANT statement.
	for _, use := range uses {
		table := use.table
		if table == nil {
			continue
		}
		t := use.stmt.Index
		if !table.Exists {
			addTable(model.Finding{
				ID: "TARGET-01", Severity: model.SeverityMedium, Table: table.Name, StatementIndex: t,
				Message:    fmt.Sprintf("%s was not found in the database catalog; the statement may fail", table.Name),
				Suggestion: "Confirm the schema qualifier and migration order, or run the statement on staging first.",
			})
			bump(stmts, t, model.RiskMedium)
			continue
		}
		if table.Source == "script" {
			continue
		}
		if table.Engine != "" && !strings.EqualFold(table.Engine, "InnoDB") {
			addTable(model.Finding{
				ID: "ENGINE-01", Severity: model.SeverityHigh, Table: table.Name, StatementIndex: t,
				Message:    fmt.Sprintf("%s uses the %s engine; mig-est only classifies InnoDB online DDL", table.Name, table.Engine),
				Detail:     map[string]any{"engine": table.Engine},
				Suggestion: "Confirm that the migration is supported by this engine, or convert to InnoDB in a separately tested change.",
			})
			bump(stmts, t, model.RiskHigh)
		}

		if len(table.IncomingFKs) > 0 {
			sev := model.SeverityMedium
			if use.keyChange || use.kind == model.KindDropTable {
				sev = model.SeverityHigh
			}
			if use.kind == model.KindDropTable {
				sev = model.SeverityCritical
			}
			addTable(model.Finding{
				ID: "FK-01", Severity: sev, Table: table.Name, StatementIndex: t,
				Message:    fmt.Sprintf("%d foreign key(s) from other tables reference %s", len(table.IncomingFKs), table.Name),
				Detail:     map[string]any{"foreign_keys": fkNames(table.IncomingFKs)},
				Suggestion: "Review the referencing tables before changing column types, keys, or dropping this table.",
			})
			bump(stmts, t, riskFor(sev))
		}
		if len(table.OutgoingFKs) > 0 {
			sev := model.SeverityInfo
			if use.keyChange || use.fkChange {
				sev = model.SeverityMedium
			}
			addTable(model.Finding{
				ID: "FK-02", Severity: sev, Table: table.Name, StatementIndex: t,
				Message:    fmt.Sprintf("%s has %d outgoing foreign key(s)", table.Name, len(table.OutgoingFKs)),
				Detail:     map[string]any{"foreign_keys": fkNames(table.OutgoingFKs)},
				Suggestion: "Confirm validation scope and lock behavior when adding or dropping foreign keys.",
			})
			bump(stmts, t, riskFor(sev))
		}
		if len(table.Triggers) > 0 {
			sev := model.SeverityMedium
			if use.anyCopy {
				sev = model.SeverityHigh
			}
			names := make([]string, 0, len(table.Triggers))
			for _, trg := range table.Triggers {
				names = append(names, trg.Name)
			}
			addTable(model.Finding{
				ID: "TRG-01", Severity: sev, Table: table.Name, StatementIndex: t,
				Message:    fmt.Sprintf("%s has %d trigger(s); gh-ost cannot manage this table", table.Name, len(table.Triggers)),
				Detail:     map[string]any{"triggers": names},
				Suggestion: "Account for trigger side effects and choose expand/contract or a carefully tested native ALTER.",
			})
			bump(stmts, t, riskFor(sev))
		}
		if len(table.Columns) > 0 && !table.HasPrimaryKey() && use.kind != model.KindCreateTable {
			sev := model.SeverityMedium
			if use.anyCopy {
				sev = model.SeverityHigh
			}
			addTable(model.Finding{
				ID: "PK-01", Severity: sev, Table: table.Name, StatementIndex: t,
				Message:    table.Name + " has no primary key; gh-ost/pt-osc prerequisites are not met",
				Suggestion: "Prefer expand/contract, or add a suitable primary/unique key before using an online schema-change tool.",
			})
			bump(stmts, t, riskFor(sev))
		}
		if table.Partitioned {
			addTable(model.Finding{
				ID: "PART-01", Severity: model.SeverityMedium, Table: table.Name, StatementIndex: t,
				Message:    table.Name + " is partitioned; partition DDL support varies by operation and version",
				Suggestion: "Test the exact statement on staging and confirm the chosen algorithm.",
			})
			bump(stmts, t, model.RiskMedium)
		}
		if use.columnChange {
			if table.HasFullText() {
				add(model.Finding{
					ID: "FTS-01", Severity: model.SeverityMedium, Table: table.Name, StatementIndex: t,
					Message:    table.Name + " has a FULLTEXT index; column add/drop may not use INSTANT",
					Suggestion: "Re-check the INSTANT exclusions on the target version before relying on a fast path.",
				})
				bump(stmts, t, model.RiskMedium)
			}
			if strings.EqualFold(table.RowFormat, "COMPRESSED") {
				add(model.Finding{
					ID: "ROWFMT-01", Severity: model.SeverityMedium, Table: table.Name, StatementIndex: t,
					Message:    table.Name + " uses ROW_FORMAT=COMPRESSED; INSTANT column changes are not supported",
					Suggestion: "Plan for INPLACE/COPY cost or convert the row format in a separate change.",
				})
				bump(stmts, t, model.RiskMedium)
			}
		}
		if use.heavy {
			large := false
			switch {
			case th.LargeTableRows > 0 && table.Rows >= th.LargeTableRows:
				large = true
			case th.LargeTableSizeMB > 0 && table.SizeBytes() >= th.LargeTableSizeMB*mb:
				large = true
			}
			if large {
				add(model.Finding{
					ID: "SIZE-01", Severity: model.SeverityHigh, Table: table.Name, StatementIndex: t,
					Message: fmt.Sprintf("%s is large (about %d rows, %.1f MB); a COPY/rebuild can take a long time",
						table.Name, table.Rows, float64(table.SizeBytes())/mb),
					Detail: map[string]any{
						"rows":       table.Rows,
						"size_mb":    float64(table.SizeBytes()) / mb,
						"thresholds": th,
					},
					Suggestion: "Use an off-peak window and evaluate gh-ost/pt-osc only after checking their prerequisites.",
				})
				bump(stmts, t, model.RiskHigh)
			}
			if snap != nil && snap.OnlineAlterLogBytes > 0 && table.SizeBytes() > snap.OnlineAlterLogBytes {
				add(model.Finding{
					ID: "LOG-01", Severity: model.SeverityLow, Table: table.Name, StatementIndex: t,
					Message: fmt.Sprintf("online ALTER log limit is %.1f MB; sustained writes during the DDL may exceed it",
						float64(snap.OnlineAlterLogBytes)/mb),
					Detail:     map[string]any{"innodb_online_alter_log_max_size": snap.OnlineAlterLogBytes},
					Suggestion: "Run during lower write volume and monitor the online ALTER log; increase the limit only with operational approval.",
				})
			}
		}

		// Metadata lock blockers.
		if snap != nil {
			key := catalog.Key(table.Schema, table.Name)
			for _, trx := range snap.LongTransactions[key] {
				msg := fmt.Sprintf("transaction on connection %d has held metadata locks for %d seconds", trx.ConnectionID, trx.AgeSeconds)
				addTable(model.Finding{
					ID: "MDL-01", Severity: model.SeverityCritical, Table: table.Name, StatementIndex: t,
					Message: msg,
					Detail: map[string]any{
						"connection_id": trx.ConnectionID,
						"age_seconds":   trx.AgeSeconds,
						"state":         trx.State,
						"query":         trx.Query,
						"lock_type":     trx.LockType,
					},
					Suggestion: "Find the owning application/task, let it commit or roll back, and re-check before applying the migration. Do not KILL blindly.",
				})
				bump(stmts, t, model.RiskCritical)
			}
			for _, wait := range snap.MDLWaits[key] {
				addTable(model.Finding{
					ID: "MDL-02", Severity: model.SeverityCritical, Table: table.Name, StatementIndex: t,
					Message: fmt.Sprintf("metadata lock wait detected on connection %d (%s)", wait.ConnectionID, wait.LockType),
					Detail: map[string]any{
						"connection_id": wait.ConnectionID,
						"lock_type":     wait.LockType,
						"duration":      wait.Duration,
						"query":         wait.Query,
					},
					Suggestion: "Resolve the lock wait before applying the migration; otherwise the ALTER and all following queries queue behind it.",
				})
				bump(stmts, t, model.RiskCritical)
			}
		}
	}

	// Statement-specific checks.
	for _, stmt := range stmts {
		if stmt.Excluded {
			continue
		}
		if stmt.Kind == model.KindCreateTable {
			if !createTableHasPK(stmt, snap) {
				add(model.Finding{
					ID: "PK-02", Severity: model.SeverityMedium, Table: stmt.Table, StatementIndex: stmt.Index,
					Message:    stmt.Table + " is created without a primary key",
					Suggestion: "Add a primary key (or a suitable unique key) now; online schema-change tools may not work on this table later.",
				})
				stmt.Risk = model.MaxRisk(stmt.Risk, model.RiskMedium)
			}
		}
		if stmt.Unsupported {
			add(model.Finding{
				ID: "UNSUPPORTED-01", Severity: model.SeverityMedium, Table: stmt.Table, StatementIndex: stmt.Index,
				Message:    "statement is not analyzed in v0.1; it is not safe by default",
				Detail:     map[string]any{"reason": "stored programs, triggers, events, and DELIMITER blocks are out of scope"},
				Suggestion: "Review this statement manually before applying it.",
			})
		}
		if hasAnyRule(stmt.RuleIDs, "ALGO-01", "ALGO-04") {
			add(model.Finding{
				ID: "ALGO-01", Severity: model.SeverityCritical, Table: stmt.Table, StatementIndex: stmt.Index,
				Message:    "the requested ALGORITHM/LOCK combination is not supported; MySQL will reject this statement",
				Detail:     map[string]any{"requested_algorithm": stmt.RequestedAlgorithm, "requested_lock": stmt.RequestedLock},
				Suggestion: "Remove or correct ALGORITHM/LOCK, then re-run mig-est to confirm the expected algorithm.",
			})
			stmt.Risk = model.MaxRisk(stmt.Risk, model.RiskCritical)
		}
		if hasAnyRule(stmt.RuleIDs, "ALGO-02") && stmt.PredictedAlgorithm == model.AlgorithmUnknown {
			add(model.Finding{
				ID: "ALGO-02", Severity: model.SeverityHigh, Table: stmt.Table, StatementIndex: stmt.Index,
				Message:    "ALGORITHM/LOCK was requested but the predicted algorithm is UNKNOWN; MySQL may reject or ignore the expectation",
				Suggestion: "Confirm the algorithm on staging, then keep the explicit ALGORITHM/LOCK clause.",
			})
		}

		if stmt.DiskRequiredMB != nil && *stmt.DiskRequiredMB > 0 {
			required := int64(*stmt.DiskRequiredMB * mb)
			if diskFree == nil {
				sev := model.SeverityMedium
				if stmt.PredictedAlgorithm == model.AlgorithmCopy {
					sev = model.SeverityMedium
				}
				add(model.Finding{
					ID: "DISK-01", Severity: sev, Table: stmt.Table, StatementIndex: stmt.Index,
					Message:    fmt.Sprintf("disk requirement is %.1f MB but --disk-free was not provided; disk safety is unknown", *stmt.DiskRequiredMB),
					Detail:     map[string]any{"required_mb": *stmt.DiskRequiredMB, "disk_free_mb": nil},
					Suggestion: "Pass --disk-free (for example --disk-free 50GB) or verify free space on every replica before applying.",
				})
			} else if required > *diskFree {
				add(model.Finding{
					ID: "DISK-01", Severity: model.SeverityCritical, Table: stmt.Table, StatementIndex: stmt.Index,
					Message:    fmt.Sprintf("insufficient disk: need about %.1f MB, only %.1f MB free", float64(required)/mb, float64(*diskFree)/mb),
					Detail:     map[string]any{"required_mb": float64(required) / mb, "disk_free_mb": float64(*diskFree) / mb},
					Suggestion: "Free space, reduce the change size, or use an online schema-change tool after verifying its disk requirements.",
				})
				stmt.Risk = model.MaxRisk(stmt.Risk, model.RiskCritical)
			} else {
				add(model.Finding{
					ID: "DISK-02", Severity: model.SeverityInfo, Table: stmt.Table, StatementIndex: stmt.Index,
					Message: fmt.Sprintf("disk check passed: need about %.1f MB, %.1f MB free", *stmt.DiskRequiredMB, float64(*diskFree)/mb),
				})
			}
		} else if stmt.Kind != model.KindCreateTable && stmt.Kind != model.KindRenameTable && stmt.PredictedAlgorithm == model.AlgorithmCopy {
			add(model.Finding{
				ID: "DISK-01", Severity: model.SeverityMedium, Table: stmt.Table, StatementIndex: stmt.Index,
				Message:    "disk requirement could not be estimated because table size is unknown",
				Suggestion: "Run online or pass --table-size so mig-est can check the COPY disk requirement.",
			})
		}
	}

	// Global checks.
	if snap != nil {
		for _, idle := range snap.IdleTransactions {
			add(model.Finding{
				ID: "MDL-03", Severity: model.SeverityHigh, Table: "", StatementIndex: firstStmt,
				Message: fmt.Sprintf("connection %d has an idle transaction (idle %ds, age %ds)", idle.ConnectionID, idle.IdleSeconds, idle.AgeSeconds),
				Detail: map[string]any{
					"connection_id": idle.ConnectionID,
					"age_seconds":   idle.AgeSeconds,
					"idle_seconds":  idle.IdleSeconds,
					"user":          idle.User,
					"database":      idle.Database,
				},
				Suggestion: "Look for a missing commit/rollback in the application. Do not terminate the session without confirming ownership.",
			})
			if firstStmt > 0 {
				stmts[firstStmt-1].Risk = model.MaxRisk(stmts[firstStmt-1].Risk, model.RiskHigh)
			}
		}
		if rep := snap.Replication; rep != nil && rep.IsReplica {
			switch {
			case !rep.IORunning || !rep.SQLRunning:
				add(model.Finding{
					ID: "REP-02", Severity: model.SeverityCritical, Table: "", StatementIndex: firstStmt,
					Message:    "replica SQL or I/O thread is not running",
					Detail:     map[string]any{"io_running": rep.IORunning, "sql_running": rep.SQLRunning},
					Suggestion: "Resolve the replication failure before applying any DDL, especially a long COPY/INPLACE operation.",
				})
			case rep.SecondsBehind != nil && *rep.SecondsBehind >= int64(th.ReplicaLagSeconds):
				add(model.Finding{
					ID: "REP-01", Severity: model.SeverityHigh, Table: "", StatementIndex: firstStmt,
					Message:    fmt.Sprintf("replica lag is %d seconds (threshold %d)", *rep.SecondsBehind, th.ReplicaLagSeconds),
					Detail:     map[string]any{"seconds_behind": *rep.SecondsBehind, "threshold_seconds": th.ReplicaLagSeconds},
					Suggestion: "Wait for lag to recover; online DDL usually increases replication lag.",
				})
			case rep.SecondsBehind != nil:
				add(model.Finding{
					ID: "REP-01", Severity: model.SeverityInfo, Table: "", StatementIndex: firstStmt,
					Message: fmt.Sprintf("replica lag is %d seconds, within the %d second threshold", *rep.SecondsBehind, th.ReplicaLagSeconds),
				})
			default:
				add(model.Finding{
					ID: "REP-01", Severity: model.SeverityInfo, Table: "", StatementIndex: firstStmt,
					Message: "replica threads are running but does not report a lag value",
				})
			}
		}
		if snap.PerformanceSchemaOK && !hasSeverity(findings, "MDL-01") && !hasSeverity(findings, "MDL-02") {
			add(model.Finding{
				ID: "MDL-00", Severity: model.SeverityInfo, Table: "", StatementIndex: firstStmt,
				Message: "no long-running transaction or pending metadata lock was detected on the target tables",
			})
		}
	}

	SortFindings(findings)
	return findings
}

type tableUse struct {
	table        *catalog.TableInfo
	stmt         *model.Statement
	kind         model.StatementKind
	anyCopy      bool
	heavy        bool
	columnChange bool
	keyChange    bool
	fkChange     bool
}

func collectUses(stmts []*model.Statement, snap *catalog.Snapshot) []*tableUse {
	var out []*tableUse
	for _, stmt := range stmts {
		if stmt.Excluded || stmt.Table == "" {
			continue
		}
		var table *catalog.TableInfo
		if snap != nil {
			table = snap.Lookup(stmt.Schema, stmt.Table)
		}
		use := &tableUse{table: table, stmt: stmt, kind: stmt.Kind}
		if stmt.PredictedAlgorithm == model.AlgorithmCopy {
			use.anyCopy = true
			use.heavy = true
		}
		if stmt.PredictedAlgorithm == model.AlgorithmInplace {
			switch stmt.Operation {
			case model.OpAddIndex, model.OpAddPrimaryKey, model.OpModifyColumn, model.OpChangeColumn, model.OpAddColumn, model.OpDropColumn, model.OpAddForeignKey, model.OpAddCheck, model.OpForce, model.OpOrderBy:
				use.heavy = true
			}
		}
		for _, op := range stmt.Operations {
			switch op.Kind {
			case model.OpAddColumn, model.OpDropColumn:
				use.columnChange = true
			case model.OpModifyColumn, model.OpChangeColumn:
				use.columnChange = true
				use.keyChange = true
			case model.OpAddPrimaryKey, model.OpDropPrimaryKey:
				use.keyChange = true
			case model.OpAddForeignKey, model.OpDropForeignKey:
				use.keyChange = true
				use.fkChange = true
			}
		}
		out = append(out, use)
	}
	return out
}

func firstStatementIndex(stmts []*model.Statement) int {
	for _, s := range stmts {
		if !s.Excluded {
			return s.Index
		}
	}
	return 0
}

func createTableHasPK(stmt *model.Statement, snap *catalog.Snapshot) bool {
	if snap == nil {
		return false
	}
	if t := snap.Lookup(stmt.Schema, stmt.Table); t != nil {
		if t.Source == "script" {
			return t.HasPrimaryKey()
		}
		if len(t.Columns) > 0 {
			return t.HasPrimaryKey()
		}
	}
	return false
}

func hasAnyRule(rules []string, ids ...string) bool {
	for _, r := range rules {
		for _, id := range ids {
			if r == id {
				return true
			}
		}
	}
	return false
}

func hasSeverity(findings []model.Finding, ids ...string) bool {
	for _, f := range findings {
		for _, id := range ids {
			if f.ID == id {
				return true
			}
		}
	}
	return false
}

func bump(stmts []*model.Statement, index int, risk model.Risk) {
	if index <= 0 || index > len(stmts) {
		return
	}
	stmts[index-1].Risk = model.MaxRisk(stmts[index-1].Risk, risk)
}

func riskFor(sev model.Severity) model.Risk {
	return model.RiskFromSeverity(sev)
}

func fkNames(fks []catalog.ForeignKey) []string {
	out := make([]string, 0, len(fks))
	for _, fk := range fks {
		name := fk.Name
		if fk.ReferencedTable != "" {
			name += " -> " + fk.ReferencedTable
		}
		out = append(out, name)
	}
	return out
}

// SortFindings orders findings by severity, then table and ID.
func SortFindings(findings []model.Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		ri, rj := model.SeverityRank(findings[i].Severity), model.SeverityRank(findings[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if findings[i].Table != findings[j].Table {
			return findings[i].Table < findings[j].Table
		}
		return findings[i].ID < findings[j].ID
	})
}
