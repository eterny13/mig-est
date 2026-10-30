package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/eterny13/mig-est/internal/catalog"
	"github.com/eterny13/mig-est/internal/checker"
	"github.com/eterny13/mig-est/internal/classifier"
	"github.com/eterny13/mig-est/internal/config"
	"github.com/eterny13/mig-est/internal/estimator"
	"github.com/eterny13/mig-est/internal/history"
	"github.com/eterny13/mig-est/internal/model"
	"github.com/eterny13/mig-est/internal/mysqlver"
	"github.com/eterny13/mig-est/internal/parser"
	"github.com/eterny13/mig-est/internal/report"
	"github.com/eterny13/mig-est/internal/suggester"
	"github.com/eterny13/mig-est/internal/version"
)

type checkOptions struct {
	files      stringList
	dirs       stringList
	pattern    string
	stdin      bool
	dsn        string
	offline    bool
	tableSizes stringList
	tableRows  stringList
	diskFree   string
	format     string
	output     string
	failOn     string
	configPath string
	timeout    time.Duration
	noColor    bool
	quiet      bool
}

func runCheck(args []string, stdout, stderr io.Writer) int {
	var opts checkOptions
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.Var(&opts.files, "file", "migration SQL file (repeatable; globs allowed)")
	fs.Var(&opts.dirs, "dir", "directory of SQL files (repeatable)")
	fs.StringVar(&opts.pattern, "pattern", "*.sql", "glob pattern used with --dir")
	fs.BoolVar(&opts.stdin, "stdin", false, "read SQL from standard input")
	fs.StringVar(&opts.dsn, "dsn", "", "MySQL DSN (prefer MIGEST_DSN)")
	fs.BoolVar(&opts.offline, "offline", false, "do not connect; use --table-size/--table-rows")
	fs.Var(&opts.tableSizes, "table-size", "offline table size, e.g. orders=8200MB (repeatable)")
	fs.Var(&opts.tableRows, "table-rows", "offline row count, e.g. orders=12400000 (repeatable)")
	fs.StringVar(&opts.diskFree, "disk-free", "", "free disk space on the target host, e.g. 50GB")
	fs.StringVar(&opts.format, "format", "", "output format: text, markdown, json")
	fs.StringVar(&opts.output, "output", "", "write the report to this file")
	fs.StringVar(&opts.failOn, "fail-on", "", "exit 1 at this risk or above: low, medium, high, critical, never")
	fs.StringVar(&opts.configPath, "config", "", "config file (default ./.mig-est.yaml if present)")
	fs.DurationVar(&opts.timeout, "timeout", 10*time.Second, "timeout for each database query")
	fs.BoolVar(&opts.noColor, "no-color", false, "disable ANSI colors")
	fs.BoolVar(&opts.quiet, "quiet", false, "print a one-line summary (text format)")
	positional, err := parseFlagSet(fs, args, func(file string) {
		opts.files = append(opts.files, file)
	})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			checkUsage(stdout)
			return 0
		}
		return fail(stderr, "check: %v", err)
	}
	if len(positional) > 0 {
		if opts.stdin {
			return fail(stderr, "check: positional arguments cannot be combined with --stdin")
		}
		if len(opts.dirs) > 0 {
			return fail(stderr, "check: positional arguments cannot be combined with --dir")
		}
	}
	if opts.timeout <= 0 {
		return fail(stderr, "check: --timeout must be positive")
	}

	cfg, err := loadConfig(opts.configPath)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if opts.failOn != "" {
		cfg.FailOn = strings.ToLower(strings.TrimSpace(opts.failOn))
	}
	if opts.format != "" {
		cfg.Output.Format = strings.ToLower(strings.TrimSpace(opts.format))
	}
	if err := cfg.Validate(); err != nil {
		return fail(stderr, "%v", err)
	}

	dsn := strings.TrimSpace(opts.dsn)
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("MIGEST_DSN"))
	}
	offline := opts.offline
	autoOffline := false
	if !offline && dsn == "" {
		offline = true
		autoOffline = true
	}

	tableInputs, err := parseTableInputs(opts.tableSizes, opts.tableRows)
	if err != nil {
		return fail(stderr, "check: %v", err)
	}
	if opts.diskFree != "" && !offline {
		// The flag is still useful online; it describes the host, not the
		// database, so do not reject it.
	}
	var diskFree *int64
	if strings.TrimSpace(opts.diskFree) != "" {
		n, err := parseByteSize(opts.diskFree)
		if err != nil {
			return fail(stderr, "check: --disk-free: %v", err)
		}
		diskFree = &n
	}

	inputs, err := gatherInputs(opts.files, opts.dirs, opts.pattern, opts.stdin, os.Stdin)
	if err != nil {
		return fail(stderr, "check: %v", err)
	}
	var parsedStmts []*parser.Statement
	for _, input := range inputs {
		stmts, err := parser.ParseSQL(input.Name, string(input.Data))
		if err != nil {
			return fail(stderr, "%v", err)
		}
		parsedStmts = append(parsedStmts, stmts...)
	}
	if len(parsedStmts) == 0 {
		return fail(stderr, "check: no SQL statements found in the input")
	}

	ctx := context.Background()
	var stop context.CancelFunc = func() {}
	if !offline {
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt)
	}
	defer stop()

	var (
		versionRaw string
		ver        mysqlver.Version
		snap       *catalog.Snapshot
		client     *catalog.Client
	)
	if offline {
		versionRaw = strings.TrimSpace(cfg.MySQL.Version)
		if versionRaw == "" {
			versionRaw = "8.0.28"
		}
		ver, err = mysqlver.Parse(versionRaw)
		if err != nil {
			return fail(stderr, "check: mysql.version: %v", err)
		}
		if !ver.IsSupported() {
			return fail(stderr, "check: mysql.version %q is not supported; v0.1 supports MySQL 8.0 and 8.4", versionRaw)
		}
		snap = buildOfflineSnapshot(tableInputs, versionRaw)
		snap.AddUnknown("offline mode: no database connection was made; metadata locks, replication, triggers, and index metadata were not checked.")
		if autoOffline {
			snap.AddUnknown("no DSN was provided (set MIGEST_DSN or use --dsn); running in offline mode.")
		}
		if opts.offline && dsn != "" {
			snap.AddUnknown("offline mode was requested; the DSN was intentionally ignored.")
		}
	} else {
		client, err = catalog.Open(ctx, dsn, opts.timeout)
		if err != nil {
			return fail(stderr, "check: %v", err)
		}
		defer client.Close()
		versionRaw, err = client.Version(ctx)
		if err != nil {
			return fail(stderr, "check: %v", err)
		}
		ver, err = mysqlver.Parse(versionRaw)
		if err != nil {
			return fail(stderr, "check: %v", err)
		}
		if !ver.IsSupported() {
			return fail(stderr, "check: MySQL version %q is not supported; v0.1 supports 8.0.x and 8.4.x (InnoDB)", versionRaw)
		}
		refs := collectTableRefs(parsedStmts)
		snap = client.Collect(ctx, versionRaw, refs, catalog.CollectOptions{
			LongTransactionMinutes: cfg.Thresholds.LongTransactionMin,
			ReplicaLagSeconds:      cfg.Thresholds.ReplicaLagSeconds,
		})
		if len(tableInputs) > 0 {
			snap.AddUnknown("--table-size/--table-rows are intended for offline mode and were ignored while connected.")
		}
	}
	if !ver.PatchKnown {
		snap.AddUnknown(fmt.Sprintf("MySQL version %q has no patch level; feature gates assume the latest patch in that series.", versionRaw))
	}
	if !offline {
		snap.AddUnknown("note: online row counts come from information_schema.TABLE_ROWS and are estimates, not exact COUNT(*).")
	}

	addScriptTables(snap, parsedStmts)

	var records []history.Record
	if cfg.HistoryEnabled() {
		records, err = history.Load(cfg.History.Path)
		if err != nil {
			snap.AddUnknown("history: " + err.Error())
		}
	}

	host, database := "", ""
	if client != nil {
		host = client.HostMasked()
		database = client.Database()
	}
	statements := analyze(parsedStmts, snap, ver, cfg, records, host, database)
	stmtPtrs := make([]*model.Statement, len(statements))
	for i := range statements {
		stmtPtrs[i] = &statements[i]
	}
	findings := checker.Check(stmtPtrs, snap, cfg.Thresholds, diskFree)

	if diskFree == nil {
		for _, s := range statements {
			if s.DiskRequiredMB != nil && *s.DiskRequiredMB > 0 {
				snap.AddUnknown("disk_free: --disk-free was not provided, so disk safety was not verified.")
				break
			}
		}
	}
	if offline {
		addOfflineUnknowns(snap, tableInputs, parsedStmts)
	}

	overall := overallRisk(statements, findings)
	exitCode := exitCodeFor(overall, cfg.FailOn)
	generatedAt := time.Now().Format(time.RFC3339)
	rep := model.Report{
		Tool:        "mig-est",
		Version:     version.String(),
		GeneratedAt: generatedAt,
		Mode:        modeName(offline),
		MySQL: model.MySQLInfo{
			Version:    versionRaw,
			Database:   database,
			HostMasked: host,
		},
		OverallRisk: overall,
		Statements:  statements,
		Blockers:    findings,
		Unknowns:    snap.Unknowns,
		Tables:      tableSummaries(statements, snap),
		Thresholds:  cfg.Thresholds,
		FailOn:      cfg.FailOn,
		ExitCode:    exitCode,
	}
	if cfg.HistoryEnabled() {
		cal := model.CalibrationInfo{Records: len(records), Source: strings.Trim(strings.Join([]string{host, database}, "/"), "/")}
		for _, s := range statements {
			if s.Estimate.Calibrated {
				cal.Applied = true
				cal.MedianMBps = s.Estimate.CalibrationMBps
				break
			}
		}
		rep.Calibration = &cal
	}

	rendered, err := render(rep, cfg.Output.Format, opts, stdout)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if err := writeOutput(opts.output, rendered, stdout); err != nil {
		return fail(stderr, "check: %v", err)
	}
	return exitCode
}

func checkUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  mig-est check [flags] [migration.sql...]
  mig-est check --file migration.sql [flags]
  mig-est check --dir ./migrations [flags]
  mig-est check --stdin [flags]

Input:
  --file PATH            SQL file; repeatable and glob-capable
  --dir PATH             directory of SQL files; repeatable
  --pattern GLOB         pattern for --dir (default *.sql)
  --stdin                read SQL from standard input

Database:
  --dsn DSN              MySQL DSN; prefer the MIGEST_DSN environment variable
  --offline              never connect; use --table-size and --table-rows
  --table-size T=SIZE    offline size, e.g. orders=8200MB; repeatable
  --table-rows T=ROWS    offline rows, e.g. orders=12400000; repeatable
  --disk-free SIZE       free space on the target host, e.g. 50GB
  --timeout DURATION     per-query timeout (default 10s)

Output and CI:
  --format FORMAT        text, markdown, or json (default text)
  --output PATH          write the report to PATH ("-" means stdout)
  --fail-on LEVEL        low, medium, high, critical, or never (default critical)
  --quiet                one-line summary (text format)
  --no-color             disable ANSI colors
  --config PATH          config file (default ./.mig-est.yaml if present)

Exit codes:
  0  diagnostics completed and risk is below --fail-on
  1  risk is at or above --fail-on (intentional CI gate)
  2  usage, parse, connection, or config error

mig-est never executes ALTER/CREATE/DROP/INSERT/UPDATE/DELETE.
`)
}

func loadConfig(path string) (config.Config, error) {
	if path != "" {
		return config.Load(path, true)
	}
	defaultPath := "./.mig-est.yaml"
	if st, err := os.Stat(defaultPath); err == nil && !st.IsDir() {
		return config.Load(defaultPath, true)
	}
	return config.Load("", false)
}

func modeName(offline bool) string {
	if offline {
		return "offline"
	}
	return "online"
}

func collectTableRefs(stmts []*parser.Statement) []catalog.TableRef {
	var out []catalog.TableRef
	seen := map[string]bool{}
	add := func(schema, name string) {
		if name == "" {
			return
		}
		key := strings.ToLower(schema + "." + name)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, catalog.TableRef{Schema: schema, Name: name})
	}
	for _, stmt := range stmts {
		switch stmt.Kind {
		case model.KindAlterTable, model.KindCreateIndex, model.KindDropIndex, model.KindCreateTable:
			add(stmt.Schema, stmt.Table)
		case model.KindDropTable, model.KindRenameTable:
			for _, t := range stmt.Tables {
				add(stmt.Schema, t)
			}
		}
	}
	return out
}

func buildOfflineSnapshot(inputs map[string]*tableInput, versionRaw string) *catalog.Snapshot {
	snap := &catalog.Snapshot{
		Version:     versionRaw,
		Tables:      make(map[string]*catalog.TableInfo),
		CollectedAt: time.Now(),
	}
	for _, in := range inputs {
		ti := &catalog.TableInfo{
			Schema:     in.Schema,
			Name:       in.Name,
			Exists:     true,
			Rows:       in.Rows,
			DataLength: in.Size,
			Source:     "offline-input",
		}
		snap.Tables[catalog.Key(in.Schema, in.Name)] = ti
	}
	return snap
}

func addScriptTables(snap *catalog.Snapshot, stmts []*parser.Statement) {
	if snap == nil {
		return
	}
	for _, stmt := range stmts {
		if stmt.Kind != model.KindCreateTable || stmt.CreateTable == nil {
			continue
		}
		schema := stmt.Schema
		if schema == "" {
			schema = snap.Database
		}
		key := catalog.Key(schema, stmt.Table)
		if existing := snap.Tables[key]; existing != nil {
			continue
		}
		ct := stmt.CreateTable
		ti := &catalog.TableInfo{
			Schema:           schema,
			Name:             stmt.Table,
			Exists:           true,
			Source:           "script",
			MetadataComplete: true,
			ColumnsKnown:     true,
			IndexesKnown:     true,
			TriggersKnown:    true,
			FKsKnown:         true,
		}
		for _, c := range ct.Columns {
			col := catalog.Column{Name: c.Name, TypeRaw: c.Type.String()}
			if c.Nullable != nil {
				col.Nullable = *c.Nullable
			}
			if c.HasDefault {
				v := c.Default
				col.Default = &v
			}
			ti.Columns = append(ti.Columns, col)
		}
		for _, idx := range ct.Indexes {
			ci := catalog.Index{Name: idx.Name, Type: "BTREE", Columns: idx.Columns}
			ci.Unique = idx.Kind == "PRIMARY" || idx.Kind == "UNIQUE"
			ti.Indexes = append(ti.Indexes, ci)
		}
		hasPK, hasUnique := false, false
		for _, idx := range ti.Indexes {
			if strings.EqualFold(idx.Name, "PRIMARY") {
				hasPK = true
			}
			if idx.Unique && !strings.EqualFold(idx.Name, "PRIMARY") {
				hasUnique = true
			}
		}
		if ct.HasPrimaryKey && !hasPK {
			ti.Indexes = append(ti.Indexes, catalog.Index{Name: "PRIMARY", Unique: true, Type: "BTREE"})
		}
		if ct.HasUniqueKey && !hasUnique {
			ti.Indexes = append(ti.Indexes, catalog.Index{Name: "unique_key", Unique: true, Type: "BTREE"})
		}
		for _, fk := range ct.ForeignKeys {
			ti.OutgoingFKs = append(ti.OutgoingFKs, catalog.ForeignKey{
				Name: fk.Name, Table: stmt.Table, Columns: fk.Columns,
				ReferencedTable: fk.ReferencedTable, ReferencedColumns: fk.ReferencedColumns,
			})
		}
		snap.Tables[key] = ti
	}
}

func analyze(pstmts []*parser.Statement, snap *catalog.Snapshot, ver mysqlver.Version, cfg config.Config, records []history.Record, host, database string) []model.Statement {
	cl := classifier.New(ver)
	est := estimator.New(cfg.Thresholds, records, host, database)
	out := make([]model.Statement, 0, len(pstmts))
	for i, p := range pstmts {
		var table *catalog.TableInfo
		if p.Table != "" {
			table = snap.Lookup(p.Schema, p.Table)
		}
		res := cl.Classify(p, table)
		primary := primaryOperation(res.Operations)
		ms := model.Statement{
			Index:              i + 1,
			File:               p.File,
			Line:               p.Line,
			SQLSummary:         p.Summary,
			RawSQL:             p.Raw,
			Kind:               p.Kind,
			Table:              p.Table,
			Tables:             p.Tables,
			Schema:             p.Schema,
			Operation:          primary.Kind,
			Operations:         res.Operations,
			PredictedAlgorithm: res.Algorithm,
			Lock:               res.Lock,
			Risk:               res.Risk,
			RuleIDs:            res.RuleIDs,
			Notes:              res.Notes,
			RequestedAlgorithm: p.Algorithm,
			RequestedLock:      p.Lock,
			Unsupported:        p.Unsupported,
			Excluded:           p.Kind == model.KindOther,
		}
		ms.Estimate, ms.DiskRequiredMB = est.Estimate(&ms, table)
		ms.Suggestions = suggester.Suggest(&ms, table)
		out = append(out, ms)
	}
	return out
}

func primaryOperation(ops []model.Operation) model.Operation {
	if len(ops) == 0 {
		return model.Operation{Kind: model.OpOther, Algorithm: model.AlgorithmUnknown, Lock: "UNKNOWN", RuleID: "UNKNOWN-01"}
	}
	best := ops[0]
	for _, op := range ops[1:] {
		if model.AlgorithmRank(op.Algorithm) > model.AlgorithmRank(best.Algorithm) {
			best = op
			continue
		}
		if op.Algorithm == best.Algorithm && model.SeverityRank(model.Severity(op.Risk)) > model.SeverityRank(model.Severity(best.Risk)) {
			best = op
		}
	}
	return best
}

func addOfflineUnknowns(snap *catalog.Snapshot, inputs map[string]*tableInput, stmts []*parser.Statement) {
	if snap == nil {
		return
	}
	refs := collectTableRefs(stmts)
	for _, ref := range refs {
		ti := snap.Lookup(ref.Schema, ref.Name)
		if ti == nil {
			snap.AddUnknown(fmt.Sprintf("table %s: no --table-size/--table-rows supplied; time and disk estimates are unavailable.", ref.Name))
			continue
		}
		if ti.SizeBytes() == 0 && ti.Rows == 0 {
			snap.AddUnknown(fmt.Sprintf("table %s: size and row count are unknown; time and disk estimates are unavailable.", ref.Name))
		}
	}
}

func overallRisk(stmts []model.Statement, findings []model.Finding) model.Risk {
	risk := model.RiskLow
	for _, s := range stmts {
		if s.Excluded {
			continue
		}
		risk = model.MaxRisk(risk, s.Risk)
	}
	for _, f := range findings {
		if f.Severity == model.SeverityInfo {
			continue
		}
		risk = model.MaxRisk(risk, model.RiskFromSeverity(f.Severity))
	}
	return risk
}

func exitCodeFor(overall model.Risk, failOn string) int {
	if strings.EqualFold(failOn, "never") {
		return 0
	}
	threshold := model.Risk(strings.ToLower(failOn))
	if model.RiskRank(overall) >= model.RiskRank(threshold) {
		return 1
	}
	return 0
}

func tableSummaries(stmts []model.Statement, snap *catalog.Snapshot) []model.TableSummary {
	var out []model.TableSummary
	seen := map[string]bool{}
	for _, s := range stmts {
		names := s.Tables
		if len(names) == 0 && s.Table != "" {
			names = []string{s.Table}
		}
		for _, name := range names {
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if seen[key] {
				continue
			}
			seen[key] = true
			t := snap.Lookup(s.Schema, name)
			if t == nil || !t.Exists {
				continue
			}
			out = append(out, model.TableSummary{
				Schema:     t.Schema,
				Name:       t.Name,
				Rows:       t.Rows,
				RowsApprox: t.RowsApprox,
				SizeBytes:  t.SizeBytes(),
				Engine:     t.Engine,
				RowFormat:  t.RowFormat,
				Collation:  t.Collation,
				Source:     t.Source,
			})
		}
	}
	return out
}

func render(rep model.Report, format string, opts checkOptions, stdout io.Writer) (string, error) {
	switch strings.ToLower(format) {
	case "", "text":
		return report.Text(rep, report.Options{
			Color:  colorEnabled(opts.noColor, opts.output, stdout),
			Quiet:  opts.quiet,
			FailOn: rep.FailOn,
		}), nil
	case "markdown", "md":
		return report.Markdown(rep), nil
	case "json":
		return report.JSON(rep)
	default:
		return "", fmt.Errorf("unsupported output format %q", format)
	}
}

func colorEnabled(noColor bool, output string, stdout io.Writer) bool {
	if noColor || os.Getenv("NO_COLOR") != "" {
		return false
	}
	if output != "" && output != "-" {
		return false
	}
	f, ok := stdout.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func writeOutput(path, content string, stdout io.Writer) error {
	if path == "" || path == "-" {
		_, err := io.WriteString(stdout, content)
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return fmt.Errorf("output directory %s does not exist", dir)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	return nil
}
