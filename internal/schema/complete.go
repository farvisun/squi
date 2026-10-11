package schema

import (
	"sort"
	"strings"
	"unicode"

	"github.com/sahilm/fuzzy"

	"github.com/farvisun/squi/internal/db"
	"github.com/farvisun/squi/internal/sqltext"
)

// ItemKind is the kind of a completion item.
type ItemKind string

const (
	ItemKeyword  ItemKind = "keyword"
	ItemTable    ItemKind = "table"
	ItemView     ItemKind = "view"
	ItemColumn   ItemKind = "column"
	ItemFunction ItemKind = "function"
	ItemSchema   ItemKind = "schema"
	ItemDatabase ItemKind = "database"
	ItemAlias    ItemKind = "alias"
)

// Item is one completion candidate.
type Item struct {
	Label  string   // display text
	Insert string   // replacement text (quoted where needed)
	Kind   ItemKind //
	Detail string   // e.g. the table of a column or the schema of a table
}

// Completion is the result of Complete: replace src[Start:End] with the
// chosen Item.Insert.
type Completion struct {
	Start, End int
	Prefix     string // identifier text typed so far, without an opening quote
	Items      []Item
}

// Context is the session state completion depends on.
type Context struct {
	Database   string
	SearchPath []string // Postgres: e.g. ["public"]; MySQL: [current database]
}

const maxItems = 50

// completer holds the state of one Complete call.
type completer struct {
	c       *Cache
	ctx     Context
	d       db.Dialect
	quoted  bool // the user opened a quoted identifier
	parenAt bool // '(' follows the cursor
	refs    []tableRef
	kwCase  func(string) string
	items   []Item
}

// Complete returns completion candidates for the cursor position in src,
// based on the statement around the cursor and the cached metadata.
func Complete(src string, cursor int, c *Cache, ctx Context) Completion {
	d := c.driver
	cursor = max(0, min(cursor, len(src)))
	comp := Completion{Start: cursor, End: cursor}
	seg := statementTokens(sqltext.Tokenize(src, d), cursor)

	var cur *sqltext.Token
	for i := range seg {
		if seg[i].Start < cursor && cursor <= seg[i].End {
			cur = &seg[i]
			break
		}
	}
	cp := &completer{c: c, ctx: ctx, d: db.Dialect{Driver: d}}
	if cur != nil {
		switch cur.Kind {
		case sqltext.TokComment, sqltext.TokString:
			if cursor < cur.End || unterminated(*cur, d) {
				return comp
			}
		case sqltext.TokNumber, sqltext.TokParam:
			return comp
		case sqltext.TokIdent, sqltext.TokKeyword:
			if strings.HasPrefix(cur.Text, "@") {
				return comp
			}
			comp.Start, comp.Prefix = cur.Start, src[cur.Start:cursor]
		case sqltext.TokQuotedIdent:
			comp.Start, cp.quoted = cur.Start, true
			comp.Prefix = quotedPrefix(src[cur.Start:cursor], cursor == cur.End && !unterminated(*cur, d))
		}
	}
	cp.parenAt = strings.HasPrefix(strings.TrimLeft(src[cursor:], " \t"), "(")

	// before: significant tokens preceding the completed word; all: every
	// significant token of the statement except that word.
	var before, all []sqltext.Token
	for _, t := range seg {
		if t.Trivia() || t.Kind == sqltext.TokDelimiter || (comp.Start < cursor && t.Start == comp.Start) {
			continue
		}
		if t.End <= comp.Start {
			before = append(before, t)
		}
		all = append(all, t)
	}
	cp.refs = parseRefs(all)
	cp.kwCase = keywordCase(comp.Prefix, before)

	switch n := len(before); {
	case n > 0 && before[n-1].Text == ".":
		cp.member(before)
	case cp.isTableContext(before):
		cp.tables()
	case n > 0 && before[n-1].Upper() == "USE" && d == db.MySQL:
		cp.databases()
	case n > 0 && cp.isExprContext(before[n-1]):
		cp.columns()
		cp.aliases()
		cp.functions()
		cp.keywords()
	default:
		cp.keywords()
	}
	comp.Items = rank(cp.items, comp.Prefix)
	return comp
}

// statementTokens returns the tokens between the statement delimiters
// surrounding cursor.
func statementTokens(toks []sqltext.Token, cursor int) []sqltext.Token {
	start, end := 0, len(toks)
	for i, t := range toks {
		if t.Kind != sqltext.TokDelimiter && !(t.Kind == sqltext.TokPunct && t.Text == ";") {
			continue
		}
		if t.End <= cursor {
			start = i + 1
		} else if t.Start >= cursor {
			end = i
			break
		}
	}
	return toks[start:end]
}

// quotedPrefix returns the identifier text of a partial quoted identifier
// such as `"My Ta` (closed reports whether text ends with its closing quote).
func quotedPrefix(text string, closed bool) string {
	text = strings.TrimPrefix(text, "U&")
	q := text[:1]
	text = text[1:]
	if closed {
		text = strings.TrimSuffix(text, q)
	}
	return strings.ReplaceAll(text, q+q, q)
}

// unterminated reports whether a string or comment token is still open,
// i.e. the cursor at its end is inside it.
func unterminated(t sqltext.Token, d db.DriverName) bool {
	if strings.HasPrefix(t.Text, "--") || strings.HasPrefix(t.Text, "#") {
		return true
	}
	probe := sqltext.Tokenize(t.Text+"\n", d)
	return len(probe) > 0 && probe[0].End > len(t.Text)
}

// keywordCase matches the case of the typed prefix, or of the previous
// keyword when nothing is typed: all-lowercase yields lowercase keywords.
func keywordCase(prefix string, before []sqltext.Token) func(string) string {
	ref := prefix
	if ref == "" {
		for i := len(before) - 1; i >= 0; i-- {
			if before[i].Kind == sqltext.TokKeyword {
				ref = before[i].Text
				break
			}
		}
	}
	if strings.IndexFunc(ref, unicode.IsLower) >= 0 && strings.IndexFunc(ref, unicode.IsUpper) < 0 {
		return strings.ToLower
	}
	return strings.ToUpper
}

var tableKeywords = map[string]bool{
	"FROM": true, "JOIN": true, "INTO": true, "UPDATE": true, "TABLE": true, "TRUNCATE": true, "ONLY": true,
}

var clauseKeywords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "GROUP": true, "ORDER": true, "HAVING": true, "SET": true,
	"VALUES": true, "JOIN": true, "ON": true, "USING": true, "LIMIT": true, "RETURNING": true, "INTO": true,
	"UPDATE": true, "WINDOW": true,
}

func (cp *completer) isTableContext(before []sqltext.Token) bool {
	n := len(before)
	if n == 0 {
		return false
	}
	switch prev := before[n-1].Upper(); {
	case tableKeywords[prev]:
		return true
	case n == 1 && (prev == "DESCRIBE" || prev == "DESC" || (prev == "EXPLAIN" && cp.d.Driver == db.MySQL)):
		return true
	case prev == ",":
		clause := currentClause(before)
		return clause == "FROM" || clause == "UPDATE"
	}
	return false
}

// currentClause returns the last clause keyword at the cursor's nesting
// level.
func currentClause(before []sqltext.Token) string {
	depth := 0
	for i := len(before) - 1; i >= 0; i-- {
		switch t := before[i]; {
		case t.Text == ")":
			depth++
		case t.Text == "(":
			if depth == 0 {
				return ""
			}
			depth--
		case depth == 0 && t.Kind == sqltext.TokKeyword && clauseKeywords[t.Upper()]:
			return t.Upper()
		}
	}
	return ""
}

var exprKeywords = map[string]bool{
	"SELECT": true, "WHERE": true, "AND": true, "OR": true, "ON": true, "BY": true, "SET": true,
	"HAVING": true, "NOT": true, "DISTINCT": true, "WHEN": true, "THEN": true, "ELSE": true, "CASE": true,
	"RETURNING": true, "IN": true, "LIKE": true, "ILIKE": true, "BETWEEN": true, "IS": true, "ALL": true,
	"ANY": true, "EXISTS": true, "USING": true, "VALUES": true,
}

func (cp *completer) isExprContext(prev sqltext.Token) bool {
	switch prev.Kind {
	case sqltext.TokOperator:
		return true
	case sqltext.TokPunct:
		return prev.Text == "(" || prev.Text == ","
	case sqltext.TokKeyword:
		return exprKeywords[prev.Upper()]
	}
	return false
}

// add appends an identifier item.
func (cp *completer) add(kind ItemKind, name, detail string) {
	cp.items = append(cp.items, Item{Label: name, Insert: ident(cp.d, name, cp.quoted), Kind: kind, Detail: detail})
}

// searchPath returns the schemas (Postgres) or database (MySQL) searched for
// unqualified names.
func (cp *completer) searchPath() []string {
	if len(cp.ctx.SearchPath) > 0 {
		return cp.ctx.SearchPath
	}
	if cp.d.Driver == db.MySQL {
		if cp.ctx.Database == "" {
			return nil
		}
		return []string{cp.ctx.Database}
	}
	return []string{"public"}
}

// location maps a qualifier to the cache's (database, schema) pair.
func (cp *completer) location(qual string) (database, schema string) {
	if cp.d.Driver == db.MySQL {
		return qual, qual
	}
	return cp.ctx.Database, qual
}

// tableColumns resolves a possibly qualified table name to its columns.
func (cp *completer) tableColumns(qual, name string) []string {
	if qual != "" {
		database, schema := cp.location(qual)
		return cp.c.Columns(database, schema, name)
	}
	for _, s := range cp.searchPath() {
		database, schema := cp.location(s)
		if cols := cp.c.Columns(database, schema, name); cols != nil {
			return cols
		}
	}
	return nil
}

func objectKind(k db.ObjectKind) (ItemKind, bool) {
	switch k {
	case db.KindTable:
		return ItemTable, true
	case db.KindView, db.KindMatView:
		return ItemView, true
	case db.KindFunction, db.KindProcedure:
		return ItemFunction, true
	}
	return "", false
}

// member completes after "qualifier.": columns of an alias/table, or
// objects of a schema/database.
func (cp *completer) member(before []sqltext.Token) {
	var quals []string
	for i := len(before) - 1; i >= 1 && len(quals) < 2 && before[i].Text == "." && isName(before[i-1]); i -= 2 {
		quals = append([]string{identName(before[i-1])}, quals...)
	}
	switch len(quals) {
	case 0:
		return
	case 2:
		database, schema := cp.location(quals[0])
		for _, col := range cp.c.Columns(database, schema, quals[1]) {
			cp.add(ItemColumn, col, quals[1])
		}
		return
	}
	q := quals[0]
	for _, r := range cp.refs {
		if strings.EqualFold(r.alias, q) || (r.alias == "" && strings.EqualFold(r.name, q)) {
			if cols := cp.tableColumns(r.qual, r.name); cols != nil {
				for _, col := range cols {
					cp.add(ItemColumn, col, r.name)
				}
				return
			}
		}
	}
	database, schema := cp.location(q)
	if objs := cp.c.Objects(database, schema); objs != nil {
		for _, o := range objs {
			if k, ok := objectKind(o.Kind); ok {
				cp.add(k, o.Name, q)
			}
		}
		return
	}
	for _, col := range cp.tableColumns("", q) {
		cp.add(ItemColumn, col, q)
	}
}

// tables offers relations in the search path, then schemas/databases, then
// qualified relations elsewhere.
func (cp *completer) tables() {
	path := cp.searchPath()
	inPath := map[string]bool{}
	for _, s := range path {
		inPath[strings.ToLower(s)] = true
		database, schema := cp.location(s)
		for _, o := range cp.c.Objects(database, schema) {
			if k, ok := objectKind(o.Kind); ok && k != ItemFunction {
				cp.add(k, o.Name, s)
			}
		}
	}
	var others []string
	if cp.d.Driver == db.MySQL {
		others = cp.c.Databases()
		cp.databases()
	} else {
		others = cp.c.Schemas(cp.ctx.Database)
		for _, s := range others {
			if !IsSystemSchema(s) {
				cp.add(ItemSchema, s, "")
			}
		}
	}
	for _, s := range others {
		if inPath[strings.ToLower(s)] {
			continue
		}
		database, schema := cp.location(s)
		for _, o := range cp.c.Objects(database, schema) {
			if k, ok := objectKind(o.Kind); ok && k != ItemFunction {
				cp.items = append(cp.items, Item{
					Label:  s + "." + o.Name,
					Insert: ident(cp.d, s, cp.quoted) + "." + ident(cp.d, o.Name, cp.quoted),
					Kind:   k,
					Detail: s,
				})
			}
		}
	}
}

func (cp *completer) databases() {
	for _, name := range cp.c.Databases() {
		cp.add(ItemDatabase, name, "")
	}
}

// columns offers the columns of every table referenced in the statement.
func (cp *completer) columns() {
	for _, r := range cp.refs {
		if r.name == "" {
			continue
		}
		for _, col := range cp.tableColumns(r.qual, r.name) {
			cp.add(ItemColumn, col, r.name)
		}
	}
}

// aliases offers aliases and unaliased table names usable as qualifiers.
func (cp *completer) aliases() {
	for _, r := range cp.refs {
		if r.alias != "" {
			cp.add(ItemAlias, r.alias, r.name)
		} else if r.name != "" {
			cp.add(ItemTable, r.name, r.qual)
		}
	}
}

// functions offers built-in and cached user functions.
func (cp *completer) functions() {
	if cp.quoted {
		return
	}
	paren := "("
	if cp.parenAt {
		paren = ""
	}
	for _, f := range functions(cp.d.Driver) {
		name := cp.kwCase(f)
		cp.items = append(cp.items, Item{Label: name, Insert: name + paren, Kind: ItemFunction, Detail: "built-in"})
	}
	for _, s := range cp.searchPath() {
		database, schema := cp.location(s)
		for _, o := range cp.c.Objects(database, schema) {
			if k, _ := objectKind(o.Kind); k == ItemFunction {
				cp.items = append(cp.items, Item{Label: o.Name, Insert: ident(cp.d, o.Name, false) + paren, Kind: ItemFunction, Detail: s})
			}
		}
	}
}

// commonKeywords are offered before the alphabetical rest.
var commonKeywords = strings.Fields(`SELECT FROM WHERE AND OR JOIN LEFT INNER ON AS ORDER GROUP BY
	HAVING LIMIT INSERT INTO VALUES UPDATE SET DELETE CREATE ALTER DROP TABLE DISTINCT NOT NULL IS IN
	LIKE BETWEEN CASE WHEN THEN ELSE END EXISTS UNION WITH`)

func (cp *completer) keywords() {
	if cp.quoted {
		return
	}
	kws := sqltext.Keywords(cp.d.Driver)
	for _, k := range append(commonKeywords[:len(commonKeywords):len(commonKeywords)], kws...) {
		k = cp.kwCase(k)
		cp.items = append(cp.items, Item{Label: k, Insert: k, Kind: ItemKeyword})
	}
}

type itemSource []Item

func (s itemSource) String(i int) string { return s[i].Label }
func (s itemSource) Len() int            { return len(s) }

// rank removes duplicates, puts case-insensitive prefix matches first (in
// generation order), then fuzzy matches by score, keeping at most maxItems.
func rank(items []Item, prefix string) []Item {
	seen := map[Item]bool{}
	uniq := items[:0]
	for _, it := range items {
		if !seen[it] {
			seen[it] = true
			uniq = append(uniq, it)
		}
	}
	if prefix == "" {
		return uniq[:min(len(uniq), maxItems)]
	}
	lp := strings.ToLower(prefix)
	var out, rest []Item
	for _, it := range uniq {
		if strings.HasPrefix(strings.ToLower(it.Label), lp) {
			out = append(out, it)
		} else {
			rest = append(rest, it)
		}
	}
	if len(out) < maxItems {
		matches := fuzzy.FindFromNoSort(prefix, itemSource(rest))
		sort.SliceStable(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
		for _, m := range matches {
			out = append(out, rest[m.Index])
		}
	}
	return out[:min(len(out), maxItems)]
}
