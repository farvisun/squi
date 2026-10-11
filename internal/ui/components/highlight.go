package components

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"

	"github.com/farvisun/squi/internal/ui/theme"
)

// Syntax style ids. 0 is plain text.
const (
	synPlain uint8 = iota
	synKeyword
	synString
	synNumber
	synComment
	synFunction
	synOperator
	synType
	synCount
)

var synStyles = [synCount]lipgloss.Style{
	theme.SynIdent, theme.SynKeyword, theme.SynString, theme.SynNumber,
	theme.SynComment, theme.SynFunction, theme.SynOperator, theme.SynType,
}

// LexerFor returns the chroma lexer name for a driver name.
func LexerFor(driver string) string {
	if driver == "mysql" || driver == "clickhouse" {
		return "mysql" // backtick identifiers, # comments
	}
	if driver == "sqlserver" {
		return "tsql"
	}
	if driver == "sqlite" {
		return "sql" // chroma has no SQLite lexer
	}
	return "postgresql"
}

// highlightLines returns one style id per rune for every line.
func highlightLines(lines [][]rune, lexer string) [][]uint8 {
	out := make([][]uint8, len(lines))
	for i, l := range lines {
		out[i] = make([]uint8, len(l))
	}
	lx := lexers.Get(lexer)
	if lx == nil {
		lx = lexers.Get("sql")
	}
	if lx == nil {
		return out
	}
	var sb strings.Builder
	for i, l := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(string(l))
	}
	it, err := chroma.Coalesce(lx).Tokenise(nil, sb.String())
	if err != nil {
		return out
	}
	row, col := 0, 0
	for tok := it(); tok != chroma.EOF; tok = it() {
		id := synID(tok.Type)
		for _, r := range tok.Value {
			if r == '\n' {
				row++
				col = 0
				continue
			}
			if row < len(out) && col < len(out[row]) {
				out[row][col] = id
			}
			col++
		}
	}
	return out
}

func synID(t chroma.TokenType) uint8 {
	switch {
	case t == chroma.KeywordType || t == chroma.NameBuiltin:
		return synType
	case t.InCategory(chroma.Keyword):
		return synKeyword
	case t.InCategory(chroma.LiteralString):
		return synString
	case t.InCategory(chroma.LiteralNumber):
		return synNumber
	case t.InCategory(chroma.Comment):
		return synComment
	case t == chroma.NameFunction:
		return synFunction
	case t.InCategory(chroma.Operator):
		return synOperator
	}
	return synPlain
}

// HighlightSQL renders SQL text with syntax colors (for read-only views).
func HighlightSQL(src, lexer string) []string {
	raw := strings.Split(src, "\n")
	lines := make([][]rune, len(raw))
	for i, l := range raw {
		lines[i] = []rune(strings.ReplaceAll(l, "\t", "    "))
	}
	hl := highlightLines(lines, lexer)
	out := make([]string, len(lines))
	for i, l := range lines {
		var sb strings.Builder
		start := 0
		for j := 1; j <= len(l); j++ {
			if j == len(l) || hl[i][j] != hl[i][start] {
				sb.WriteString(synStyles[hl[i][start]].Render(string(l[start:j])))
				start = j
			}
		}
		out[i] = sb.String()
	}
	return out
}
