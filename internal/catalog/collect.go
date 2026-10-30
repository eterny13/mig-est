package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// TableRef identifies a table to inspect.
type TableRef struct {
	Schema string
	Name   string
}

// CollectOptions controls threshold-sensitive checks.
type CollectOptions struct {
	LongTransactionMinutes int
	ReplicaLagSeconds      int
}

// Collect gathers metadata for the requested tables and global server state.
// It never returns an error for a single missing permission; those fields are
// recorded in Snapshot.Unknowns instead.
func (c *Client) Collect(ctx context.Context, version string, refs []TableRef, opts CollectOptions) *Snapshot {
	snap := &Snapshot{
		Version:          version,
		Database:         c.database,
		HostMasked:       c.host,
		Tables:           make(map[string]*TableInfo),
		LongTransactions: make(map[string][]LongTransaction),
		MDLWaits:         make(map[string][]MDLWait),
		CollectedAt:      time.Now(),
	}
	if opts.LongTransactionMinutes <= 0 {
		opts.LongTransactionMinutes = 5
	}
	if opts.ReplicaLagSeconds <= 0 {
		opts.ReplicaLagSeconds = 10
	}

	c.collectVariables(ctx, snap)
	snap.PerformanceSchemaOK = snap.PerformanceSchema
	c.collectIdleTransactions(ctx, snap, opts.LongTransactionMinutes)
	snap.Replication = c.collectReplication(ctx, snap)

	seen := make(map[string]bool)
	for _, ref := range refs {
		schema := c.tableSchema(ref.Schema)
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			continue
		}
		key := Key(schema, name)
		if seen[key] {
			continue
		}
		seen[key] = true
		ti := c.collectTable(ctx, snap, schema, name)
		snap.Tables[key] = ti
		if ti.Exists {
			c.collectLocks(ctx, snap, schema, name, opts.LongTransactionMinutes)
		}
	}
	return snap
}

func (c *Client) collectTable(ctx context.Context, snap *Snapshot, schema, name string) *TableInfo {
	ti := &TableInfo{Schema: schema, Name: name, Source: "online", RowsApprox: true}
	qctx, cancel := c.queryContext(ctx)
	defer cancel()

	var rows, dataLen, idxLen sql.NullInt64
	var engine, rowFormat, collation sql.NullString
	err := c.db.QueryRowContext(qctx, `
		SELECT TABLE_ROWS, DATA_LENGTH, INDEX_LENGTH, ENGINE, ROW_FORMAT, TABLE_COLLATION
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, schema, name).
		Scan(&rows, &dataLen, &idxLen, &engine, &rowFormat, &collation)
	if errors.Is(err, sql.ErrNoRows) {
		return ti
	}
	if err != nil {
		snap.AddUnknown(fmt.Sprintf("table %s.%s: table statistics unavailable: %v", schema, name, err))
		return ti
	}
	ti.Exists = true
	ti.Rows = rows.Int64
	ti.DataLength = dataLen.Int64
	ti.IndexLength = idxLen.Int64
	ti.Engine = engine.String
	ti.RowFormat = rowFormat.String
	ti.Collation = collation.String

	ti.Columns = c.collectColumns(ctx, snap, schema, name)
	ti.ColumnsKnown = !snap.HasUnknownPrefix(fmt.Sprintf("table %s.%s: columns", schema, name))
	ti.Indexes = c.collectIndexes(ctx, snap, schema, name)
	ti.IndexesKnown = !snap.HasUnknownPrefix(fmt.Sprintf("table %s.%s: indexes", schema, name))
	ti.OutgoingFKs = c.collectOutgoingFKs(ctx, snap, schema, name)
	ti.IncomingFKs = c.collectIncomingFKs(ctx, snap, schema, name)
	ti.FKsKnown = !snap.HasUnknownPrefix(fmt.Sprintf("table %s.%s: outgoing", schema, name)) &&
		!snap.HasUnknownPrefix(fmt.Sprintf("table %s.%s: incoming", schema, name))
	ti.Triggers = c.collectTriggers(ctx, snap, schema, name)
	ti.TriggersKnown = !snap.HasUnknownPrefix(fmt.Sprintf("table %s.%s: triggers", schema, name))
	ti.Partitioned = c.collectPartitioned(ctx, snap, schema, name)
	ti.MetadataComplete = ti.ColumnsKnown && ti.IndexesKnown
	return ti
}

func (c *Client) collectColumns(ctx context.Context, snap *Snapshot, schema, name string) []Column {
	qctx, cancel := c.queryContext(ctx)
	defer cancel()
	rows, err := c.db.QueryContext(qctx, `
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY, EXTRA, ORDINAL_POSITION
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, schema, name)
	if err != nil {
		snap.AddUnknown(fmt.Sprintf("table %s.%s: columns unavailable: %v", schema, name, err))
		return nil
	}
	defer rows.Close()
	var out []Column
	for rows.Next() {
		var col Column
		var nullable, key, extra string
		var def sql.NullString
		if err := rows.Scan(&col.Name, &col.TypeRaw, &nullable, &def, &key, &extra, &col.OrdinalPos); err != nil {
			snap.AddUnknown(fmt.Sprintf("table %s.%s: column metadata scan failed: %v", schema, name, err))
			continue
		}
		col.Nullable = strings.EqualFold(nullable, "YES")
		if def.Valid {
			v := def.String
			col.Default = &v
		}
		col.Key = key
		col.Extra = extra
		out = append(out, col)
	}
	return out
}

func (c *Client) collectIndexes(ctx context.Context, snap *Snapshot, schema, name string) []Index {
	qctx, cancel := c.queryContext(ctx)
	defer cancel()
	rows, err := c.db.QueryContext(qctx, `
		SELECT INDEX_NAME, NON_UNIQUE, INDEX_TYPE, COLUMN_NAME
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY INDEX_NAME, SEQ_IN_INDEX`, schema, name)
	if err != nil {
		snap.AddUnknown(fmt.Sprintf("table %s.%s: indexes unavailable: %v", schema, name, err))
		return nil
	}
	defer rows.Close()
	var out []Index
	byName := make(map[string]int)
	for rows.Next() {
		var idxName, idxType string
		var nonUnique int
		var col sql.NullString
		if err := rows.Scan(&idxName, &nonUnique, &idxType, &col); err != nil {
			snap.AddUnknown(fmt.Sprintf("table %s.%s: index metadata scan failed: %v", schema, name, err))
			continue
		}
		pos, ok := byName[idxName]
		if !ok {
			out = append(out, Index{Name: idxName, Unique: nonUnique == 0, Type: idxType})
			pos = len(out) - 1
			byName[idxName] = pos
		}
		if col.Valid {
			out[pos].Columns = append(out[pos].Columns, col.String)
		}
	}
	return out
}

func (c *Client) collectOutgoingFKs(ctx context.Context, snap *Snapshot, schema, name string) []ForeignKey {
	return c.collectFKs(ctx, snap, schema, name, false)
}

func (c *Client) collectIncomingFKs(ctx context.Context, snap *Snapshot, schema, name string) []ForeignKey {
	return c.collectFKs(ctx, snap, schema, name, true)
}

func (c *Client) collectFKs(ctx context.Context, snap *Snapshot, schema, name string, incoming bool) []ForeignKey {
	qctx, cancel := c.queryContext(ctx)
	defer cancel()
	query := `
		SELECT CONSTRAINT_NAME, TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY CONSTRAINT_NAME, ORDINAL_POSITION`
	args := []any{schema, name}
	if incoming {
		query = `
			SELECT CONSTRAINT_NAME, TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
			FROM information_schema.KEY_COLUMN_USAGE
			WHERE REFERENCED_TABLE_SCHEMA = ? AND REFERENCED_TABLE_NAME = ? AND REFERENCED_TABLE_NAME IS NOT NULL
			ORDER BY CONSTRAINT_NAME, ORDINAL_POSITION`
	}
	rows, err := c.db.QueryContext(qctx, query, args...)
	if err != nil {
		label := "outgoing foreign keys"
		if incoming {
			label = "incoming foreign keys"
		}
		snap.AddUnknown(fmt.Sprintf("table %s.%s: %s unavailable: %v", schema, name, label, err))
		return nil
	}
	defer rows.Close()
	var out []ForeignKey
	byName := make(map[string]int)
	for rows.Next() {
		var fkName, tableSchema, tableName, col string
		var refSchema, refTable, refCol sql.NullString
		if err := rows.Scan(&fkName, &tableSchema, &tableName, &col, &refSchema, &refTable, &refCol); err != nil {
			snap.AddUnknown(fmt.Sprintf("table %s.%s: foreign key metadata scan failed: %v", schema, name, err))
			continue
		}
		key := fkName + "\x00" + tableSchema + "\x00" + tableName
		pos, ok := byName[key]
		if !ok {
			out = append(out, ForeignKey{Name: fkName, Table: tableName})
			pos = len(out) - 1
			byName[key] = pos
		}
		out[pos].Columns = append(out[pos].Columns, col)
		if refTable.Valid {
			out[pos].ReferencedTable = refTable.String
		}
		if refCol.Valid {
			out[pos].ReferencedColumns = append(out[pos].ReferencedColumns, refCol.String)
		}
	}
	return out
}

func (c *Client) collectTriggers(ctx context.Context, snap *Snapshot, schema, name string) []Trigger {
	qctx, cancel := c.queryContext(ctx)
	defer cancel()
	rows, err := c.db.QueryContext(qctx, `
		SELECT TRIGGER_NAME, ACTION_TIMING, EVENT_MANIPULATION, ACTION_STATEMENT
		FROM information_schema.TRIGGERS
		WHERE EVENT_OBJECT_SCHEMA = ? AND EVENT_OBJECT_TABLE = ?`, schema, name)
	if err != nil {
		snap.AddUnknown(fmt.Sprintf("table %s.%s: triggers unavailable: %v", schema, name, err))
		return nil
	}
	defer rows.Close()
	var out []Trigger
	for rows.Next() {
		var t Trigger
		if err := rows.Scan(&t.Name, &t.Timing, &t.Event, &t.Statement); err != nil {
			snap.AddUnknown(fmt.Sprintf("table %s.%s: trigger metadata scan failed: %v", schema, name, err))
			continue
		}
		out = append(out, t)
	}
	return out
}

func (c *Client) collectPartitioned(ctx context.Context, snap *Snapshot, schema, name string) bool {
	qctx, cancel := c.queryContext(ctx)
	defer cancel()
	var n int
	err := c.db.QueryRowContext(qctx, `
		SELECT COUNT(*) FROM information_schema.PARTITIONS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND PARTITION_NAME IS NOT NULL`, schema, name).Scan(&n)
	if err != nil {
		snap.AddUnknown(fmt.Sprintf("table %s.%s: partition metadata unavailable: %v", schema, name, err))
		return false
	}
	return n > 0
}

func (c *Client) collectLocks(ctx context.Context, snap *Snapshot, schema, name string, longMinutes int) {
	key := Key(schema, name)
	if snap.PerformanceSchema {
		if trx := c.collectLongTransactions(ctx, snap, schema, name, longMinutes); len(trx) > 0 {
			snap.LongTransactions[key] = trx
		}
		if waits := c.collectMDLWaits(ctx, snap, schema, name); len(waits) > 0 {
			snap.MDLWaits[key] = waits
		}
	}
}

func (c *Client) collectLongTransactions(ctx context.Context, snap *Snapshot, schema, name string, minutes int) []LongTransaction {
	qctx, cancel := c.queryContext(ctx)
	defer cancel()
	rows, err := c.db.QueryContext(qctx, `
		SELECT trx.trx_mysql_thread_id,
		       TIMESTAMPDIFF(SECOND, trx.trx_started, NOW()) AS age_seconds,
		       COALESCE(trx.trx_state, ''),
		       COALESCE(trx.trx_query, ''),
		       COALESCE(ml.LOCK_TYPE, '')
		FROM information_schema.INNODB_TRX trx
		JOIN performance_schema.threads th ON th.PROCESSLIST_ID = trx.trx_mysql_thread_id
		JOIN performance_schema.metadata_locks ml ON ml.OWNER_THREAD_ID = th.THREAD_ID
		WHERE ml.OBJECT_SCHEMA = ? AND ml.OBJECT_NAME = ?
		  AND ml.LOCK_STATUS = 'GRANTED'
		  AND TIMESTAMPDIFF(SECOND, trx.trx_started, NOW()) >= ?
		GROUP BY trx.trx_mysql_thread_id, trx.trx_started, trx.trx_state, trx.trx_query, ml.LOCK_TYPE`,
		schema, name, minutes*60)
	if err != nil {
		snap.PerformanceSchemaOK = false
		snap.AddUnknown(fmt.Sprintf("metadata locks for %s.%s unavailable: %v", schema, name, err))
		return nil
	}
	defer rows.Close()
	var out []LongTransaction
	for rows.Next() {
		var trx LongTransaction
		if err := rows.Scan(&trx.ConnectionID, &trx.AgeSeconds, &trx.State, &trx.Query, &trx.LockType); err != nil {
			snap.AddUnknown(fmt.Sprintf("metadata lock scan failed for %s.%s: %v", schema, name, err))
			continue
		}
		out = append(out, trx)
	}
	return out
}

func (c *Client) collectMDLWaits(ctx context.Context, snap *Snapshot, schema, name string) []MDLWait {
	qctx, cancel := c.queryContext(ctx)
	defer cancel()
	rows, err := c.db.QueryContext(qctx, `
		SELECT th.PROCESSLIST_ID, ml.LOCK_TYPE, ml.LOCK_DURATION, COALESCE(th.PROCESSLIST_INFO, '')
		FROM performance_schema.metadata_locks ml
		JOIN performance_schema.threads th ON th.THREAD_ID = ml.OWNER_THREAD_ID
		WHERE ml.OBJECT_SCHEMA = ? AND ml.OBJECT_NAME = ? AND ml.LOCK_STATUS = 'PENDING'`,
		schema, name)
	if err != nil {
		snap.PerformanceSchemaOK = false
		snap.AddUnknown(fmt.Sprintf("pending metadata locks for %s.%s unavailable: %v", schema, name, err))
		return nil
	}
	defer rows.Close()
	var out []MDLWait
	for rows.Next() {
		var w MDLWait
		if err := rows.Scan(&w.ConnectionID, &w.LockType, &w.Duration, &w.Query); err != nil {
			snap.AddUnknown(fmt.Sprintf("pending metadata lock scan failed for %s.%s: %v", schema, name, err))
			continue
		}
		out = append(out, w)
	}
	return out
}

func (c *Client) collectIdleTransactions(ctx context.Context, snap *Snapshot, longMinutes int) {
	qctx, cancel := c.queryContext(ctx)
	defer cancel()
	threshold := longMinutes * 60
	query := `
		SELECT trx.trx_mysql_thread_id,
		       TIMESTAMPDIFF(SECOND, trx.trx_started, NOW()) AS age_seconds,
		       p.TIME,
		       COALESCE(p.USER, ''),
		       COALESCE(p.DB, '')
		FROM information_schema.INNODB_TRX trx
		JOIN information_schema.PROCESSLIST p ON p.ID = trx.trx_mysql_thread_id
		WHERE p.COMMAND = 'Sleep' AND p.TIME >= ?
		  AND TIMESTAMPDIFF(SECOND, trx.trx_started, NOW()) >= ?`
	args := []any{threshold, threshold}
	if c.database != "" {
		query += " AND p.DB = ?"
		args = append(args, c.database)
	}
	rows, err := c.db.QueryContext(qctx, query, args...)
	if err != nil {
		snap.AddUnknown(fmt.Sprintf("idle transaction check unavailable: %v", err))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var t IdleTransaction
		if err := rows.Scan(&t.ConnectionID, &t.AgeSeconds, &t.IdleSeconds, &t.User, &t.Database); err != nil {
			snap.AddUnknown(fmt.Sprintf("idle transaction scan failed: %v", err))
			continue
		}
		snap.IdleTransactions = append(snap.IdleTransactions, t)
	}
}

func (c *Client) collectVariables(ctx context.Context, snap *Snapshot) {
	qctx, cancel := c.queryContext(ctx)
	defer cancel()
	rows, err := c.db.QueryContext(qctx, `
		SHOW VARIABLES WHERE Variable_name IN ('performance_schema', 'innodb_online_alter_log_max_size')`)
	if err != nil {
		snap.AddUnknown(fmt.Sprintf("server variables unavailable: %v", err))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			snap.AddUnknown(fmt.Sprintf("server variable scan failed: %v", err))
			continue
		}
		switch strings.ToLower(name) {
		case "performance_schema":
			snap.PerformanceSchema = strings.EqualFold(value, "ON") || strings.EqualFold(value, "1")
		case "innodb_online_alter_log_max_size":
			snap.OnlineAlterLogBytes = parseByteSize(value)
		}
	}
}

func (c *Client) collectReplication(ctx context.Context, snap *Snapshot) *Replication {
	rows, cancel, err := c.replicationRows(ctx)
	if err != nil {
		snap.AddUnknown(fmt.Sprintf("replication status unavailable: %v", err))
		return nil
	}
	defer cancel()
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		snap.AddUnknown(fmt.Sprintf("replication status columns unavailable: %v", err))
		return nil
	}
	var rep *Replication
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			snap.AddUnknown(fmt.Sprintf("replication status scan failed: %v", err))
			continue
		}
		m := make(map[string]string, len(cols))
		for i, c := range cols {
			m[c] = valueString(vals[i])
		}
		if rep == nil {
			rep = &Replication{IsReplica: true, IORunning: true, SQLRunning: true, CheckedFrom: "SHOW REPLICA STATUS"}
		}
		rep.Channels++
		if !strings.EqualFold(first(m, "Replica_IO_Running", "Slave_IO_Running"), "Yes") {
			rep.IORunning = false
		}
		if !strings.EqualFold(first(m, "Replica_SQL_Running", "Slave_SQL_Running"), "Yes") {
			rep.SQLRunning = false
		}
		if lag := firstNonEmpty(m, "Seconds_Behind_Source", "Seconds_Behind_Master"); lag != "" {
			if n, err := strconv.ParseInt(lag, 10, 64); err == nil {
				if rep.SecondsBehind == nil || n > *rep.SecondsBehind {
					v := n
					rep.SecondsBehind = &v
				}
			}
		}
		if host := first(m, "Source_Host", "Master_Host"); host != "" {
			rep.SourceHost = host
		}
	}
	if rep == nil {
		return &Replication{IsReplica: false, CheckedFrom: "SHOW REPLICA STATUS"}
	}
	return rep
}

func (c *Client) replicationRows(ctx context.Context) (*sql.Rows, context.CancelFunc, error) {
	qctx, cancel := c.queryContext(ctx)
	rows, err := c.db.QueryContext(qctx, "SHOW REPLICA STATUS")
	if err == nil {
		return rows, cancel, nil
	}
	cancel()
	var myErr *mysql.MySQLError
	if !errors.As(err, &myErr) || myErr.Number != 1064 {
		return nil, func() {}, err
	}
	// MySQL 8.0.22+ uses REPLICA terminology; older versions still accept the
	// legacy command. Fall back for servers that reject the newer syntax.
	qctx2, cancel2 := c.queryContext(ctx)
	rows, err = c.db.QueryContext(qctx2, "SHOW SLAVE STATUS")
	if err != nil {
		cancel2()
		return nil, func() {}, err
	}
	return rows, cancel2, nil
}

func parseByteSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := int64(1)
	last := s[len(s)-1]
	switch last {
	case 'k', 'K':
		mult = 1024
		s = s[:len(s)-1]
	case 'm', 'M':
		mult = 1024 * 1024
		s = s[:len(s)-1]
	case 'g', 'G':
		mult = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case 't', 'T':
		mult = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n * mult
}

func valueString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	case string:
		return x
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		return fmt.Sprint(x)
	}
}

func first(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return ""
}

func firstNonEmpty(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != "" {
			return v
		}
	}
	return ""
}
