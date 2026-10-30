package report

import (
	"fmt"
	"strings"

	"github.com/eterny13/mig-est/internal/model"
)

// Markdown renders a PR-friendly report.
func Markdown(r model.Report) string {
	var b strings.Builder
	b.WriteString("# mig-est report\n\n")
	b.WriteString("| Field | Value |\n")
	b.WriteString("| --- | --- |\n")
	fmt.Fprintf(&b, "| Generated | %s |\n", r.GeneratedAt)
	fmt.Fprintf(&b, "| Mode | %s |\n", r.Mode)
	if r.MySQL.Version != "" {
		fmt.Fprintf(&b, "| MySQL | %s |\n", r.MySQL.Version)
	}
	if r.MySQL.Database != "" {
		fmt.Fprintf(&b, "| Database | %s |\n", r.MySQL.Database)
	}
	if r.MySQL.HostMasked != "" {
		fmt.Fprintf(&b, "| Host | %s |\n", r.MySQL.HostMasked)
	}
	fmt.Fprintf(&b, "| Overall risk | **%s** |\n", strings.ToUpper(string(r.OverallRisk)))
	fmt.Fprintf(&b, "| Fail-on | %s |\n", r.FailOn)
	fmt.Fprintf(&b, "| Exit code | %d |\n\n", r.ExitCode)

	if len(r.Tables) > 0 {
		b.WriteString("## Target tables\n\n")
		b.WriteString("| Table | Rows | Size | Engine | Row format | Source |\n")
		b.WriteString("| --- | ---: | ---: | --- | --- | --- |\n")
		for _, t := range r.Tables {
			rows := humanInt(t.Rows)
			if t.RowsApprox {
				rows = "~" + rows
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s |\n", mdIdent(t.Name), rows, humanBytes(t.SizeBytes), md(t.Engine), md(t.RowFormat), t.Source)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Statements\n")
	for _, s := range r.Statements {
		status := ""
		if s.Excluded {
			status = " _(out of scope)_"
		} else if s.Unsupported {
			status = " _(unsupported)_"
		}
		fmt.Fprintf(&b, "\n### [%d] %s%s\n\n", s.Index, md(s.SQLSummary), status)
		fmt.Fprintf(&b, "- Location: `%s:%d`\n", s.File, s.Line)
		if s.Table != "" {
			fmt.Fprintf(&b, "- Table: `%s`\n", mdIdent(s.Table))
			fmt.Fprintf(&b, "- Operation: `%s`\n", s.Operation)
			fmt.Fprintf(&b, "- Predicted algorithm: **%s**", s.PredictedAlgorithm)
			if s.Lock != "" {
				fmt.Fprintf(&b, " (LOCK=%s)", s.Lock)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "- Risk: **%s**\n", strings.ToUpper(string(s.Risk)))
		if len(s.RuleIDs) > 0 {
			fmt.Fprintf(&b, "- Rules: %s\n", strings.Join(backtickAll(s.RuleIDs), ", "))
		}
		if s.RequestedAlgorithm != "" || s.RequestedLock != "" {
			fmt.Fprintf(&b, "- Requested: ALGORITHM=%s LOCK=%s\n", emptyDefault(s.RequestedAlgorithm), emptyDefault(s.RequestedLock))
		}
		fmt.Fprintf(&b, "- Estimate: %s (confidence: %s)\n", estimateRange(s.Estimate), s.Estimate.Confidence)
		if s.Estimate.Basis != "" {
			fmt.Fprintf(&b, "- Basis: %s\n", md(s.Estimate.Basis))
		}
		if s.DiskRequiredMB != nil {
			if *s.DiskRequiredMB > 0 {
				fmt.Fprintf(&b, "- Disk required: %.1f MB\n", *s.DiskRequiredMB)
			} else {
				b.WriteString("- Disk required: none\n")
			}
		}
		if len(s.Operations) > 1 {
			b.WriteString("\n| Operation | Algorithm | Lock | Rule | Risk |\n")
			b.WriteString("| --- | --- | --- | --- | --- |\n")
			for _, op := range s.Operations {
				fmt.Fprintf(&b, "| `%s` | %s | %s | `%s` | %s |\n", op.Kind, op.Algorithm, op.Lock, op.RuleID, op.Risk)
			}
		}
		if len(s.Notes) > 0 {
			b.WriteString("\n**Notes**\n\n")
			for _, n := range s.Notes {
				fmt.Fprintf(&b, "- %s\n", md(n))
			}
		}
		if len(s.Suggestions) > 0 {
			b.WriteString("\n**Suggestions**\n\n")
			for _, sug := range s.Suggestions {
				fmt.Fprintf(&b, "- %s\n", md(sug))
			}
		}
	}

	b.WriteString("\n## Findings\n\n")
	if len(r.Blockers) == 0 {
		b.WriteString("No findings.\n")
	} else {
		b.WriteString("| Severity | ID | Table | Message | Suggestion |\n")
		b.WriteString("| --- | --- | --- | --- | --- |\n")
		for _, f := range r.Blockers {
			fmt.Fprintf(&b, "| **%s** | `%s` | %s | %s | %s |\n",
				strings.ToUpper(string(f.Severity)), f.ID, mdIdent(f.Table), md(f.Message), md(f.Suggestion))
		}
	}

	if len(r.Unknowns) > 0 {
		b.WriteString("\n## Unknowns\n\n")
		for _, u := range r.Unknowns {
			fmt.Fprintf(&b, "- %s\n", md(u))
		}
	}

	if r.Calibration != nil {
		b.WriteString("\n## Calibration\n\n")
		fmt.Fprintf(&b, "- History records loaded: %d\n", r.Calibration.Records)
		if r.Calibration.Applied {
			if r.Calibration.MedianMBps > 0 {
				fmt.Fprintf(&b, "- Applied median: %.1f MB/s\n", r.Calibration.MedianMBps)
			} else {
				b.WriteString("- Calibration applied.\n")
			}
		} else {
			b.WriteString("- Not enough matching records for calibration (need at least 3).\n")
		}
	}

	return b.String()
}

func md(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func mdIdent(s string) string {
	if s == "" {
		return "-"
	}
	return strings.ReplaceAll(s, "`", "\\`")
}

func backtickAll(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, "`"+item+"`")
	}
	return out
}
