package mysqlver

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		raw        string
		want       string
		major      int
		minor      int
		patch      int
		patchKnown bool
	}{
		{"8.0.36", "8.0.36", 8, 0, 36, true},
		{"8.4.0-log", "8.4.0", 8, 4, 0, true},
		{"8.0.36-0ubuntu0.22.04.1", "8.0.36", 8, 0, 36, true},
		{"8.0", "8.0", 8, 0, 0, false},
		{" 8.4 ", "8.4", 8, 4, 0, false},
	}
	for _, tt := range tests {
		got, err := Parse(tt.raw)
		if err != nil {
			t.Fatalf("%q: %v", tt.raw, err)
		}
		if got.String() != tt.want || got.Major != tt.major || got.Minor != tt.minor || got.Patch != tt.patch || got.PatchKnown != tt.patchKnown {
			t.Errorf("%q = %+v, want %s (%d.%d.%d known=%v)", tt.raw, got, tt.want, tt.major, tt.minor, tt.patch, tt.patchKnown)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, raw := range []string{"", "8", "8.0.x", "MariaDB 10.6.16"} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("%q: expected an error", raw)
		}
	}
}

func TestSupportAndFeatureGates(t *testing.T) {
	v, _ := Parse("8.0.28")
	if !v.IsSupported() || v.AtLeast(8, 0, 29) || !v.AtLeast(8, 0, 12) {
		t.Errorf("8.0.28 gates are wrong: %+v", v)
	}
	v, _ = Parse("8.4")
	if !v.IsSupported() {
		t.Errorf("8.4 should be supported")
	}
	// A missing patch level is treated as the latest patch in the series.
	if !v.AtLeast(8, 0, 29) || !v.AtLeast(8, 4, 99) {
		t.Errorf("8.4 gates are wrong: %+v", v)
	}
	v, _ = Parse("5.7.44")
	if v.IsSupported() || v.AtLeast(8, 0, 12) {
		t.Errorf("5.7 must be unsupported: %+v", v)
	}
}
