package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/eterny13/mig-est/internal/classifier"
	"github.com/eterny13/mig-est/internal/history"
	"github.com/eterny13/mig-est/internal/model"
	"github.com/eterny13/mig-est/internal/mysqlver"
	"github.com/eterny13/mig-est/internal/parser"
)

type recordOptions struct {
	files      stringList
	actual     string
	table      string
	operation  string
	algorithm  string
	host       string
	database   string
	rows       string
	size       string
	dsn        string
	configPath string
}

func runRecord(args []string, stdout, stderr io.Writer) int {
	var opts recordOptions
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	fs.Var(&opts.files, "file", "migration SQL file used for inference (repeatable)")
	fs.StringVar(&opts.actual, "actual", "", "actual duration, e.g. 8m12s or 492")
	fs.StringVar(&opts.table, "table", "", "table name (inferred when exactly one table is in the file)")
	fs.StringVar(&opts.operation, "operation", "", "operation name (optional)")
	fs.StringVar(&opts.algorithm, "algorithm", "", "INSTANT, INPLACE, COPY, or UNKNOWN (optional)")
	fs.StringVar(&opts.host, "host", "", "database host (never include credentials)")
	fs.StringVar(&opts.database, "database", "", "database name")
	fs.StringVar(&opts.rows, "rows", "", "table row count")
	fs.StringVar(&opts.size, "size", "", "table size, e.g. 8200MB")
	fs.StringVar(&opts.dsn, "dsn", "", "DSN used only to read host/database (prefer MIGEST_DSN)")
	fs.StringVar(&opts.configPath, "config", "", "config file (default ./.mig-est.yaml if present)")
	_, err := parseFlagSet(fs, args, func(file string) {
		opts.files = append(opts.files, file)
	})
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			recordUsage(stdout)
			return 0
		}
		return fail(stderr, "record: %v", err)
	}
	if strings.TrimSpace(opts.actual) == "" {
		return fail(stderr, "record: --actual is required")
	}
	actual, err := parseActual(opts.actual)
	if err != nil {
		return fail(stderr, "record: --actual: %v", err)
	}

	cfg, err := loadConfig(opts.configPath)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	if !cfg.HistoryEnabled() {
		return fail(stderr, "record: history is disabled in %s", cfg.History.Path)
	}

	var parsedStmts []*parser.Statement
	if len(opts.files) > 0 {
		inputs, err := gatherInputs(opts.files, nil, "", false, os.Stdin)
		if err != nil {
			return fail(stderr, "record: %v", err)
		}
		for _, input := range inputs {
			stmts, err := parser.ParseSQL(input.Name, string(input.Data))
			if err != nil {
				return fail(stderr, "%v", err)
			}
			parsedStmts = append(parsedStmts, stmts...)
		}
	}

	table := strings.TrimSpace(opts.table)
	tableNames := inferredTables(parsedStmts)
	if table == "" {
		switch len(tableNames) {
		case 1:
			table = tableNames[0]
		case 0:
			return fail(stderr, "record: --table is required when it cannot be inferred from --file")
		default:
			return fail(stderr, "record: multiple tables found (%s); pass --table", strings.Join(tableNames, ", "))
		}
	}

	algorithm := strings.ToUpper(strings.TrimSpace(opts.algorithm))
	switch algorithm {
	case "":
		algorithm = inferAlgorithm(parsedStmts, table, cfg.MySQL.Version)
	case "INSTANT", "INPLACE", "COPY", "UNKNOWN":
	default:
		return fail(stderr, "record: --algorithm must be INSTANT, INPLACE, COPY, or UNKNOWN")
	}
	operation := strings.ToUpper(strings.TrimSpace(opts.operation))
	if operation == "" {
		operation = inferOperation(parsedStmts, table, cfg.MySQL.Version)
	}

	var rows int64
	if strings.TrimSpace(opts.rows) != "" {
		rows, err = parseRows(opts.rows)
		if err != nil {
			return fail(stderr, "record: --rows: %v", err)
		}
	}
	var sizeBytes int64
	if strings.TrimSpace(opts.size) != "" {
		sizeBytes, err = parseByteSize(opts.size)
		if err != nil {
			return fail(stderr, "record: --size: %v", err)
		}
	}

	host := strings.TrimSpace(opts.host)
	database := strings.TrimSpace(opts.database)
	dsn := strings.TrimSpace(opts.dsn)
	if dsn == "" {
		dsn = strings.TrimSpace(os.Getenv("MIGEST_DSN"))
	}
	if dsn != "" && (host == "" || database == "") {
		if cfg, err := mysql.ParseDSN(dsn); err == nil {
			if host == "" {
				host = cfg.Addr
			}
			if database == "" {
				database = cfg.DBName
			}
		}
	}

	rec := history.Record{
		At:            time.Now().Format(time.RFC3339),
		Table:         table,
		Operation:     operation,
		Algorithm:     algorithm,
		Rows:          rows,
		SizeMB:        float64(sizeBytes) / (1024 * 1024),
		ActualSeconds: actual.Seconds(),
		Host:          host,
		Database:      database,
		MySQLVersion:  cfg.MySQL.Version,
	}
	if err := history.Append(cfg.History.Path, rec); err != nil {
		return fail(stderr, "record: %v", err)
	}
	fmt.Fprintf(stdout, "Recorded %.1fs for %s (%s) in %s\n", rec.ActualSeconds, table, algorithm, cfg.History.Path)
	if rec.SizeMB <= 0 {
		fmt.Fprintln(stderr, "mig-est: warning: no --size recorded; this entry cannot be used for speed calibration until a size is provided.")
	}
	return 0
}

func recordUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  mig-est record [flags] [migration.sql...]
  mig-est record --file migration.sql --actual 8m12s --table orders [flags]

Flags:
  --file PATH        migration file used to infer table/operation/algorithm
  --actual DURATION  measured duration (8m12s or seconds)
  --table NAME       table name
  --operation NAME   operation override
  --algorithm NAME   INSTANT, INPLACE, COPY, or UNKNOWN
  --rows N           table row count
  --size SIZE        table size, e.g. 8200MB
  --host HOST        database host (no credentials)
  --database NAME    database name
  --dsn DSN          read only host/database from the DSN; never stored
  --config PATH      config file

Records are appended to history.path as JSON Lines. SQL text and credentials
are never stored.
`)
}

func parseActual(raw string) (time.Duration, error) {
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return 0, fmt.Errorf("duration must be positive")
		}
		return d, nil
	}
	sec, err := strconv.ParseFloat(raw, 64)
	if err != nil || sec <= 0 {
		return 0, fmt.Errorf("expected a duration like 8m12s or a positive number of seconds")
	}
	return time.Duration(sec * float64(time.Second)), nil
}

func inferredTables(stmts []*parser.Statement) []string {
	var out []string
	seen := map[string]bool{}
	for _, stmt := range stmts {
		names := stmt.Tables
		if len(names) == 0 && stmt.Table != "" {
			names = []string{stmt.Table}
		}
		if stmt.Kind == model.KindCreateTable {
			continue
		}
		for _, name := range names {
			if name == "" || seen[strings.ToLower(name)] {
				continue
			}
			seen[strings.ToLower(name)] = true
			out = append(out, name)
		}
	}
	return out
}

func inferAlgorithm(stmts []*parser.Statement, table, versionRaw string) string {
	ver := recordVersion(versionRaw)
	cl := classifier.New(ver)
	var algos []model.Algorithm
	for _, stmt := range stmts {
		if !strings.EqualFold(stmt.Table, table) {
			continue
		}
		res := cl.Classify(stmt, nil)
		algos = append(algos, res.Algorithm)
	}
	if len(algos) == 0 {
		return "UNKNOWN"
	}
	return string(model.CombineAlgorithms(algos))
}

func inferOperation(stmts []*parser.Statement, table, versionRaw string) string {
	ver := recordVersion(versionRaw)
	cl := classifier.New(ver)
	op := model.Operation{Kind: model.OpOther}
	best := model.AlgorithmRank(model.AlgorithmInstant) - 1
	for _, stmt := range stmts {
		if !strings.EqualFold(stmt.Table, table) {
			continue
		}
		res := cl.Classify(stmt, nil)
		for _, candidate := range res.Operations {
			if model.AlgorithmRank(candidate.Algorithm) > best {
				best = model.AlgorithmRank(candidate.Algorithm)
				op = candidate
			}
		}
	}
	return string(op.Kind)
}

func recordVersion(raw string) mysqlver.Version {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "8.0.28"
	}
	v, err := mysqlver.Parse(raw)
	if err != nil || !v.IsSupported() {
		v, _ = mysqlver.Parse("8.0.28")
	}
	return v
}
