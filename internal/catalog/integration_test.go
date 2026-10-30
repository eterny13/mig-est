package catalog

import (
	"context"
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
