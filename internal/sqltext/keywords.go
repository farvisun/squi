package sqltext

import (
	"slices"
	"strings"

	"github.com/farvisun/squi/internal/db"
)

// commonKeywords are keywords shared by Postgres and MySQL.
var commonKeywords = []string{
	"ADD", "ALL", "ALTER", "ANALYZE", "AND", "ANY", "AS", "ASC", "BEGIN", "BETWEEN", "BOTH", "BY",
	"CALL", "CASCADE", "CASE", "CAST", "CHECK", "COLLATE", "COLUMN", "COMMENT", "COMMIT", "CONSTRAINT",
	"CREATE", "CROSS", "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER", "DATABASE",
	"DEFAULT", "DELETE", "DESC", "DESCRIBE", "DISTINCT", "DROP", "ELSE", "END", "ESCAPE", "EXCEPT",
	"EXECUTE", "EXISTS", "EXPLAIN", "FALSE", "FETCH", "FOR", "FOREIGN", "FROM", "FULL", "FUNCTION",
	"GRANT", "GROUP", "HAVING", "IF", "IN", "INDEX", "INNER", "INSERT", "INTERSECT", "INTERVAL", "INTO",
	"IS", "JOIN", "KEY", "LEADING", "LEFT", "LIKE", "LIMIT", "LOCK", "NATURAL", "NOT", "NULL", "OFFSET",
	"ON", "OR", "ORDER", "OUTER", "OVER", "PARTITION", "PRIMARY", "PROCEDURE", "REFERENCES", "RENAME",
	"REPLACE", "RESTRICT", "REVOKE", "RIGHT", "ROLLBACK", "ROW", "ROWS", "SAVEPOINT", "SCHEMA", "SELECT",
	"SET", "SHOW", "TABLE", "THEN", "TO", "TRAILING", "TRANSACTION", "TRIGGER", "TRUE", "TRUNCATE",
	"UNION", "UNIQUE", "UPDATE", "USING", "VALUES", "VIEW", "WHEN", "WHERE", "WINDOW", "WITH",
}

var pgKeywords = []string{
	"ABORT", "ARRAY", "ASYMMETRIC", "CLUSTER", "CONCURRENTLY", "CONFLICT", "COPY", "DEFERRABLE", "DO",
	"EXCLUDE", "EXTENSION", "FILTER", "FREEZE", "ILIKE", "INHERITS", "ISNULL", "LATERAL", "LISTEN",
	"MATERIALIZED", "NOTHING", "NOTIFY", "NOTNULL", "NULLS", "ONLY", "OWNER", "REINDEX", "REFRESH",
	"RELEASE", "RESET", "RETURNING", "SEQUENCE", "SIMILAR", "SYMMETRIC", "TABLESPACE", "TEMPORARY",
	"UNLOGGED", "VACUUM", "VARIADIC", "VERBOSE",
}

var mysqlKeywords = []string{
	"AUTO_INCREMENT", "CHANGE", "CHARSET", "DATABASES", "DELIMITER", "DIV", "DUPLICATE", "ENGINE",
	"FORCE", "FULLTEXT", "HANDLER", "IGNORE", "KILL", "LOAD", "MODIFY", "OPTIMIZE", "OUTFILE",
	"PROCESSLIST", "REGEXP", "REPAIR", "RLIKE", "SQL_CALC_FOUND_ROWS", "STRAIGHT_JOIN", "TABLES",
	"UNLOCK", "UNSIGNED", "USE", "XOR", "ZEROFILL",
}

var (
	pgKeywordSet    = keywordSet(commonKeywords, pgKeywords)
	mysqlKeywordSet = keywordSet(commonKeywords, mysqlKeywords)
)

func keywordSet(lists ...[]string) map[string]bool {
	m := map[string]bool{}
	for _, l := range lists {
		for _, k := range l {
			m[k] = true
		}
	}
	return m
}

// IsKeyword reports whether word (any case) is a keyword of dialect d.
func IsKeyword(word string, d db.DriverName) bool {
	if d == db.MySQL {
		return mysqlKeywordSet[strings.ToUpper(word)]
	}
	return pgKeywordSet[strings.ToUpper(word)]
}

// Keywords returns the sorted, upper-case keyword list of a dialect.
func Keywords(d db.DriverName) []string {
	extra := pgKeywords
	if d == db.MySQL {
		extra = mysqlKeywords
	}
	out := append(slices.Clone(commonKeywords), extra...)
	slices.Sort(out)
	return out
}
