// Package catalog collects read-only table and server metadata from MySQL.
package catalog

import (
	"strings"
	"time"
)

// Column is a column from information_schema.COLUMNS.
type Column struct {
	Name       string
	TypeRaw    string
	Nullable   bool
	Default    *string
	Key        string
	Extra      string
	OrdinalPos int
}

// Index is an index from information_schema.STATISTICS.
type Index struct {
	Name    string
	Unique  bool
	Type    string
	Columns []string
}

// ForeignKey describes a foreign key relationship.
type ForeignKey struct {
	Name              string
	Table             string
	Columns           []string
	ReferencedTable   string
	ReferencedColumns []string
}

// Trigger is a trigger on a table.
type Trigger struct {
	Name      string
	Timing    string
	Event     string
	Statement string
}

// TableInfo is the metadata mig-est uses to classify and size a table.
type TableInfo struct {
	Schema      string
	Name        string
	Exists      bool
	Rows        int64
	RowsApprox  bool
	DataLength  int64
	IndexLength int64
	Engine      string
	RowFormat   string
	Collation   string
	Columns     []Column
	Indexes     []Index
	OutgoingFKs []ForeignKey
	IncomingFKs []ForeignKey
	Triggers    []Trigger
	Partitioned bool
	Source      string // online, offline-input, or script

	ColumnsKnown     bool
	IndexesKnown     bool
	TriggersKnown    bool
	FKsKnown         bool
	MetadataComplete bool
}

// SizeBytes is data plus index length.
func (t *TableInfo) SizeBytes() int64 {
	if t == nil {
		return 0
	}
	return t.DataLength + t.IndexLength
}

// HasPrimaryKey reports whether a primary key is present.
func (t *TableInfo) HasPrimaryKey() bool {
	if t == nil {
		return false
	}
	for _, idx := range t.Indexes {
		if strings.EqualFold(idx.Name, "PRIMARY") {
			return true
		}
	}
	return false
}

// HasUniqueKey reports whether a unique key other than the primary key exists.
func (t *TableInfo) HasUniqueKey() bool {
	if t == nil {
		return false
	}
	for _, idx := range t.Indexes {
		if idx.Unique && !strings.EqualFold(idx.Name, "PRIMARY") {
			return true
		}
	}
	return false
}

// HasFullText reports whether the table has a FULLTEXT index.
func (t *TableInfo) HasFullText() bool {
	if t == nil {
		return false
	}
	for _, idx := range t.Indexes {
		if strings.EqualFold(idx.Type, "FULLTEXT") {
			return true
		}
	}
	return false
}

// FindColumn returns a column by name, case-insensitively.
func (t *TableInfo) FindColumn(name string) *Column {
	if t == nil {
		return nil
	}
	for i := range t.Columns {
		if strings.EqualFold(t.Columns[i].Name, name) {
			return &t.Columns[i]
		}
	}
	return nil
}

// FindIndexByColumns returns an index whose leading columns match cols.
func (t *TableInfo) FindIndexByColumns(cols []string) *Index {
	if t == nil || len(cols) == 0 {
		return nil
	}
	for i := range t.Indexes {
		idx := &t.Indexes[i]
		if len(idx.Columns) < len(cols) {
			continue
		}
		match := true
		for j, c := range cols {
			if !strings.EqualFold(idx.Columns[j], c) {
				match = false
				break
			}
		}
		if match {
			return idx
		}
	}
	return nil
}

// LongTransaction is a transaction older than the configured threshold.
type LongTransaction struct {
	ConnectionID int64
	AgeSeconds   int64
	State        string
	Query        string
	LockType     string
}

// MDLWait is a pending metadata lock request.
type MDLWait struct {
	ConnectionID int64
	LockType     string
	Duration     string
	Query        string
}

// IdleTransaction is a sleeping connection that still has an InnoDB
// transaction open.
type IdleTransaction struct {
	ConnectionID int64
	AgeSeconds   int64
	IdleSeconds  int64
	User         string
	Database     string
}

// Replication describes replica health when the target is a replica.
type Replication struct {
	IsReplica     bool
	IORunning     bool
	SQLRunning    bool
	SecondsBehind *int64
	SourceHost    string
	CheckedFrom   string
	Channels      int
}

// Snapshot is all read-only metadata collected for one check run.
type Snapshot struct {
	Version             string
	Database            string
	HostMasked          string
	Tables              map[string]*TableInfo
	LongTransactions    map[string][]LongTransaction // table key -> transactions
	MDLWaits            map[string][]MDLWait
	IdleTransactions    []IdleTransaction
	Replication         *Replication
	PerformanceSchema   bool
	PerformanceSchemaOK bool
	OnlineAlterLogBytes int64
	Unknowns            []string
	CollectedAt         time.Time
}

// AddUnknown records a field that could not be collected, without aborting
// the whole run.
func (s *Snapshot) AddUnknown(msg string) {
	if s == nil || msg == "" {
		return
	}
	for _, u := range s.Unknowns {
		if u == msg {
			return
		}
	}
	s.Unknowns = append(s.Unknowns, msg)
}

// HasUnknownPrefix reports whether an unknown entry starts with prefix.
func (s *Snapshot) HasUnknownPrefix(prefix string) bool {
	if s == nil {
		return false
	}
	for _, u := range s.Unknowns {
		if strings.HasPrefix(u, prefix) {
			return true
		}
	}
	return false
}
