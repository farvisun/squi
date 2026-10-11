// Package sqltext provides dialect-aware SQL lexing: a tokenizer, a script
// splitter, cursor/position helpers and a statement classifier used for
// read-only enforcement and destructive-statement warnings.
package sqltext

import (
	"strings"
	"unicode/utf8"

	"github.com/farvisun/squi/internal/db"
)

// TokenKind is the lexical class of a Token.
type TokenKind int

const (
	TokWhitespace  TokenKind = iota
	TokComment               // -- …, # … (MySQL), /* … */ (incl. MySQL /*! … */)
	TokKeyword               // keyword of the dialect (see IsKeyword)
	TokIdent                 // unquoted identifier; also MySQL @var / @@var
	TokQuotedIdent           // "ident" or `ident` (MySQL)
	TokString                // '…', E'…', $tag$…$tag$, MySQL "…"
	TokNumber                // 42, 3.14, 1e10, 0xFF
	TokOperator              // + - * / = <> :: || -> etc.
	TokPunct                 // ( ) [ ] , ; .
	TokParam                 // $1, ? (MySQL), :name
	TokDelimiter             // MySQL "DELIMITER x" command or an occurrence of a custom delimiter
)

var tokenKindNames = [...]string{"whitespace", "comment", "keyword", "ident", "quoted-ident", "string", "number", "operator", "punct", "param", "delimiter"}

func (k TokenKind) String() string {
	if int(k) < len(tokenKindNames) {
		return tokenKindNames[k]
	}
	return "unknown"
}

// Token is a lexical token. Start and End are byte offsets into the source;
// Text is src[Start:End].
type Token struct {
	Kind       TokenKind
	Text       string
	Start, End int
}

// IsWord reports whether t is a keyword or an unquoted identifier.
func (t Token) IsWord() bool { return t.Kind == TokKeyword || t.Kind == TokIdent }

// Trivia reports whether t is whitespace or a comment.
func (t Token) Trivia() bool { return t.Kind == TokWhitespace || t.Kind == TokComment }

// Upper returns the upper-cased text of a word token, or the raw text.
func (t Token) Upper() string {
	if t.IsWord() {
		return strings.ToUpper(t.Text)
	}
	return t.Text
}

// IsHint reports whether t is a comment the server interprets: a MySQL
// versioned comment (/*! … */) or an optimizer hint (/*+ … */).
func (t Token) IsHint() bool {
	return t.Kind == TokComment && (strings.HasPrefix(t.Text, "/*!") || strings.HasPrefix(t.Text, "/*+"))
}

// Tokenize splits src into tokens. Concatenating the Text of all tokens
// reproduces src exactly. Unterminated strings and comments extend to the
// end of the input.
func Tokenize(src string, d db.DriverName) []Token {
	l := lexer{src: src, driver: d, mysql: d == db.MySQL, delim: ";"}
	var toks []Token
	prevSig := -1
	for l.pos < len(src) {
		start := l.pos
		kind := l.next()
		t := Token{Kind: kind, Text: src[start:l.pos], Start: start, End: l.pos}
		if kind == TokKeyword && prevSig >= 0 && toks[prevSig].Text == "." {
			t.Kind = TokIdent // qualified names are never keywords: t.user
		}
		if !t.Trivia() {
			prevSig = len(toks)
		}
		toks = append(toks, t)
	}
	return toks
}

type lexer struct {
	src    string
	pos    int
	driver db.DriverName
	mysql  bool
	delim  string // current MySQL client delimiter
}

func (l *lexer) peek(off int) byte {
	if p := l.pos + off; p < len(l.src) {
		return l.src[p]
	}
	return 0
}

// next consumes one token starting at l.pos and returns its kind.
func (l *lexer) next() TokenKind {
	c := l.src[l.pos]
	if l.mysql {
		if (c == 'd' || c == 'D') && l.atLineStart() {
			if delim, n := parseDelimiterCommand(l.src[l.pos:]); n > 0 {
				l.delim = delim
				l.pos += n
				return TokDelimiter
			}
		}
		if l.atCustomDelim() {
			l.pos += len(l.delim)
			return TokDelimiter
		}
	}
	switch {
	case isSpace(c):
		for l.pos < len(l.src) && isSpace(l.src[l.pos]) {
			l.pos++
		}
		return TokWhitespace
	case c == '-' && l.peek(1) == '-' && (!l.mysql || l.pos+2 >= len(l.src) || l.peek(2) <= ' '):
		l.lineComment()
		return TokComment
	case c == '#' && l.mysql:
		l.lineComment()
		return TokComment
	case c == '/' && l.peek(1) == '*':
		l.blockComment()
		return TokComment
	case c == '\'':
		l.quoted('\'', l.mysql)
		return TokString
	case c == '"':
		if l.mysql { // without ANSI_QUOTES, "…" is a string in MySQL
			l.quoted('"', true)
			return TokString
		}
		l.quoted('"', false)
		return TokQuotedIdent
	case c == '`' && l.mysql:
		l.quoted('`', false)
		return TokQuotedIdent
	case (c == 'e' || c == 'E') && l.peek(1) == '\'' && !l.mysql:
		l.pos++
		l.quoted('\'', true)
		return TokString
	case strings.IndexByte("bBxXnN", c) >= 0 && l.peek(1) == '\'':
		l.pos++
		l.quoted('\'', l.mysql)
		return TokString
	case (c == 'u' || c == 'U') && l.peek(1) == '&' && (l.peek(2) == '\'' || l.peek(2) == '"') && !l.mysql:
		q := l.peek(2)
		l.pos += 2
		l.quoted(q, false)
		if q == '"' {
			return TokQuotedIdent
		}
		return TokString
	case c == '$' && !l.mysql:
		return l.dollar()
	case isDigit(c) || (c == '.' && isDigit(l.peek(1))):
		l.number()
		return TokNumber
	case isIdentStart(c) || (c == '$' && l.mysql):
		return l.word()
	case c == '@' && l.mysql && (isIdentChar(l.peek(1)) || l.peek(1) == '@'):
		l.pos++
		if l.peek(0) == '@' {
			l.pos++
		}
		l.identChars()
		return TokIdent
	case c == '?' && l.mysql:
		l.pos++
		return TokParam
	case c == ':' && l.peek(1) == ':':
		l.pos += 2
		return TokOperator
	case c == ':' && isIdentStart(l.peek(1)) && (l.pos == 0 || l.src[l.pos-1] != ':'):
		l.pos++
		for l.pos < len(l.src) && isIdentChar(l.src[l.pos]) {
			l.pos++
		}
		return TokParam
	case strings.IndexByte("()[]{},;.", c) >= 0:
		l.pos++
		return TokPunct
	case isOpChar(c, l.mysql):
		l.operator()
		return TokOperator
	}
	_, n := utf8.DecodeRuneInString(l.src[l.pos:])
	l.pos += n
	return TokOperator
}

// atLineStart reports whether only spaces/tabs precede l.pos on its line.
func (l *lexer) atLineStart() bool {
	for i := l.pos - 1; i >= 0; i-- {
		switch l.src[i] {
		case '\n':
			return true
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

// parseDelimiterCommand parses a MySQL client "DELIMITER x" command at the
// start of s. It returns the new delimiter and the length of the command
// (up to, not including, the line break), or n == 0 if s is not one.
func parseDelimiterCommand(s string) (delim string, n int) {
	const kw = "delimiter"
	if len(s) <= len(kw) || !strings.EqualFold(s[:len(kw)], kw) || (s[len(kw)] != ' ' && s[len(kw)] != '\t') {
		return "", 0
	}
	end := strings.IndexByte(s, '\n')
	if end < 0 {
		end = len(s)
	}
	line := strings.TrimRight(s[:end], "\r")
	fields := strings.Fields(line[len(kw):])
	if len(fields) == 0 {
		return "", 0
	}
	return fields[0], len(line)
}

func (l *lexer) lineComment() {
	if i := strings.IndexByte(l.src[l.pos:], '\n'); i >= 0 {
		l.pos += i
	} else {
		l.pos = len(l.src)
	}
}

// blockComment consumes /* … */; Postgres comments nest, MySQL ones do not.
func (l *lexer) blockComment() {
	depth := 0
	for l.pos < len(l.src) {
		switch {
		case l.src[l.pos] == '/' && l.peek(1) == '*':
			depth++
			l.pos += 2
			if l.mysql && depth > 1 {
				depth = 1
			}
		case l.src[l.pos] == '*' && l.peek(1) == '/':
			depth--
			l.pos += 2
			if depth == 0 {
				return
			}
		default:
			l.pos++
		}
	}
}

// quoted consumes a q-quoted run starting at l.pos (which holds q). A doubled
// quote is an escaped quote; with backslash, \x escapes any character.
func (l *lexer) quoted(q byte, backslash bool) {
	l.pos++
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\' && backslash:
			l.pos += 2
		case c == q && l.peek(1) == q:
			l.pos += 2
		case c == q:
			l.pos++
			return
		default:
			l.pos++
		}
	}
	l.pos = len(l.src)
}

// dollar handles '$' in Postgres: $1 params and $tag$…$tag$ strings.
func (l *lexer) dollar() TokenKind {
	if isDigit(l.peek(1)) {
		l.pos++
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
		return TokParam
	}
	j := l.pos + 1
	if j < len(l.src) && isIdentStart(l.src[j]) {
		for j < len(l.src) && isIdentChar(l.src[j]) && l.src[j] != '$' {
			j++
		}
	}
	if j >= len(l.src) || l.src[j] != '$' {
		l.pos++
		return TokOperator
	}
	tag := l.src[l.pos : j+1]
	if end := strings.Index(l.src[j+1:], tag); end >= 0 {
		l.pos = j + 1 + end + len(tag)
	} else {
		l.pos = len(l.src)
	}
	return TokString
}

func (l *lexer) number() {
	s := l.src
	if s[l.pos] == '0' && strings.IndexByte("xXbBoO", l.peek(1)) >= 0 && isHexDigit(l.peek(2)) {
		l.pos += 2
		for l.pos < len(s) && (isHexDigit(s[l.pos]) || s[l.pos] == '_') {
			l.pos++
		}
		return
	}
	digits := func() {
		for l.pos < len(s) && (isDigit(s[l.pos]) || s[l.pos] == '_') {
			l.pos++
		}
	}
	digits()
	if l.peek(0) == '.' && l.peek(1) != '.' {
		l.pos++
		digits()
	}
	if c := l.peek(0); c == 'e' || c == 'E' {
		n := 1
		if p := l.peek(1); p == '+' || p == '-' {
			n = 2
		}
		if isDigit(l.peek(n)) {
			l.pos += n
			digits()
		}
	}
}

func (l *lexer) word() TokenKind {
	start := l.pos
	l.identChars()
	if IsKeyword(l.src[start:l.pos], l.driver) {
		return TokKeyword
	}
	return TokIdent
}

// identChars consumes identifier characters, stopping at a custom MySQL
// delimiter (as in "END$$").
func (l *lexer) identChars() {
	for l.pos < len(l.src) && isIdentChar(l.src[l.pos]) && !l.atCustomDelim() {
		l.pos++
	}
}

func (l *lexer) atCustomDelim() bool {
	return l.mysql && l.delim != ";" && strings.HasPrefix(l.src[l.pos:], l.delim)
}

// operator consumes a run of operator characters, never swallowing the start
// of a comment and (as in Postgres) not ending a multi-character operator in
// + or - unless it contains one of ~ ! @ # % ^ & | ` ?.
func (l *lexer) operator() {
	start := l.pos
	for l.pos < len(l.src) && isOpChar(l.src[l.pos], l.mysql) {
		if l.pos > start && ((l.src[l.pos] == '-' && l.peek(1) == '-') || (l.src[l.pos] == '/' && l.peek(1) == '*')) {
			break
		}
		if l.pos > start && l.atCustomDelim() {
			break
		}
		l.pos++
	}
	for l.pos-start > 1 {
		last := l.src[l.pos-1]
		if (last != '+' && last != '-') || strings.ContainsAny(l.src[start:l.pos], "~!@#%^&|`?") {
			break
		}
		l.pos--
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}
func isDigit(c byte) bool    { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool { return isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'f') }
func isIdentStart(c byte) bool {
	return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') || c >= 0x80
}
func isIdentChar(c byte) bool { return isIdentStart(c) || isDigit(c) || c == '$' }

func isOpChar(c byte, mysql bool) bool {
	if mysql && (c == '#' || c == '?' || c == '`' || c == '@') {
		return false
	}
	return strings.IndexByte("+-*/<>=~!@#%^&|`?:", c) >= 0
}
