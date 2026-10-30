// Package report renders check results as text, Markdown, or JSON.
package report

import (
	"fmt"
	"strings"

	"github.com/eterny13/mig-est/internal/model"
)

// Options controls rendering.
type Options struct {
	Color  bool
	Quiet  bool
	FailOn string
}

const (
	ansiReset   = "\x1b[0m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiRed     = "\x1b[31m"
	ansiBoldRed = "\x1b[1;31m"
	ansiBlue    = "\x1b[34m"
	ansiDim     = "\x1b[2m"
)

// Text renders the human-facing report.
func Text(r model.Report, opts Options) string {
	var b strings.Builder
	col := func(s, color string) string {
		if !opts.Color {
			return s
		}
		return color + s + ansiReset
	}
	risk := func(x model.Risk) string {
		return col(strings.ToUpper(string(x)), riskColor(x))
	}
	sev := func(s model.Severity) string {
		return col("["+strings.ToUpper(string(s))+"]", severityColor(s))
	}

	if opts.Quiet {
		fmt.Fprintf(&b, "mig-est: %d statement(s), overall %s, exit %d (fail-on %s)\n",
			len(r.Statements), strings.ToUpper(string(r.OverallRisk)), r.ExitCode, r.FailOn)
		return b.String()
	}

	fmt.Fprintf(&b, "mig-est %s\n", r.Version)
	fmt.Fprintf(&b, "Report generated: %s\n", r.GeneratedAt)
	fmt.Fprintf(&b, "Mode: %s\n", r.Mode)
	if r.MySQL.Version != "" || r.MySQL.Database != "" {
		fmt.Fprintf(&b, "MySQL: %s", r.MySQL.Version)
		if r.MySQL.Database != "" {
			fmt.Fprintf(&b, " / database %s", r.MySQL.Database)
		}
		if r.MySQL.HostMasked != "" {
			fmt.Fprintf(&b, " / %s", r.MySQL.HostMasked)
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintf(&b, "Overall risk: %s\n", risk(r.OverallRisk))
	fmt.Fprintf(&b, "Fail-on: %s\n\n", r.FailOn)

	if len(r.Tables) > 0 {
		b.WriteString("Target tables:\n")
		for _, t := range r.Tables {
			source := ""
			if t.Source == "offline-input" {
				source = " (size supplied offline)"
			} else if t.Source == "script" {
				source = " (created by this migration)"
			}
			fmt.Fprintf(&b, "  %s: %s rows%s / %s", t.Name, humanInt(t.Rows), approx(t), humanBytes(t.SizeBytes))
			if t.Engine != "" || t.RowFormat != "" {
				fmt.Fprintf(&b, " / %s %s", t.Engine, t.RowFormat)
			}
			fmt.Fprintf(&b, "%s\n", source)
		}
		b.WriteString("\n")
	}

	b.WriteString("Statements:\n")
	for _, s := range r.Statements {
		status := ""
		if s.Excluded {
			status = " (out of scope)"
		} else if s.Unsupported {
			status = " (unsupported)"
		}
		fmt.Fprintf(&b, "\n[%d] %s%s\n", s.Index, s.SQLSummary, status)
		fmt.Fprintf(&b, "    location: %s:%d\n", s.File, s.Line)
		if s.Table != "" {
			fmt.Fprintf(&b, "    table: %s", s.Table)
			if s.Operation != "" {
				fmt.Fprintf(&b, " / operation: %s", s.Operation)
			}
			fmt.Fprintf(&b, " / predicted: %s", s.PredictedAlgorithm)
			if s.Lock != "" {
				fmt.Fprintf(&b, " (LOCK=%s)", s.Lock)
			}
			fmt.Fprintln(&b)
		}
		fmt.Fprintf(&b, "    risk: %s", risk(s.Risk))
		if len(s.RuleIDs) > 0 {
			fmt.Fprintf(&b, " / rules: %s", strings.Join(s.RuleIDs, ", "))
		}
		fmt.Fprintln(&b)
		if s.RequestedAlgorithm != "" || s.RequestedLock != "" {
			fmt.Fprintf(&b, "    requested: ALGORITHM=%s LOCK=%s\n", emptyDefault(s.RequestedAlgorithm), emptyDefault(s.RequestedLock))
		}
		fmt.Fprintf(&b, "    estimate: %s (confidence: %s)\n", estimateRange(s.Estimate), s.Estimate.Confidence)
		if s.Estimate.Basis != "" {
			fmt.Fprintf(&b, "    basis: %s\n", s.Estimate.Basis)
		}
		if s.DiskRequiredMB != nil {
			if *s.DiskRequiredMB > 0 {
				fmt.Fprintf(&b, "    disk required: %.1f MB (safety factor applied)\n", *s.DiskRequiredMB)
			} else {
				fmt.Fprintf(&b, "    disk required: none\n")
			}
		}
		if len(s.Operations) > 1 {
			b.WriteString("    operations:\n")
			for _, op := range s.Operations {
				fmt.Fprintf(&b, "      - %s: %s (rule %s, %s)\n", op.Kind, op.Algorithm, op.RuleID, op.Risk)
			}
		}
		for _, n := range s.Notes {
			fmt.Fprintf(&b, "    note: %s\n", n)
		}
		if len(s.Suggestions) > 0 {
			b.WriteString("    suggestions:\n")
			for _, sug := range s.Suggestions {
				fmt.Fprintf(&b, "      - %s\n", sug)
			}
		}
	}

	b.WriteString("\nFindings:\n")
	if len(r.Blockers) == 0 {
		b.WriteString("  none\n")
	} else {
		for _, f := range r.Blockers {
			table := ""
			if f.Table != "" {
				table = " (" + f.Table + ")"
			}
			fmt.Fprintf(&b, "  %s %s%s: %s\n", sev(f.Severity), f.ID, table, f.Message)
			if len(f.Detail) > 0 {
				fmt.Fprintf(&b, "      %s\n", formatDetail(f.Detail))
			}
			if f.Suggestion != "" {
				fmt.Fprintf(&b, "      => %s\n", f.Suggestion)
			}
		}
	}

	if len(r.Unknowns) > 0 {
		b.WriteString("\nUnknowns:\n")
		for _, u := range r.Unknowns {
			fmt.Fprintf(&b, "  - %s\n", u)
		}
	}

	if r.Calibration != nil {
		b.WriteString("\nCalibration:\n")
		fmt.Fprintf(&b, "  history records loaded: %d", r.Calibration.Records)
		if r.Calibration.Applied && r.Calibration.MedianMBps > 0 {
			fmt.Fprintf(&b, " / applied median: %.1f MB/s", r.Calibration.MedianMBps)
		} else if r.Calibration.Applied {
			b.WriteString(" / calibration applied")
		} else {
			b.WriteString(" / not enough matching records (need at least 3)")
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "\nExit code: %d (fail-on %s)\n", r.ExitCode, r.FailOn)
	return b.String()
}

func riskColor(r model.Risk) string {
	switch r {
	case model.RiskCritical:
		return ansiBoldRed
	case model.RiskHigh:
		return ansiRed
	case model.RiskMedium:
		return ansiYellow
	default:
		return ansiGreen
	}
}

func severityColor(s model.Severity) string {
	switch s {
	case model.SeverityCritical:
		return ansiBoldRed
	case model.SeverityHigh:
		return ansiRed
	case model.SeverityMedium:
		return ansiYellow
	case model.SeverityLow:
		return ansiGreen
	default:
		return ansiBlue
	}
}

func emptyDefault(s string) string {
	if s == "" {
		return "DEFAULT"
	}
	return s
}

func formatDetail(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Stable output.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%v", k, m[k])
	}
	return b.String()
}
