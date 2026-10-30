package classifier

import (
	"fmt"
	"strings"

	"github.com/eterny13/mig-est/internal/model"
	"github.com/eterny13/mig-est/internal/parser"
)

// TypeChange is the result of comparing two column types.
type TypeChange struct {
	Algorithm model.Algorithm
	Risk      model.Risk
	RuleID    string
	Note      string
}

var integerRank = map[string]int{
	"tinyint": 1, "smallint": 2, "mediumint": 3, "int": 4, "integer": 4, "bigint": 5,
}

var textRank = map[string]int{
	"tinytext": 1, "text": 2, "mediumtext": 3, "longtext": 4,
}

var blobRank = map[string]int{
	"tinyblob": 1, "blob": 2, "mediumblob": 3, "longblob": 4,
}

// CompareColumnTypes classifies a MODIFY/CHANGE type conversion. It never
// returns INSTANT when the old type is unknown.
func CompareColumnTypes(old, new model.ColumnType) TypeChange {
	if old.Base == "" {
		return TypeChange{
			Algorithm: model.AlgorithmUnknown,
			Risk:      model.RiskMedium,
			RuleID:    "TYPECHG-03",
			Note:      "old column type is unknown; type compatibility requires manual confirmation",
		}
	}
	oldBase := normalizeTypeBase(old.Base)
	newBase := normalizeTypeBase(new.Base)
	if isEnumSet(oldBase) && isEnumSet(newBase) {
		return compareEnumSet(old, new, oldBase)
	}
	if oldBase == newBase {
		return compareSameBase(old, new, oldBase)
	}
	if integerRank[oldBase] > 0 && integerRank[newBase] > 0 {
		if old.Unsigned != new.Unsigned {
			return TypeChange{Algorithm: model.AlgorithmCopy, Risk: model.RiskHigh, RuleID: "TYPECHG-05", Note: "changing SIGNED/UNSIGNED rewrites the table"}
		}
		if integerRank[newBase] > integerRank[oldBase] {
			return TypeChange{Algorithm: model.AlgorithmInplace, Risk: model.RiskMedium, RuleID: "TYPECHG-01", Note: "integer type widened; MySQL can rebuild in place, but test on staging"}
		}
		return TypeChange{Algorithm: model.AlgorithmCopy, Risk: model.RiskHigh, RuleID: "TYPECHG-02", Note: "narrowing an integer type rewrites the table"}
	}
	if textRank[oldBase] > 0 && textRank[newBase] > 0 {
		return TypeChange{Algorithm: model.AlgorithmUnknown, Risk: model.RiskMedium, RuleID: "TYPECHG-03", Note: "changing between TEXT/BLOB sizes requires manual confirmation"}
	}
	return TypeChange{
		Algorithm: model.AlgorithmCopy,
		Risk:      model.RiskHigh,
		RuleID:    "TYPECHG-02",
		Note:      fmt.Sprintf("changing %s to %s is not an in-place conversion", oldBase, newBase),
	}
}

func compareSameBase(old, new model.ColumnType, base string) TypeChange {
	if old.Charset != "" && new.Charset != "" && !strings.EqualFold(old.Charset, new.Charset) {
		return TypeChange{Algorithm: model.AlgorithmCopy, Risk: model.RiskHigh, RuleID: "TYPECHG-02", Note: "changing a column character set rewrites the table"}
	}
	switch {
	case integerRank[base] > 0:
		if old.Unsigned != new.Unsigned {
			return TypeChange{Algorithm: model.AlgorithmCopy, Risk: model.RiskHigh, RuleID: "TYPECHG-05", Note: "changing SIGNED/UNSIGNED rewrites the table"}
		}
		return TypeChange{Algorithm: model.AlgorithmInstant, Risk: model.RiskLow, RuleID: "TYPECHG-01", Note: "same integer type and signedness"}
	case base == "char" || base == "varchar" || base == "binary" || base == "varbinary":
		oldLen, newLen := intValue(old.Length), intValue(new.Length)
		if newLen > oldLen {
			return TypeChange{Algorithm: model.AlgorithmInplace, Risk: model.RiskMedium, RuleID: "TYPECHG-01", Note: "length increase can be done in place"}
		}
		if newLen < oldLen {
			return TypeChange{Algorithm: model.AlgorithmCopy, Risk: model.RiskHigh, RuleID: "TYPECHG-02", Note: "length decrease rewrites the table"}
		}
		return TypeChange{Algorithm: model.AlgorithmInstant, Risk: model.RiskLow, RuleID: "TYPECHG-01", Note: "same string type and length"}
	case base == "decimal" || base == "numeric":
		oldP, oldS := intValue(old.Length), intValue(old.Scale)
		newP, newS := intValue(new.Length), intValue(new.Scale)
		if newP >= oldP && newS >= oldS {
			return TypeChange{Algorithm: model.AlgorithmInplace, Risk: model.RiskMedium, RuleID: "TYPECHG-01", Note: "precision/scale increase can be done in place"}
		}
		return TypeChange{Algorithm: model.AlgorithmCopy, Risk: model.RiskHigh, RuleID: "TYPECHG-02", Note: "reducing decimal precision rewrites the table"}
	case textRank[base] > 0 || blobRank[base] > 0:
		return TypeChange{Algorithm: model.AlgorithmInstant, Risk: model.RiskLow, RuleID: "TYPECHG-01", Note: "same TEXT/BLOB type"}
	case base == "json" || base == "bit" || base == "year":
		return TypeChange{Algorithm: model.AlgorithmInstant, Risk: model.RiskLow, RuleID: "TYPECHG-01", Note: "same type"}
	default:
		return TypeChange{Algorithm: model.AlgorithmUnknown, Risk: model.RiskMedium, RuleID: "TYPECHG-03", Note: "type pair is not covered by the compatibility table; confirm on staging"}
	}
}

func compareEnumSet(old, new model.ColumnType, base string) TypeChange {
	if !isPrefix(old.EnumValues, new.EnumValues) {
		return TypeChange{Algorithm: model.AlgorithmCopy, Risk: model.RiskHigh, RuleID: "TYPECHG-04", Note: "removing or reordering ENUM/SET values changes stored value interpretation"}
	}
	if len(old.EnumValues) == len(new.EnumValues) {
		return TypeChange{Algorithm: model.AlgorithmInstant, Risk: model.RiskLow, RuleID: "TYPECHG-01", Note: "same " + strings.ToUpper(base) + " values"}
	}
	return TypeChange{Algorithm: model.AlgorithmInplace, Risk: model.RiskMedium, RuleID: "TYPECHG-01", Note: "appending " + strings.ToUpper(base) + " values can be done in place; verify old values"}
}

func isPrefix(old, new []string) bool {
	if len(old) > len(new) {
		return false
	}
	for i := range old {
		if old[i] != new[i] {
			return false
		}
	}
	return true
}

func isEnumSet(base string) bool { return base == "enum" || base == "set" }

func normalizeTypeBase(base string) string {
	base = strings.ToLower(strings.TrimSpace(base))
	if base == "integer" {
		return "int"
	}
	return base
}

func intValue(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// CatalogType parses a type string from information_schema, falling back to a
// conservative raw type when parsing fails.
func CatalogType(raw string) model.ColumnType {
	t, err := parser.ParseTypeString(raw)
	if err == nil {
		return t
	}
	return model.ColumnType{Raw: raw, Base: strings.ToLower(strings.TrimSpace(raw))}
}

// CompareNullability returns a rule when a MODIFY changes nullability.
func CompareNullability(oldNullable *bool, newNullable *bool) (model.Algorithm, model.Risk, string, string) {
	if oldNullable == nil || newNullable == nil || *oldNullable == *newNullable {
		return "", "", "", ""
	}
	if !*newNullable {
		return model.AlgorithmInplace, model.RiskMedium, "NULL-01",
			"making a column NOT NULL validates every row; existing NULLs will fail the migration"
	}
	return model.AlgorithmInstant, model.RiskLow, "NULL-02", "relaxing NOT NULL is a metadata change"
}
