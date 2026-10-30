package catalog

import (
	"strings"
	"testing"
)

func TestMaskDSN(t *testing.T) {
	tests := []struct {
		dsn  string
		want string
	}{
		{"migread:secret@tcp(db.example.com:3306)/appdb", "migread@tcp(db.example.com:3306)/appdb"},
		{"migread:secret@tcp(db.example.com:3306)/appdb?parseTime=true", "migread@tcp(db.example.com:3306)/appdb"},
		{"tcp(db:3306)/app", "tcp(db:3306)/app"},
		{"bad dsn", "<invalid DSN>"},
	}
	for _, tt := range tests {
		got := MaskDSN(tt.dsn)
		if got != tt.want {
			t.Errorf("MaskDSN(%q) = %q, want %q", tt.dsn, got, tt.want)
		}
		if strings.Contains(got, "secret") {
			t.Errorf("MaskDSN leaked a password: %q", got)
		}
	}
}

func TestKeyAndLookup(t *testing.T) {
	snap := &Snapshot{
		Database: "app",
		Tables: map[string]*TableInfo{
			Key("app", "orders"): {Schema: "app", Name: "orders"},
			"events":             {Schema: "analytics", Name: "events"},
		},
	}
	for _, tc := range []struct {
		schema, name string
		want         string
	}{
		{"app", "orders", "orders"},
		{"", "orders", "orders"},
		{"APP", "ORDERS", "orders"},
		{"other", "orders", "orders"},
		{"", "events", "events"},
	} {
		got := snap.Lookup(tc.schema, tc.name)
		if got == nil || got.Name != tc.want {
			t.Errorf("Lookup(%q, %q) = %+v, want %s", tc.schema, tc.name, got, tc.want)
		}
	}
	if Key(" App ", " Orders ") != "app.orders" {
		t.Errorf("Key normalization failed: %q", Key(" App ", " Orders "))
	}
}

func TestParseByteSize(t *testing.T) {
	for raw, want := range map[string]int64{"128": 128, "128K": 128 * 1024, "128M": 128 * 1024 * 1024, "1G": 1024 * 1024 * 1024} {
		if got := parseByteSize(raw); got != want {
			t.Errorf("parseByteSize(%q) = %d, want %d", raw, got, want)
		}
	}
	if got := parseByteSize("bad"); got != 0 {
		t.Errorf("bad size = %d, want 0", got)
	}
}
