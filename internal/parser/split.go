package parser

import (
	"strings"
)

// Piece is one SQL statement and the 1-based line where its first code token
// appears.
type Piece struct {
	SQL  string
	Line int
}

// SplitSQL splits a migration file on the active delimiter while respecting
// quoted strings, backtick identifiers, MySQL line/block comments, and mysql
// client DELIMITER directives. A DELIMITER block is kept as one piece so the
// parser can report stored programs as unsupported rather than failing on
// their internal semicolons.
func SplitSQL(input string) []Piece {
	var pieces []Piece
	start := 0
	state := scanNormal
	line := 1
	delimiter := ";"
	i := 0
	for i < len(input) {
		c := input[i]
		switch state {
		case scanNormal:
			if newDelimiter, end, ok := delimiterDirective(input, i); ok {
				pieces = appendPiece(pieces, input, start, i, line)
				i = end
				start = i
				delimiter = newDelimiter
				continue
			}
			switch {
			case c == '\'':
				state = scanSingle
				i++
			case c == '"':
				state = scanDouble
				i++
			case c == '`':
				state = scanBacktick
				i++
			case c == '-' && i+1 < len(input) && input[i+1] == '-' && (i+2 >= len(input) || isSpace(input[i+2])):
				state = scanLineComment
				i += 2
			case c == '#':
				state = scanLineComment
				i++
			case c == '/' && i+1 < len(input) && input[i+1] == '*':
				state = scanBlockComment
				i += 2
			default:
				if delimiter != "" && strings.HasPrefix(input[i:], delimiter) {
					pieces = appendPiece(pieces, input, start, i, line)
					i += len(delimiter)
					start = i
					continue
				}
				if c == '\n' {
					line++
				}
				i++
			}
		case scanSingle:
			switch {
			case c == '\\' && i+1 < len(input):
				if input[i+1] == '\n' {
					line++
				}
				i += 2
			case c == '\'' && i+1 < len(input) && input[i+1] == '\'':
				i += 2
			case c == '\'':
				state = scanNormal
				i++
			default:
				if c == '\n' {
					line++
				}
				i++
			}
		case scanDouble:
			switch {
			case c == '\\' && i+1 < len(input):
				if input[i+1] == '\n' {
					line++
				}
				i += 2
			case c == '"' && i+1 < len(input) && input[i+1] == '"':
				i += 2
			case c == '"':
				state = scanNormal
				i++
			default:
				if c == '\n' {
					line++
				}
				i++
			}
		case scanBacktick:
			if c == '`' && i+1 < len(input) && input[i+1] == '`' {
				i += 2
			} else if c == '`' {
				state = scanNormal
				i++
			} else {
				if c == '\n' {
					line++
				}
				i++
			}
		case scanLineComment:
			if c == '\n' {
				line++
				state = scanNormal
			}
			i++
		case scanBlockComment:
			if c == '*' && i+1 < len(input) && input[i+1] == '/' {
				state = scanNormal
				i += 2
			} else {
				if c == '\n' {
					line++
				}
				i++
			}
		}
	}
	pieces = appendPiece(pieces, input, start, len(input), line)
	return pieces
}

type scanState int

const (
	scanNormal scanState = iota
	scanSingle
	scanDouble
	scanBacktick
	scanLineComment
	scanBlockComment
)

func appendPiece(pieces []Piece, input string, start, end, line int) []Piece {
	if start >= end {
		return pieces
	}
	raw := input[start:end]
	if strings.TrimSpace(raw) == "" {
		return pieces
	}
	off := firstCodeOffset(raw)
	if off >= len(raw) {
		return pieces
	}
	// Recompute the starting line from the absolute offset of the first code
	// token so leading comments do not shift the reported line.
	abs := start + off
	line = 1 + strings.Count(input[:abs], "\n")
	return append(pieces, Piece{SQL: strings.TrimSpace(raw), Line: line})
}

func firstCodeOffset(s string) int {
	i := 0
	for i < len(s) {
		switch {
		case isSpace(s[i]):
			i++
		case s[i] == '-' && i+1 < len(s) && s[i+1] == '-' && (i+2 >= len(s) || isSpace(s[i+2])):
			i += 2
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case s[i] == '#':
			i++
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case s[i] == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			}
		default:
			return i
		}
	}
	return i
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}

// summarize strips comments, collapses whitespace, and truncates long SQL.
func summarize(raw string, max int) string {
	s := stripComments(raw)
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		s = strings.Join(strings.Fields(raw), " ")
	}
	if len(s) > max {
		if max < 4 {
			return s[:max]
		}
		return s[:max-3] + "..."
	}
	return s
}

// stripComments removes --, #, and /* */ comments while preserving quoted
// literals. It replaces comments with a space so tokens cannot run together.
func stripComments(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	state := scanNormal
	i := 0
	for i < len(s) {
		c := s[i]
		switch state {
		case scanNormal:
			switch {
			case c == '\'':
				state = scanSingle
				b.WriteByte(c)
				i++
			case c == '"':
				state = scanDouble
				b.WriteByte(c)
				i++
			case c == '`':
				state = scanBacktick
				b.WriteByte(c)
				i++
			case c == '-' && i+1 < len(s) && s[i+1] == '-' && (i+2 >= len(s) || isSpace(s[i+2])):
				state = scanLineComment
				b.WriteByte(' ')
				i += 2
			case c == '#':
				state = scanLineComment
				b.WriteByte(' ')
				i++
			case c == '/' && i+1 < len(s) && s[i+1] == '*':
				state = scanBlockComment
				b.WriteByte(' ')
				i += 2
			default:
				b.WriteByte(c)
				i++
			}
		case scanSingle:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				b.WriteByte(s[i+1])
				i += 2
			} else if c == '\'' && i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte(s[i+1])
				i += 2
			} else {
				if c == '\'' {
					state = scanNormal
				}
				i++
			}
		case scanDouble:
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				b.WriteByte(s[i+1])
				i += 2
			} else if c == '"' && i+1 < len(s) && s[i+1] == '"' {
				b.WriteByte(s[i+1])
				i += 2
			} else {
				if c == '"' {
					state = scanNormal
				}
				i++
			}
		case scanBacktick:
			b.WriteByte(c)
			if c == '`' && i+1 < len(s) && s[i+1] == '`' {
				b.WriteByte(s[i+1])
				i += 2
			} else {
				if c == '`' {
					state = scanNormal
				}
				i++
			}
		case scanLineComment:
			if c == '\n' {
				state = scanNormal
				b.WriteByte('\n')
			}
			i++
		case scanBlockComment:
			if c == '*' && i+1 < len(s) && s[i+1] == '/' {
				state = scanNormal
				i += 2
			} else {
				i++
			}
		}
	}
	return b.String()
}

// delimiterDirective recognizes a mysql client DELIMITER directive at the
// beginning of a line. It returns the new delimiter and the offset just after
// the directive line. DELIMITER is not SQL sent to the server, so it is not
// included in any Piece.
func delimiterDirective(input string, i int) (string, int, bool) {
	if i > 0 && input[i-1] != '\n' && input[i-1] != ' ' && input[i-1] != '\t' {
		return "", 0, false
	}
	lineStart := strings.LastIndexByte(input[:i], '\n') + 1
	if strings.TrimSpace(input[lineStart:i]) != "" {
		return "", 0, false
	}
	const kw = "DELIMITER"
	if len(input)-i < len(kw) || !strings.EqualFold(input[i:i+len(kw)], kw) {
		return "", 0, false
	}
	j := i + len(kw)
	if j >= len(input) || (input[j] != ' ' && input[j] != '\t') {
		return "", 0, false
	}
	for j < len(input) && (input[j] == ' ' || input[j] == '\t') {
		j++
	}
	k := j
	for k < len(input) && !isSpace(input[k]) {
		k++
	}
	if k == j {
		return "", 0, false
	}
	newDelimiter := input[j:k]
	for k < len(input) && input[k] != '\n' {
		if !isSpace(input[k]) {
			return "", 0, false
		}
		k++
	}
	return newDelimiter, k, true
}
