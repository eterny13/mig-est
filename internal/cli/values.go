package cli

import (
	"fmt"
	"strconv"
	"strings"
)

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*s = append(*s, v)
	return nil
}

// parseByteSize accepts 8200MB, 8GB, 1.5GiB, or a plain byte count.
func parseByteSize(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	upper := strings.ToUpper(s)
	mult := int64(1)
	for _, suffix := range []struct {
		names []string
		mult  int64
	}{
		{[]string{"TIB", "TB", "T"}, 1024 * 1024 * 1024 * 1024},
		{[]string{"GIB", "GB", "G"}, 1024 * 1024 * 1024},
		{[]string{"MIB", "MB", "M"}, 1024 * 1024},
		{[]string{"KIB", "KB", "K"}, 1024},
		{[]string{"B"}, 1},
	} {
		for _, name := range suffix.names {
			if strings.HasSuffix(upper, name) {
				mult = suffix.mult
				upper = strings.TrimSpace(strings.TrimSuffix(upper, name))
				goto parsed
			}
		}
	}
parsed:
	if upper == "" {
		return 0, fmt.Errorf("invalid size %q", raw)
	}
	f, err := strconv.ParseFloat(upper, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid size %q", raw)
	}
	return int64(f * float64(mult)), nil
}

func parseRows(raw string) (int64, error) {
	s := strings.ReplaceAll(strings.TrimSpace(raw), ",", "")
	s = strings.ReplaceAll(s, "_", "")
	if s == "" {
		return 0, fmt.Errorf("empty row count")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid row count %q", raw)
	}
	return n, nil
}

// splitAssignment splits name=value on the first '='.
func splitAssignment(raw string) (string, string, error) {
	i := strings.Index(raw, "=")
	if i <= 0 || i == len(raw)-1 {
		return "", "", fmt.Errorf("expected name=value, got %q", raw)
	}
	return strings.TrimSpace(raw[:i]), strings.TrimSpace(raw[i+1:]), nil
}

// splitTableName splits an optional schema qualifier.
func splitTableName(name string) (schema, table string) {
	name = strings.TrimSpace(strings.Trim(name, "`"))
	if i := strings.LastIndex(name, "."); i >= 0 {
		return strings.Trim(name[:i], "`"), strings.Trim(name[i+1:], "`")
	}
	return "", name
}

type tableInput struct {
	Schema  string
	Name    string
	Size    int64
	Rows    int64
	HasSize bool
	HasRows bool
}

func parseTableInputs(sizes, rows []string) (map[string]*tableInput, error) {
	out := make(map[string]*tableInput)
	get := func(raw string) (*tableInput, error) {
		key, _, err := splitAssignment(raw)
		if err != nil {
			return nil, err
		}
		schema, name := splitTableName(key)
		if name == "" {
			return nil, fmt.Errorf("empty table name in %q", raw)
		}
		mapKey := strings.ToLower(schema + "." + name)
		if schema == "" {
			mapKey = strings.ToLower(name)
		}
		ti := out[mapKey]
		if ti == nil {
			ti = &tableInput{Schema: schema, Name: name}
			out[mapKey] = ti
		}
		return ti, nil
	}
	for _, raw := range sizes {
		ti, err := get(raw)
		if err != nil {
			return nil, err
		}
		_, value, _ := splitAssignment(raw)
		n, err := parseByteSize(value)
		if err != nil {
			return nil, err
		}
		ti.Size = n
		ti.HasSize = true
	}
	for _, raw := range rows {
		ti, err := get(raw)
		if err != nil {
			return nil, err
		}
		_, value, _ := splitAssignment(raw)
		n, err := parseRows(value)
		if err != nil {
			return nil, err
		}
		ti.Rows = n
		ti.HasRows = true
	}
	return out, nil
}
