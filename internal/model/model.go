// Package model contains the data types shared by the parser, classifier,
// catalog, estimator, checker, and report packages.
package model

import (
	"strings"
)

// Algorithm is the execution algorithm MySQL is expected to choose.
type Algorithm string

const (
	AlgorithmInstant Algorithm = "INSTANT"
	AlgorithmInplace Algorithm = "INPLACE"
	AlgorithmCopy    Algorithm = "COPY"
	AlgorithmUnknown Algorithm = "UNKNOWN"
)

// AlgorithmRank returns a conservative ordering for combining operations.
// Unknown deliberately ranks between INPLACE and COPY: it must never make a
// statement look safer than it is.
func AlgorithmRank(a Algorithm) int {
	switch a {
	case AlgorithmInstant:
		return 0
	case AlgorithmInplace:
		return 1
	case AlgorithmUnknown:
		return 2
	case AlgorithmCopy:
		return 3
	default:
		return 2
	}
}

// CombineAlgorithms returns the most expensive algorithm in ops. COPY wins
// over UNKNOWN so that an unknown side operation cannot hide a known COPY.
func CombineAlgorithms(ops []Algorithm) Algorithm {
	if len(ops) == 0 {
		return AlgorithmUnknown
	}
	best := AlgorithmInstant
	seenUnknown := false
	for _, a := range ops {
		switch a {
		case AlgorithmCopy:
			return AlgorithmCopy
		case AlgorithmUnknown:
			seenUnknown = true
		}
		if AlgorithmRank(a) > AlgorithmRank(best) {
			best = a
		}
	}
	if seenUnknown {
		return AlgorithmUnknown
	}
	return best
}

// Risk is the four-level risk classification used for statements and the
// overall report.
type Risk string

const (
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
	RiskCritical Risk = "critical"
)

// RiskRank returns 0..3 for low..critical. Unknown values are treated as
// medium so that a typo never silently lowers risk.
func RiskRank(r Risk) int {
	switch r {
	case RiskLow:
		return 0
	case RiskMedium:
		return 1
	case RiskHigh:
		return 2
	case RiskCritical:
		return 3
	default:
		return 1
	}
}

// MaxRisk returns the higher of two risk levels.
func MaxRisk(a, b Risk) Risk {
	if RiskRank(a) >= RiskRank(b) {
		return a
	}
	return b
}

// RiskFromSeverity converts a finding severity to a risk level.
func RiskFromSeverity(s Severity) Risk {
	switch s {
	case SeverityCritical:
		return RiskCritical
	case SeverityHigh:
		return RiskHigh
	case SeverityMedium:
		return RiskMedium
	default:
		return RiskLow
	}
}

// Severity is used for findings. Info findings are shown but do not affect the
// overall risk.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// SeverityRank returns 0..4 for info..critical.
func SeverityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	default:
		return 0
	}
}

// StatementKind is the parsed statement category.
type StatementKind string

const (
	KindAlterTable  StatementKind = "ALTER_TABLE"
	KindCreateIndex StatementKind = "CREATE_INDEX"
	KindDropIndex   StatementKind = "DROP_INDEX"
	KindCreateTable StatementKind = "CREATE_TABLE"
	KindDropTable   StatementKind = "DROP_TABLE"
	KindRenameTable StatementKind = "RENAME_TABLE"
	KindUnsupported StatementKind = "UNSUPPORTED"
	KindOther       StatementKind = "OTHER"
)

// OperationKind is a single ALTER option or DDL action.
type OperationKind string

const (
	OpAddColumn       OperationKind = "ADD_COLUMN"
	OpDropColumn      OperationKind = "DROP_COLUMN"
	OpModifyColumn    OperationKind = "MODIFY_COLUMN"
	OpChangeColumn    OperationKind = "CHANGE_COLUMN"
	OpRenameColumn    OperationKind = "RENAME_COLUMN"
	OpAddIndex        OperationKind = "ADD_INDEX"
	OpAddPrimaryKey   OperationKind = "ADD_PRIMARY_KEY"
	OpDropIndex       OperationKind = "DROP_INDEX"
	OpDropPrimaryKey  OperationKind = "DROP_PRIMARY_KEY"
	OpRenameIndex     OperationKind = "RENAME_INDEX"
	OpSetDefault      OperationKind = "SET_DEFAULT"
	OpDropDefault     OperationKind = "DROP_DEFAULT"
	OpSetVisibility   OperationKind = "SET_VISIBILITY"
	OpAddForeignKey   OperationKind = "ADD_FOREIGN_KEY"
	OpDropForeignKey  OperationKind = "DROP_FOREIGN_KEY"
	OpAddCheck        OperationKind = "ADD_CHECK"
	OpDropCheck       OperationKind = "DROP_CHECK"
	OpConvertCharset  OperationKind = "CONVERT_CHARSET"
	OpSetTableCharset OperationKind = "SET_TABLE_CHARSET"
	OpSetEngine       OperationKind = "SET_ENGINE"
	OpSetRowFormat    OperationKind = "SET_ROW_FORMAT"
	OpSetTableOption  OperationKind = "SET_TABLE_OPTION"
	OpPartition       OperationKind = "PARTITION"
	OpRenameTable     OperationKind = "RENAME_TABLE"
	OpForce           OperationKind = "FORCE"
	OpOrderBy         OperationKind = "ORDER_BY"
	OpKeyState        OperationKind = "KEY_STATE"
	OpTablespace      OperationKind = "TABLESPACE"
	OpCreateTable     OperationKind = "CREATE_TABLE"
	OpDropTable       OperationKind = "DROP_TABLE"
	OpOther           OperationKind = "OTHER"
)

// ColumnType is a normalized representation of a MySQL column type.
type ColumnType struct {
	Raw        string   `json:"raw"`
	Base       string   `json:"base"`
	Length     *int     `json:"length,omitempty"`
	Scale      *int     `json:"scale,omitempty"`
	Unsigned   bool     `json:"unsigned,omitempty"`
	Zerofill   bool     `json:"zerofill,omitempty"`
	Charset    string   `json:"charset,omitempty"`
	EnumValues []string `json:"enum_values,omitempty"`
}

// String returns a readable type representation.
func (t ColumnType) String() string {
	if t.Raw != "" {
		return t.Raw
	}
	var b strings.Builder
	b.WriteString(t.Base)
	if t.Length != nil {
		b.WriteByte('(')
		b.WriteString(itoa(*t.Length))
		if t.Scale != nil {
			b.WriteByte(',')
			b.WriteString(itoa(*t.Scale))
		}
		b.WriteByte(')')
	}
	if t.Unsigned {
		b.WriteString(" unsigned")
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Column is a column definition extracted from SQL or the catalog.
type Column struct {
	Name          string     `json:"name"`
	Type          ColumnType `json:"type"`
	Nullable      *bool      `json:"nullable,omitempty"`
	HasDefault    bool       `json:"has_default,omitempty"`
	Default       string     `json:"default,omitempty"`
	AutoIncrement bool       `json:"auto_increment,omitempty"`
	Position      string     `json:"position,omitempty"` // "", FIRST, or AFTER <col>
	Key           string     `json:"key,omitempty"`
	Extra         string     `json:"extra,omitempty"`
}

// Index describes an index definition extracted from SQL.
type Index struct {
	Name    string   `json:"name,omitempty"`
	Kind    string   `json:"kind"` // PRIMARY, UNIQUE, FULLTEXT, SPATIAL, INDEX
	Columns []string `json:"columns,omitempty"`
}

// ForeignKey describes a foreign key constraint extracted from SQL.
type ForeignKey struct {
	Name              string   `json:"name,omitempty"`
	Columns           []string `json:"columns,omitempty"`
	ReferencedTable   string   `json:"referenced_table,omitempty"`
	ReferencedColumns []string `json:"referenced_columns,omitempty"`
}

// Operation is one classified ALTER option.
type Operation struct {
	Kind      OperationKind `json:"kind"`
	Algorithm Algorithm     `json:"algorithm"`
	Lock      string        `json:"lock"`
	RuleID    string        `json:"rule_id"`
	Risk      Risk          `json:"risk"`
	Note      string        `json:"note,omitempty"`

	ColumnName        string   `json:"column,omitempty"`
	ColumnType        string   `json:"column_type,omitempty"`
	OldColumnName     string   `json:"old_column,omitempty"`
	NewColumnName     string   `json:"new_column,omitempty"`
	Position          string   `json:"position,omitempty"`
	IndexName         string   `json:"index_name,omitempty"`
	IndexKind         string   `json:"index_kind,omitempty"`
	IndexColumns      []string `json:"index_columns,omitempty"`
	ConstraintName    string   `json:"constraint_name,omitempty"`
	ReferencedTable   string   `json:"referenced_table,omitempty"`
	ReferencedColumns []string `json:"referenced_columns,omitempty"`
	TableOptionName   string   `json:"table_option,omitempty"`
	TableOptionValue  string   `json:"table_option_value,omitempty"`
	Value             string   `json:"value,omitempty"`
}

// Estimate is always a range plus a confidence. A nil Min/Max means the input
// needed for an estimate was not available; it must never be rendered as zero.
type Estimate struct {
	MinSeconds         *float64 `json:"min_seconds"`
	MaxSeconds         *float64 `json:"max_seconds"`
	Confidence         string   `json:"confidence"`
	Basis              string   `json:"basis"`
	CalibrationRecords int      `json:"calibration_records,omitempty"`
	CalibrationMBps    float64  `json:"calibration_mbps,omitempty"`
	Calibrated         bool     `json:"calibrated,omitempty"`
}

// Available reports whether the estimate contains a numeric range.
func (e Estimate) Available() bool { return e.MinSeconds != nil && e.MaxSeconds != nil }

// Statement is one parsed and analyzed SQL statement.
type Statement struct {
	Index              int           `json:"index"`
	File               string        `json:"file"`
	Line               int           `json:"line"`
	SQLSummary         string        `json:"sql_summary"`
	RawSQL             string        `json:"-"`
	Kind               StatementKind `json:"kind"`
	Table              string        `json:"table,omitempty"`
	Tables             []string      `json:"tables,omitempty"`
	Schema             string        `json:"schema,omitempty"`
	Operation          OperationKind `json:"operation"`
	Operations         []Operation   `json:"operations,omitempty"`
	PredictedAlgorithm Algorithm     `json:"predicted_algorithm"`
	Lock               string        `json:"lock"`
	Risk               Risk          `json:"risk"`
	RuleIDs            []string      `json:"rule_ids"`
	Estimate           Estimate      `json:"estimate"`
	DiskRequiredMB     *float64      `json:"disk_required_mb"`
	Suggestions        []string      `json:"suggestions,omitempty"`
	Notes              []string      `json:"notes,omitempty"`
	RequestedAlgorithm string        `json:"requested_algorithm,omitempty"`
	RequestedLock      string        `json:"requested_lock,omitempty"`
	Unsupported        bool          `json:"unsupported,omitempty"`
	Excluded           bool          `json:"excluded,omitempty"`
}

// Finding is a blocker or informational check result.
type Finding struct {
	ID             string         `json:"id"`
	Severity       Severity       `json:"severity"`
	Table          string         `json:"table,omitempty"`
	StatementIndex int            `json:"statement_index,omitempty"`
	Message        string         `json:"message"`
	Detail         map[string]any `json:"detail,omitempty"`
	Suggestion     string         `json:"suggestion,omitempty"`
}

// TableSummary is safe table metadata included in the report.
type TableSummary struct {
	Schema     string `json:"schema,omitempty"`
	Name       string `json:"name"`
	Rows       int64  `json:"rows"`
	RowsApprox bool   `json:"rows_approx,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	Engine     string `json:"engine,omitempty"`
	RowFormat  string `json:"row_format,omitempty"`
	Collation  string `json:"collation,omitempty"`
	Source     string `json:"source"` // online or offline-input
}

// MySQLInfo contains non-secret connection information.
type MySQLInfo struct {
	Version    string `json:"version,omitempty"`
	Database   string `json:"database,omitempty"`
	HostMasked string `json:"host_masked,omitempty"`
}

// Thresholds are the configurable limits used by the checker.
type Thresholds struct {
	LargeTableRows     int64   `json:"large_table_rows" yaml:"large_table_rows"`
	LargeTableSizeMB   int64   `json:"large_table_size_mb" yaml:"large_table_size_mb"`
	LongTransactionMin int     `json:"long_transaction_minutes" yaml:"long_transaction_minutes"`
	ReplicaLagSeconds  int     `json:"replica_lag_seconds" yaml:"replica_lag_seconds"`
	DiskSafetyFactor   float64 `json:"disk_safety_factor" yaml:"disk_safety_factor"`
}

// CalibrationInfo describes whether history was used for estimates.
type CalibrationInfo struct {
	Records    int     `json:"records"`
	Applied    bool    `json:"applied"`
	MedianMBps float64 `json:"median_mbps,omitempty"`
	Source     string  `json:"source,omitempty"`
}

// Report is the complete result of a check run.
type Report struct {
	Tool        string           `json:"tool"`
	Version     string           `json:"version"`
	GeneratedAt string           `json:"generated_at"`
	Mode        string           `json:"mode"` // online or offline
	MySQL       MySQLInfo        `json:"mysql"`
	OverallRisk Risk             `json:"overall_risk"`
	Statements  []Statement      `json:"statements"`
	Blockers    []Finding        `json:"blockers"`
	Unknowns    []string         `json:"unknowns"`
	Tables      []TableSummary   `json:"tables,omitempty"`
	Thresholds  Thresholds       `json:"thresholds"`
	Calibration *CalibrationInfo `json:"calibration,omitempty"`
	FailOn      string           `json:"fail_on"`
	ExitCode    int              `json:"exit_code"`
}
