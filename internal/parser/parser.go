// Package parser turns migration SQL into a small, database-independent
// statement model. It uses the Vitess MySQL parser and deliberately supports
// only the DDL forms that mig-est can diagnose.
package parser

import (
	"fmt"
	"regexp"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/eterny13/mig-est/internal/model"
)

// Option is one ALTER TABLE option extracted from the AST.
type Option struct {
	Kind             model.OperationKind
	Column           *model.Column
	OldColumn        string
	Index            *model.Index
	ForeignKey       *model.ForeignKey
	TableOptionName  string
	TableOptionValue string
	PartitionAction  string
	Note             string
}

// CreateTable captures the parts of CREATE TABLE that are useful for checks.
type CreateTable struct {
	HasPrimaryKey bool
	HasUniqueKey  bool
	HasSelect     bool
	Columns       []model.Column
	Indexes       []model.Index
	ForeignKeys   []model.ForeignKey
}

// Statement is a parsed SQL statement ready for classification.
type Statement struct {
	Kind              model.StatementKind
	Raw               string
	Summary           string
	File              string
	Line              int
	Table             string
	Schema            string
	Tables            []string
	Options           []Option
	Algorithm         string
	Lock              string
	CreateTable       *CreateTable
	Unsupported       bool
	UnsupportedReason string
}

var unsupportedRE = regexp.MustCompile(`(?is)^CREATE\s+(?:OR\s+REPLACE\s+)?(?:DEFINER\s*=\s*[^\s]+\s+)?(?:TRIGGER|PROCEDURE|FUNCTION|EVENT)\b|^DROP\s+(?:TRIGGER|PROCEDURE|FUNCTION|EVENT)\b|^ALTER\s+(?:TRIGGER|PROCEDURE|FUNCTION|EVENT)\b`)

var createIndexRE = regexp.MustCompile(`(?is)^CREATE\s+(?:(?:UNIQUE|FULLTEXT|SPATIAL)\s+)?INDEX\b`)
var dropIndexRE = regexp.MustCompile(`(?is)^DROP\s+INDEX\b`)

// ParseSQL splits input on statement boundaries and parses each piece.
func ParseSQL(file, input string) ([]*Statement, error) {
	pieces := SplitSQL(input)
	out := make([]*Statement, 0, len(pieces))
	for _, p := range pieces {
		stmt, err := parseOne(file, p)
		if err != nil {
			return nil, err
		}
		out = append(out, stmt)
	}
	return out, nil
}

func parseOne(file string, piece Piece) (*Statement, error) {
	raw := strings.TrimSpace(piece.SQL)
	if raw == "" {
		return nil, nil
	}
	stmt := &Statement{
		Raw:     raw,
		Summary: summarize(raw, 260),
		File:    file,
		Line:    piece.Line,
	}
	code := strings.TrimSpace(stripComments(raw))
	upper := strings.ToUpper(code)
	if strings.HasPrefix(upper, "DELIMITER") || unsupportedRE.MatchString(code) {
		stmt.Kind = model.KindUnsupported
		stmt.Unsupported = true
		stmt.UnsupportedReason = "stored programs, triggers, events, and DELIMITER blocks are not analyzed in v0.1"
		return stmt, nil
	}

	parsed, err := sqlparser.NewTestParser().Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s:%d: cannot parse SQL: %w", file, piece.Line, err)
	}

	switch v := parsed.(type) {
	case *sqlparser.AlterTable:
		stmt.Table = v.Table.Name.String()
		stmt.Schema = v.Table.Qualifier.String()
		stmt.Tables = []string{stmt.Table}
		switch {
		case createIndexRE.MatchString(code):
			stmt.Kind = model.KindCreateIndex
		case dropIndexRE.MatchString(code):
			stmt.Kind = model.KindDropIndex
		default:
			stmt.Kind = model.KindAlterTable
		}
		if err := extractAlter(stmt, v); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", file, piece.Line, err)
		}
	case *sqlparser.CreateTable:
		stmt.Kind = model.KindCreateTable
		stmt.Table = v.Table.Name.String()
		stmt.Schema = v.Table.Qualifier.String()
		stmt.Tables = []string{stmt.Table}
		stmt.CreateTable = extractCreateTable(v)
	case *sqlparser.DropTable:
		stmt.Kind = model.KindDropTable
		for _, t := range v.FromTables {
			stmt.Tables = append(stmt.Tables, t.Name.String())
		}
		if len(stmt.Tables) > 0 {
			stmt.Table = stmt.Tables[0]
		}
	case *sqlparser.RenameTable:
		stmt.Kind = model.KindRenameTable
		for _, pair := range v.TablePairs {
			stmt.Tables = append(stmt.Tables, pair.FromTable.Name.String())
		}
		if len(stmt.Tables) > 0 {
			stmt.Table = stmt.Tables[0]
		}
	default:
		stmt.Kind = model.KindOther
	}
	return stmt, nil
}

func extractAlter(stmt *Statement, v *sqlparser.AlterTable) error {
	for _, opt := range v.AlterOptions {
		switch o := opt.(type) {
		case sqlparser.AlgorithmValue:
			stmt.Algorithm = strings.ToUpper(string(o))
		case *sqlparser.LockOption:
			stmt.Lock = strings.ToUpper(o.Type.ToString())
		case *sqlparser.AddColumns:
			for _, col := range o.Columns {
				c := columnFromAST(col)
				c.Position = position(o.First, o.After)
				stmt.Options = append(stmt.Options, Option{Kind: model.OpAddColumn, Column: &c})
			}
		case *sqlparser.DropColumn:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpDropColumn, OldColumn: o.Name.Name.String()})
		case *sqlparser.ModifyColumn:
			c := columnFromAST(o.NewColDefinition)
			c.Position = position(o.First, o.After)
			stmt.Options = append(stmt.Options, Option{Kind: model.OpModifyColumn, Column: &c, OldColumn: o.NewColDefinition.Name.String()})
		case *sqlparser.ChangeColumn:
			c := columnFromAST(o.NewColDefinition)
			c.Position = position(o.First, o.After)
			stmt.Options = append(stmt.Options, Option{Kind: model.OpChangeColumn, Column: &c, OldColumn: o.OldColumn.Name.String()})
		case *sqlparser.RenameColumn:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpRenameColumn, OldColumn: o.OldName.Name.String(), Column: &model.Column{Name: o.NewName.Name.String()}})
		case *sqlparser.AddIndexDefinition:
			idx := indexFromAST(o.IndexDefinition)
			kind := model.OpAddIndex
			if idx.Kind == "PRIMARY" {
				kind = model.OpAddPrimaryKey
			}
			stmt.Options = append(stmt.Options, Option{Kind: kind, Index: &idx})
		case *sqlparser.DropKey:
			kind := model.OpDropIndex
			switch o.Type {
			case sqlparser.PrimaryKeyType:
				kind = model.OpDropPrimaryKey
			case sqlparser.ForeignKeyType:
				kind = model.OpDropForeignKey
			case sqlparser.CheckKeyType:
				kind = model.OpDropCheck
			}
			stmt.Options = append(stmt.Options, Option{Kind: kind, Note: o.Name.String()})
		case *sqlparser.AddConstraintDefinition:
			switch d := o.ConstraintDefinition.Details.(type) {
			case *sqlparser.ForeignKeyDefinition:
				fk := &model.ForeignKey{
					Name:    o.ConstraintDefinition.Name.String(),
					Columns: columnsFromAST(d.Source),
				}
				if d.ReferenceDefinition != nil {
					fk.ReferencedTable = d.ReferenceDefinition.ReferencedTable.Name.String()
					fk.ReferencedColumns = columnsFromAST(d.ReferenceDefinition.ReferencedColumns)
				}
				stmt.Options = append(stmt.Options, Option{Kind: model.OpAddForeignKey, ForeignKey: fk})
			case *sqlparser.CheckConstraintDefinition:
				stmt.Options = append(stmt.Options, Option{Kind: model.OpAddCheck, Note: o.ConstraintDefinition.Name.String()})
			default:
				stmt.Options = append(stmt.Options, Option{Kind: model.OpOther, Note: "constraint"})
			}
		case *sqlparser.AlterColumn:
			kind := model.OpOther
			switch {
			case o.DropDefault:
				kind = model.OpDropDefault
			case o.DefaultVal != nil:
				kind = model.OpSetDefault
			case o.Invisible != nil:
				kind = model.OpSetVisibility
			}
			stmt.Options = append(stmt.Options, Option{Kind: kind, Column: &model.Column{Name: o.Column.Name.String()}})
		case *sqlparser.AlterIndex:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpSetVisibility, Note: o.Name.String()})
		case *sqlparser.RenameIndex:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpRenameIndex, OldColumn: o.OldName.String(), Note: o.NewName.String()})
		case *sqlparser.AlterCharset:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpConvertCharset, Note: o.CharacterSet + " " + o.Collate})
		case sqlparser.TableOptions:
			for _, to := range o {
				name := strings.ToUpper(to.Name)
				value := to.String
				if value == "" && to.Value != nil {
					value = to.Value.Val
				}
				switch name {
				case "ENGINE":
					stmt.Options = append(stmt.Options, Option{Kind: model.OpSetEngine, TableOptionName: name, TableOptionValue: value})
				case "ROW_FORMAT":
					stmt.Options = append(stmt.Options, Option{Kind: model.OpSetRowFormat, TableOptionName: name, TableOptionValue: value})
				case "CHARSET", "CHARACTER SET":
					stmt.Options = append(stmt.Options, Option{Kind: model.OpSetTableCharset, TableOptionName: "CHARSET", TableOptionValue: value})
				default:
					stmt.Options = append(stmt.Options, Option{Kind: model.OpSetTableOption, TableOptionName: name, TableOptionValue: value})
				}
			}
		case *sqlparser.RenameTableName:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpRenameTable, Note: o.Table.Name.String()})
		case *sqlparser.Force:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpForce})
		case *sqlparser.OrderByOption:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpOrderBy})
		case *sqlparser.KeyState:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpKeyState})
		case *sqlparser.TablespaceOperation:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpTablespace})
		case *sqlparser.Validation:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpOther, Note: "validation"})
		default:
			stmt.Options = append(stmt.Options, Option{Kind: model.OpOther, Note: fmt.Sprintf("%T", opt)})
		}
	}
	if v.PartitionSpec != nil || v.PartitionOption != nil {
		stmt.Options = append(stmt.Options, Option{Kind: model.OpPartition})
	}
	if len(stmt.Options) == 0 {
		stmt.Options = append(stmt.Options, Option{Kind: model.OpOther})
	}
	return nil
}

func position(first bool, after *sqlparser.ColName) string {
	if first {
		return "FIRST"
	}
	if after != nil {
		return "AFTER " + after.Name.String()
	}
	return ""
}

func columnFromAST(c *sqlparser.ColumnDefinition) model.Column {
	col := model.Column{Name: c.Name.String()}
	if c.Type != nil {
		col.Type = ColumnTypeFromAST(c.Type)
		if c.Type.Options != nil {
			col.Nullable = c.Type.Options.Null
			col.AutoIncrement = c.Type.Options.Autoincrement
			if c.Type.Options.Default != nil {
				col.HasDefault = true
				col.Default = sqlparser.String(c.Type.Options.Default)
			}
		}
	}
	return col
}

func indexFromAST(idx *sqlparser.IndexDefinition) model.Index {
	out := model.Index{Columns: indexColumns(idx.Columns)}
	if idx.Info != nil {
		out.Name = idx.Info.Name.String()
		switch idx.Info.Type {
		case sqlparser.IndexTypePrimary:
			out.Kind = "PRIMARY"
		case sqlparser.IndexTypeUnique:
			out.Kind = "UNIQUE"
		case sqlparser.IndexTypeFullText:
			out.Kind = "FULLTEXT"
		case sqlparser.IndexTypeSpatial:
			out.Kind = "SPATIAL"
		default:
			out.Kind = "INDEX"
		}
	}
	if out.Kind == "" {
		out.Kind = "INDEX"
	}
	return out
}

func indexColumns(cols []*sqlparser.IndexColumn) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		if c.Column.String() != "" {
			out = append(out, c.Column.String())
		} else if c.Expression != nil {
			out = append(out, sqlparser.String(c.Expression))
		}
	}
	return out
}

func columnsFromAST(cols sqlparser.Columns) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.String())
	}
	return out
}

func extractCreateTable(v *sqlparser.CreateTable) *CreateTable {
	ct := &CreateTable{HasSelect: v.Select != nil}
	if v.TableSpec == nil {
		return ct
	}
	for _, c := range v.TableSpec.Columns {
		col := columnFromAST(c)
		ct.Columns = append(ct.Columns, col)
		if c.Type != nil && c.Type.Options != nil && c.Type.Options.KeyOpt == sqlparser.ColKeyPrimary {
			ct.HasPrimaryKey = true
		}
		if c.Type != nil && c.Type.Options != nil {
			switch c.Type.Options.KeyOpt {
			case sqlparser.ColKeyUnique, sqlparser.ColKeyUniqueKey:
				ct.HasUniqueKey = true
			}
		}
	}
	for _, idx := range v.TableSpec.Indexes {
		ix := indexFromAST(idx)
		ct.Indexes = append(ct.Indexes, ix)
		switch ix.Kind {
		case "PRIMARY":
			ct.HasPrimaryKey = true
		case "UNIQUE":
			ct.HasUniqueKey = true
		}
	}
	for _, c := range v.TableSpec.Constraints {
		if fk, ok := c.Details.(*sqlparser.ForeignKeyDefinition); ok {
			out := model.ForeignKey{Name: c.Name.String(), Columns: columnsFromAST(fk.Source)}
			if fk.ReferenceDefinition != nil {
				out.ReferencedTable = fk.ReferenceDefinition.ReferencedTable.Name.String()
				out.ReferencedColumns = columnsFromAST(fk.ReferenceDefinition.ReferencedColumns)
			}
			ct.ForeignKeys = append(ct.ForeignKeys, out)
		}
	}
	return ct
}

// ColumnTypeFromAST converts a Vitess column type into the shared model.
func ColumnTypeFromAST(ct *sqlparser.ColumnType) model.ColumnType {
	out := model.ColumnType{
		Raw:        sqlparser.String(ct),
		Base:       strings.ToLower(ct.Type),
		Length:     ct.Length,
		Scale:      ct.Scale,
		Unsigned:   ct.Unsigned,
		Zerofill:   ct.Zerofill,
		Charset:    ct.Charset.Name,
		EnumValues: ct.EnumValues,
	}
	return out
}

// ParseTypeString parses a MySQL type as returned by
// information_schema.COLUMNS.COLUMN_TYPE, for example "varchar(50)" or
// "int unsigned".
func ParseTypeString(raw string) (model.ColumnType, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return model.ColumnType{}, fmt.Errorf("empty column type")
	}
	stmt, err := sqlparser.NewTestParser().Parse("CREATE TABLE __migest_type (c " + raw + ")")
	if err != nil {
		return model.ColumnType{Base: strings.ToLower(raw), Raw: raw}, err
	}
	ct, ok := stmt.(*sqlparser.CreateTable)
	if !ok || ct.TableSpec == nil || len(ct.TableSpec.Columns) != 1 {
		return model.ColumnType{Base: strings.ToLower(raw), Raw: raw}, fmt.Errorf("not a single column type")
	}
	return ColumnTypeFromAST(ct.TableSpec.Columns[0].Type), nil
}
