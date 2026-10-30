package cli

import "testing"

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		raw  string
		want int64
	}{
		{"100", 100},
		{"1KB", 1024},
		{"1.5MB", 1572864},
		{"8GB", 8 * 1024 * 1024 * 1024},
		{"1TiB", 1024 * 1024 * 1024 * 1024},
		{"8200MB", 8200 * 1024 * 1024},
		{" 2 G ", 2 * 1024 * 1024 * 1024},
	}
	for _, tt := range tests {
		got, err := parseByteSize(tt.raw)
		if err != nil || got != tt.want {
			t.Errorf("parseByteSize(%q) = %d, %v; want %d", tt.raw, got, err, tt.want)
		}
	}
	for _, raw := range []string{"", "MB", "1XB", "-1GB", "abc"} {
		if _, err := parseByteSize(raw); err == nil {
			t.Errorf("parseByteSize(%q) should fail", raw)
		}
	}
}

func TestParseRows(t *testing.T) {
	for raw, want := range map[string]int64{"1000": 1000, "1,000,000": 1000000, "1_000": 1000, "0": 0} {
		got, err := parseRows(raw)
		if err != nil || got != want {
			t.Errorf("parseRows(%q) = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "-1", "1.5"} {
		if _, err := parseRows(raw); err == nil {
			t.Errorf("parseRows(%q) should fail", raw)
		}
	}
}

func TestParseTableInputs(t *testing.T) {
	got, err := parseTableInputs(
		[]string{"orders=8200MB", "shop.users=1GB"},
		[]string{"orders=12400000", "shop.users=100"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tables: %+v", len(got), got)
	}
	orders := got["orders"]
	if orders == nil || !orders.HasSize || !orders.HasRows || orders.Rows != 12400000 {
		t.Errorf("orders = %+v", orders)
	}
	users := got["shop.users"]
	if users == nil || users.Schema != "shop" || users.Name != "users" || !users.HasSize {
		t.Errorf("users = %+v", users)
	}
	for _, args := range [][2][]string{
		{{"orders"}, nil},
		{{"orders=bad"}, nil},
		{nil, {"orders=bad"}},
	} {
		if _, err := parseTableInputs(args[0], args[1]); err == nil {
			t.Errorf("parseTableInputs(%v, %v) should fail", args[0], args[1])
		}
	}
}

func TestParseActual(t *testing.T) {
	d, err := parseActual("8m12s")
	if err != nil || d.Seconds() != 492 {
		t.Errorf("8m12s = %v, %v", d, err)
	}
	d, err = parseActual("12.5")
	if err != nil || d.Seconds() != 12.5 {
		t.Errorf("12.5 = %v, %v", d, err)
	}
	for _, raw := range []string{"", "0", "-1s", "soon"} {
		if _, err := parseActual(raw); err == nil {
			t.Errorf("parseActual(%q) should fail", raw)
		}
	}
}
