package sqltext

import (
	"strconv"

	"github.com/farvisun/squi/internal/db"
)

// Placeholders: :name on every engine, $1 on Postgres, ? on MySQL and SQLite,
// and ?1, @name, $name on SQLite. Each bare ? is its own parameter, named ?1,
// ?2… by position; the same :name or $1 used twice is one parameter.
//
// A :name right after a value (a number, an identifier, ] or ) with no space
// in between) is not a parameter: that is an array slice like a[1:n] or a
// ClickHouse {id:UInt32} parameter.

type param struct {
	name       string
	start, end int
}

func placeholders(sql string, d db.DriverName) []param {
	toks := Tokenize(sql, d)
	var out []param
	bare := 0
	for i, t := range toks {
		if t.Kind != TokParam {
			continue
		}
		name := t.Text
		switch {
		case name == "?":
			bare++
			name = "?" + strconv.Itoa(bare)
		case name[0] == ':' && i > 0 && adjacentValue(toks[i-1], t):
			continue
		}
		out = append(out, param{name: name, start: t.Start, end: t.End})
	}
	return out
}

func adjacentValue(prev, t Token) bool {
	if prev.End != t.Start {
		return false
	}
	switch prev.Kind {
	case TokNumber, TokIdent, TokKeyword, TokQuotedIdent:
		return true
	case TokPunct:
		return prev.Text == "]" || prev.Text == ")"
	}
	return false
}

// Params returns the distinct placeholders of sql in order of first use.
func Params(sql string, d db.DriverName) []string {
	var names []string
	seen := map[string]bool{}
	for _, p := range placeholders(sql, d) {
		if !seen[p.name] {
			seen[p.name] = true
			names = append(names, p.name)
		}
	}
	return names
}

// BindParams replaces each placeholder of sql with its value, inserted as
// written (values are SQL literals: 42, 'text', NULL). Placeholders without a
// value are left alone.
func BindParams(sql string, d db.DriverName, values map[string]string) string {
	ps := placeholders(sql, d)
	out := sql
	for i := len(ps) - 1; i >= 0; i-- {
		p := ps[i]
		if v, ok := values[p.name]; ok {
			out = out[:p.start] + v + out[p.end:]
		}
	}
	return out
}
