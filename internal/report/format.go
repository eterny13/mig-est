package report

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/eterny13/mig-est/internal/model"
)

func estimateRange(e model.Estimate) string {
	if !e.Available() {
		return "unknown"
	}
	min, max := *e.MinSeconds, *e.MaxSeconds
	if max <= 0 {
		return "<1s"
	}
	if min <= 0 && max <= 1 {
		return "<1s"
	}
	return formatSeconds(min) + " to " + formatSeconds(max)
}

func formatSeconds(s float64) string {
	if s < 1 {
		return "<1s"
	}
	if s < 60 {
		return fmt.Sprintf("%ds", int(math.Round(s)))
	}
	if s < 3600 {
		m := int(s) / 60
		sec := int(math.Round(s)) % 60
		if sec == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	if s < 86400 {
		h := int(s) / 3600
		m := (int(s) % 3600) / 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	d := int(s) / 86400
	h := (int(s) % 86400) / 3600
	if h == 0 {
		return fmt.Sprintf("%dd", d)
	}
	return fmt.Sprintf("%dd %dh", d, h)
}

func humanInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		out = "-" + out
	}
	return out
}

func humanBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case n >= tb:
		return fmt.Sprintf("%.1f TB", float64(n)/tb)
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/gb)
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func approx(t model.TableSummary) string {
	if t.RowsApprox {
		return " (approx)"
	}
	return ""
}
