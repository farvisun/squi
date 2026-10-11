package sqltext

import (
	"errors"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// CleanCondition checks a user-typed WHERE condition before it is embedded
// in a generated "SELECT … WHERE (cond)" statement. Comments are dropped
// (a line comment would swallow the closing parenthesis), and conditions
// that could end the statement or escape the parentheses are rejected: a
// statement separator, a client delimiter or unbalanced parentheses.
func CleanCondition(cond string, d db.DriverName) (string, error) {
	var b strings.Builder
	depth := 0
	for _, t := range Tokenize(cond, d) {
		switch {
		case t.Kind == TokComment && !t.IsHint():
			b.WriteByte(' ')
			continue
		case t.Kind == TokDelimiter, t.Kind == TokPunct && t.Text == ";":
			return "", errors.New("the condition must not contain ';' — it filters one SELECT")
		case t.Kind == TokPunct && t.Text == "(":
			depth++
		case t.Kind == TokPunct && t.Text == ")":
			if depth--; depth < 0 {
				return "", errors.New("unbalanced parentheses in the condition")
			}
		case t.Kind == TokString && !closedString(t.Text):
			return "", errors.New("unterminated string in the condition")
		}
		b.WriteString(t.Text)
	}
	if depth != 0 {
		return "", errors.New("unbalanced parentheses in the condition")
	}
	return strings.Join(strings.Fields(b.String()), " "), nil
}

// closedString reports whether a string token has its closing quote.
func closedString(s string) bool {
	if strings.HasPrefix(s, "$") { // $tag$…$tag$
		end := strings.IndexByte(s[1:], '$')
		if end < 0 {
			return false
		}
		tag := s[:end+2]
		return len(s) >= 2*len(tag) && strings.HasSuffix(s, tag)
	}
	i := strings.IndexAny(s, `'"`)
	if i < 0 || len(s) < i+2 {
		return false
	}
	q := s[i]
	body := s[i+1:]
	if body[len(body)-1] != q {
		return false
	}
	// an odd run of trailing quotes closes the string ('it''s' ends in ');
	// a backslash-escaped last quote is left for the server to reject
	n := 0
	for j := len(body) - 1; j >= 0 && body[j] == q; j-- {
		n++
	}
	return n%2 == 1
}
