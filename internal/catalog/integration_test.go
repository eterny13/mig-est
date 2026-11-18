package catalog

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// TestIntegrationOnlineReadOnly runs only when MIGEST_TEST_DSN points at a
// disposable MySQL instance. It is a read-only smoke test.
func TestIntegrationOnlineReadOnly(t *testing.T) {
	dsn := os.Getenv("MIGEST_TEST_DSN")
	if dsn == "" {
		t.Skip("set MIGEST_TEST_DSN to run the online integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Open(ctx, dsn, 10*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()
	version, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	t.Logf("connected to MySQL %s at %s", version, c.HostMasked())
	snap := c.Collect(ctx, version, []TableRef{{Name: "orders"}}, CollectOptions{
		LongTransactionMinutes: 5,
		ReplicaLagSeconds:      10,
	})
	table := snap.Lookup(c.Database(), "orders")
	if table == nil || !table.Exists {
		t.Fatalf("orders table not found; unknowns=%v", snap.Unknowns)
	}
	if len(table.Columns) == 0 || len(table.Indexes) == 0 {
		t.Errorf("metadata was not collected: columns=%d indexes=%d unknowns=%v", len(table.Columns), len(table.Indexes), snap.Unknowns)
	}
	if table.Engine != "InnoDB" {
		t.Errorf("orders engine = %q, want InnoDB", table.Engine)
	}
	if len(table.OutgoingFKs) == 0 && table.FKsKnown {
		t.Errorf("orders foreign key was not collected")
	}
}

func TestIntegrationMDLBlocker(t *testing.T) {
	dsn := os.Getenv("MIGEST_TEST_DSN")
	if dsn == "" {
		t.Skip("set MIGEST_TEST_DSN to run the online MDL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	blockerDB, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer blockerDB.Close()

	tx, err := blockerDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders FOR SHARE").Scan(&count); err != nil {
		t.Fatalf("SELECT FOR SHARE: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)

	c, err := Open(ctx, dsn, 10*time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	version, err := c.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	snap := c.Collect(ctx, version, []TableRef{{Name: "orders"}}, CollectOptions{
		LongTransactionSeconds: 1,
		ReplicaLagSeconds:      10,
	})
	if !snap.PerformanceSchemaOK {
		t.Skipf("performance_schema metadata_locks unavailable: %v", snap.Unknowns)
	}
	key := Key(c.Database(), "orders")
	if len(snap.LongTransactions[key]) == 0 {
		t.Errorf("expected long transaction holding MDL on %s, got none (unknowns=%v)", key, snap.Unknowns)
	}
}
